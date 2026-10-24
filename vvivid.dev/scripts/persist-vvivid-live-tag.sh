#!/bin/sh
set -eu

tag="${1:?usage: persist-vvivid-live-tag.sh <tag> [env-file]}"
env_file="${2:-/infra/.env}"

if ! echo "$tag" | grep -Eq '^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$'; then
  echo "Invalid Docker tag: $tag" >&2
  exit 1
fi

if [ ! -f "$env_file" ]; then
  echo "Env file not found: $env_file" >&2
  exit 1
fi

tmp="$(mktemp)"
awk -v tag="$tag" '
  BEGIN { written = 0 }
  /^VVIVID_LIVE_TAG=/ {
    if (written == 0) {
      print "VVIVID_LIVE_TAG=" tag
      written = 1
    }
    next
  }
  {
    print
    if ($0 ~ /^VVIVID_PREVIEW_HOST=/ && written == 0) {
      print "VVIVID_LIVE_TAG=" tag
      written = 1
    }
  }
  END {
    if (written == 0) {
      print "VVIVID_LIVE_TAG=" tag
    }
  }
' "$env_file" > "$tmp"
cat "$tmp" > "$env_file"
rm -f "$tmp"
echo "Persisted VVIVID_LIVE_TAG=$tag in $env_file"
