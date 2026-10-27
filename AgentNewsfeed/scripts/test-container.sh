#!/bin/sh
# Local synthetic acceptance only: no published ports, runtime network or secrets.
set -eu
image=${NEWSFEED_TEST_IMAGE:-agentnewsfeed:local}
image_id=$(docker image inspect --format '{{.Id}}' "$image")
architecture=$(docker image inspect --format '{{.Architecture}}' "$image_id")
case "$architecture" in amd64|arm64) ;; *) echo 'Expected a Linux amd64/arm64 image' >&2; exit 1 ;; esac
platform="linux/$architecture"
root=$(pwd)
artifact_dir=.development/private-feed/container
mkdir -p "$artifact_dir"
test_binary="$artifact_dir/acceptance-linux-$architecture.test"
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go test -c -o "$test_binary" ./internal/acceptance
# Go emits owner-only test executables; the nonroot image uses a different UID.
chmod 0555 "$test_binary"
volume="newsfeed-acceptance-$$"
runner="newsfeed-acceptance-runner-$$"
persisted="newsfeed-acceptance-restart-$$"
cleanup() {
  docker rm -f "$runner" "$persisted" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
docker volume create "$volume" >/dev/null
docker run --platform "$platform" --name "$runner" --read-only --network none --cap-drop ALL --security-opt no-new-privileges --pids-limit 128 \
  --mount "type=volume,source=$volume,target=/data" \
  --mount "type=bind,source=$root/$test_binary,target=/acceptance.test,readonly" \
  -e TMPDIR=/data -e NEWSFEED_CONTAINER_TEST=seed \
  --entrypoint /acceptance.test "$image_id" -test.v -test.timeout=90s
docker rm "$runner" >/dev/null
docker run --platform "$platform" -d --name "$persisted" --read-only --network none --cap-drop ALL --security-opt no-new-privileges --pids-limit 128 \
  --health-interval=1s --health-start-period=1ms --health-start-interval=100ms --health-retries=1 \
  --mount "type=volume,source=$volume,target=/data" \
  --mount "type=bind,source=$root/$test_binary,target=/acceptance.test,readonly" "$image_id" >/dev/null
test "$(docker inspect --format '{{.Config.User}}' "$persisted")" = '65532:65532'
test "$(docker inspect --format '{{.HostConfig.ReadonlyRootfs}}' "$persisted")" = true
test "$(docker inspect --format '{{.HostConfig.NetworkMode}}' "$persisted")" = none
test "$(docker inspect --format '{{len .HostConfig.PortBindings}}' "$persisted")" = 0
wait_live() {
  attempts=0
  until docker exec "$persisted" /newsfeed healthcheck live >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 40 ]; then docker logs "$persisted"; exit 1; fi
    sleep 0.1
  done
  if docker exec "$persisted" /newsfeed healthcheck ready >/dev/null 2>&1; then
    echo 'Unconfigured identity incorrectly reported ready' >&2
    exit 1
  fi
}
verify() {
  docker exec -e NEWSFEED_CONTAINER_TEST=verify "$persisted" /acceptance.test -test.run '^TestContainerPersistedFixture$' -test.v -test.timeout=20s
}
wait_live
attempts=0
until [ "$(docker inspect --format '{{.State.Health.Status}}' "$persisted")" = unhealthy ]; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 150 ]; then echo 'Docker readiness healthcheck did not fail closed' >&2; exit 1; fi
  sleep 0.1
done
verify
docker stop --time 10 "$persisted" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$persisted")" = 0
docker start "$persisted" >/dev/null
wait_live
verify
docker stop --time 10 "$persisted" >/dev/null
test "$(docker inspect --format '{{.State.ExitCode}}' "$persisted")" = 0
printf 'Local image %s (%s): uid65532, read-only root, fail-closed Docker readiness, actual container stop/start and unchanged private SQLite/retry results passed.\n' "$image_id" "$platform"
