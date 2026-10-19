#!/bin/sh
# Local-only acceptance. No ports, external network, credentials or live services.
set -eu
image=${SHOPPING_TEST_IMAGE:-agentshoppinglist:local}
architecture=$(docker image inspect --format '{{.Architecture}}' "$image")
case "$architecture" in amd64|arm64) ;; *) echo 'Expected a Linux amd64/arm64 release image' >&2; exit 1 ;; esac
platform="linux/$architecture"
root=$(pwd)
mkdir -p .cache/acceptance
test_binary=".cache/acceptance/process-$architecture.test"
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go test -c -o "$test_binary" ./internal/acceptance
volume="shopping-acceptance-$$"
runner="shopping-acceptance-runner-$$"
persisted="shopping-acceptance-restart-$$"
cleanup() {
  docker rm -f "$runner" "$persisted" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
docker volume create "$volume" >/dev/null
docker run --platform "$platform" --name "$runner" --read-only --network none --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=volume,source=$volume,target=/data" \
  --mount "type=bind,source=$root/$test_binary,target=/acceptance.test,readonly" \
  -e TMPDIR=/data -e SHOPPING_PROCESS_TEST=1 \
  -e SHOPPING_ACCEPTANCE_BINARY=/shopping -e SHOPPING_ACCEPTANCE_ADMIN_BINARY=/shopping-admin \
  -e SHOPPING_ACCEPTANCE_EXPORT=/data/acceptance.tar \
  --entrypoint /acceptance.test "$image" -test.v -test.timeout=120s
docker rm "$runner" >/dev/null
docker run --platform "$platform" --rm --read-only --network none --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=volume,source=$volume,target=/data" --entrypoint /shopping-admin "$image" \
  restore --archive /data/acceptance.tar --target /data/shopping.sqlite >/dev/null
docker run --platform "$platform" -d --name "$persisted" --read-only --network none --cap-drop ALL --security-opt no-new-privileges \
  --mount "type=volume,source=$volume,target=/data" "$image" >/dev/null
wait_live() {
  attempts=0
  until docker exec "$persisted" /shopping healthcheck live >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 40 ]; then docker logs "$persisted"; exit 1; fi
    sleep 0.1
  done
}
wait_live
if docker exec "$persisted" /shopping healthcheck ready >/dev/null 2>&1; then
  echo 'Unconfigured identity shell incorrectly reported ready' >&2
  exit 1
fi
docker exec "$persisted" /shopping-admin inspect --db /data/shopping.sqlite > ".cache/acceptance/container-$architecture-before.json"
docker stop --time 10 "$persisted" >/dev/null
docker start "$persisted" >/dev/null
wait_live
docker exec "$persisted" /shopping-admin inspect --db /data/shopping.sqlite > ".cache/acceptance/container-$architecture-after.json"
cmp ".cache/acceptance/container-$architecture-before.json" ".cache/acceptance/container-$architecture-after.json"
docker stop --time 10 "$persisted" >/dev/null
echo "Local $architecture production image: process journey, isolated recovery, actual container stop/start and identical durable counts/totals passed."
