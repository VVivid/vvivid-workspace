#!/bin/sh
set -eu

IMAGE="${CI_REGISTRY_IMAGE:-registry.100.96.215.95.sslip.io:5005/personal/vvivid.dev}"
PROJECT_PATH="${VVIVID_PROJECT_PATH:-personal/vvivid.dev}"
INFRA_ENV_PATH="${INFRA_ENV_PATH:-${INFRA_DIR:-/infra}/.env}"
KEEP_RELEASES="${VVIVID_RELEASE_KEEP:-3}"
DRY_RUN="${VVIVID_CLEANUP_DRY_RUN:-false}"

is_release_tag() {
  case "$1" in
    ""|"<none>"|preview|preview-*|live|live-*) return 1 ;;
    *) return 0 ;;
  esac
}

contains_word() {
  needle="$1"
  shift
  for value in "$@"; do
    [ "$value" = "$needle" ] && return 0
  done
  return 1
}

run_or_echo() {
  if [ "$DRY_RUN" = "true" ]; then
    printf '[dry-run] %s\n' "$*"
  else
    "$@" || true
  fi
}

version_key() {
  tag="$1"
  clean="${tag#v}"
  major=0
  minor=0
  patch=0
  rest="$clean"
  major="${rest%%.*}"
  [ "$major" = "$rest" ] && rest="" || rest="${rest#*.}"
  if [ -n "$rest" ]; then
    minor="${rest%%.*}"
    [ "$minor" = "$rest" ] && rest="" || rest="${rest#*.}"
  fi
  if [ -n "$rest" ]; then
    patch="${rest%%[-+._]*}"
  fi
  case "$major" in *[!0-9]*|"") major=0 ;; esac
  case "$minor" in *[!0-9]*|"") minor=0 ;; esac
  case "$patch" in *[!0-9]*|"") patch=0 ;; esac
  printf '%010d.%010d.%010d %s\n' "$major" "$minor" "$patch" "$tag"
}

current_live_tag="${VVIVID_LIVE_TAG:-}"
if [ -z "$current_live_tag" ] && [ -f "$INFRA_ENV_PATH" ]; then
  current_live_tag="$(grep '^VVIVID_LIVE_TAG=' "$INFRA_ENV_PATH" | tail -n 1 | cut -d= -f2- || true)"
fi
if [ -z "$current_live_tag" ] && docker ps --format '{{.Names}}' | grep -qx 'vvivid-live'; then
  live_image="$(docker inspect vvivid-live --format '{{.Config.Image}}' 2>/dev/null || true)"
  case "$live_image" in
    "$IMAGE":*) current_live_tag="${live_image#${IMAGE}:}" ;;
  esac
fi

echo "Cleaning vvivid images for $IMAGE; current_live_tag=${current_live_tag:-none}; keep_releases=$KEEP_RELEASES; dry_run=$DRY_RUN"

local_tags="$(docker image ls --format '{{.Repository}} {{.Tag}}' | awk -v image="$IMAGE" '$1 == image { print $2 }' | sort -u || true)"
release_tags=""
for tag in $local_tags; do
  if is_release_tag "$tag"; then
    release_tags="${release_tags}${tag}
"
  fi
done

keep_release_tags=""
if [ -n "$current_live_tag" ]; then
  keep_release_tags="${keep_release_tags}${current_live_tag}
"
fi
sorted_release_tags="$(printf '%s' "$release_tags" | awk 'NF { print }' | while IFS= read -r tag; do version_key "$tag"; done | sort -r | awk '{ print $2 }')"
for tag in $sorted_release_tags; do
  count="$(printf '%s' "$keep_release_tags" | awk 'NF { c++ } END { print c + 0 }')"
  [ "$count" -ge "$KEEP_RELEASES" ] && break
  if ! printf '%s' "$keep_release_tags" | grep -qxF "$tag"; then
    keep_release_tags="${keep_release_tags}${tag}
"
  fi
done

for tag in $local_tags; do
  keep=false
  [ "$tag" = "preview" ] && keep=true
  if is_release_tag "$tag" && printf '%s' "$keep_release_tags" | grep -qxF "$tag"; then
    keep=true
  fi
  if [ "$keep" = false ]; then
    echo "Removing local image tag $IMAGE:$tag"
    run_or_echo docker image rm "$IMAGE:$tag"
  fi
done

local_test_tags="$(docker image ls --format '{{.Repository}} {{.Tag}}' | awk '$1 == "vvivid-dev" { print $2 }' | sort -u || true)"
for tag in $local_test_tags; do
  echo "Removing local test image tag vvivid-dev:$tag"
  run_or_echo docker image rm "vvivid-dev:$tag"
done

registry_keep="preview"
if [ -n "$keep_release_tags" ]; then
  registry_keep="$(printf '%s\n%s' "$registry_keep" "$keep_release_tags" | awk 'NF && !seen[$0]++')"
fi

registry_keep_csv="$(printf '%s' "$registry_keep" | awk 'NF { printf "%s%s", sep, $0; sep="," }')"
rails_script="$(mktemp)"
cat > "$rails_script" <<'RUBY'
project_path = ENV.fetch('VVIVID_PROJECT_PATH')
keep = ENV.fetch('VVIVID_KEEP_TAGS').split(',').reject(&:empty?)
dry_run = ENV.fetch('VVIVID_CLEANUP_DRY_RUN', 'false') == 'true'
release_keep = ENV.fetch('VVIVID_RELEASE_KEEP', '3').to_i
release_keep = 1 if release_keep < 1

project = Project.find_by_full_path(project_path)
raise "Project not found: #{project_path}" unless project
repo = project.container_repositories.first
unless repo
  puts "No container repository found for #{project_path}"
  exit 0
end

def release_tag?(tag)
  return false if tag.nil? || tag.empty?
  return false if tag == 'preview' || tag == 'live'
  return false if tag.start_with?('preview-', 'live-')
  true
end

def version_key(tag)
  parts = tag.sub(/^v/, '').split(/[.+_-]/, 4)
  [parts[0].to_i, parts[1].to_i, parts[2].to_i, tag]
end

tags = repo.tags.map(&:name).sort
release_tags = tags.select { |tag| release_tag?(tag) }
keep_release_tags = keep.select { |tag| release_tag?(tag) }
release_tags.sort_by { |tag| version_key(tag) }.reverse_each do |tag|
  break if keep_release_tags.uniq.size >= release_keep
  keep_release_tags << tag unless keep_release_tags.include?(tag)
end
keep = (['preview'] + keep_release_tags).uniq
stale = tags.reject { |tag| keep.include?(tag) }

puts "Registry tags: #{tags.join(', ')}"
puts "Keeping registry tags: #{keep.join(', ')}"
puts "Deleting registry tags: #{stale.join(', ')}"

if stale.empty? || dry_run
  exit 0
end

user = User.admins.first || User.first
raise 'No GitLab user available for registry cleanup' unless user
result = Projects::ContainerRepository::DeleteTagsService.new(project, user, tags: stale).execute(repo)
puts result.inspect
exit(result[:status] == :success ? 0 : 1)
RUBY

docker exec \
  -e VVIVID_PROJECT_PATH="$PROJECT_PATH" \
  -e VVIVID_KEEP_TAGS="$registry_keep_csv" \
  -e VVIVID_CLEANUP_DRY_RUN="$DRY_RUN" \
  -e VVIVID_RELEASE_KEEP="$KEEP_RELEASES" \
  -i gitlab sh -lc 'cat >/tmp/vvivid_registry_cleanup.rb && gitlab-rails runner /tmp/vvivid_registry_cleanup.rb; status=$?; rm -f /tmp/vvivid_registry_cleanup.rb; exit $status' < "$rails_script"
rm -f "$rails_script"
