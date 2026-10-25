#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    printf 'Usage: %s <local-image> [expected-base-url]\n' "$0" >&2
    exit 1
fi

image=$1
container=
cleanup() {
    if [ -n "$container" ]; then
        docker rm --force "$container" >/dev/null
    fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

docker run --rm --entrypoint nginx "$image" -t
container=$(docker run --detach --publish 127.0.0.1::80 "$image")
port=$(docker port "$container" 80/tcp)
case "$port" in
    127.0.0.1:*) ;;
    *) printf 'Expected a loopback-only published port.\n' >&2; exit 1 ;;
esac
case "${port#127.0.0.1:}" in
    ''|*[!0-9]*) printf 'Invalid published port.\n' >&2; exit 1 ;;
esac
origin="http://$port"
attempt=0
until [ "$(curl --silent --max-time 5 --noproxy '*' --output /dev/null --write-out '%{http_code}' "$origin/" || true)" = 200 ]; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 20 ]; then
        printf 'Image did not serve HTTP 200.\n' >&2
        exit 1
    fi
    sleep 1
done

leaked_guide=$(docker exec "$container" find /usr/share/nginx/html -type f \( -iname agents.md -o -iname agent.md \) -print -quit)
if [ -n "$leaked_guide" ]; then
    printf 'Agent guide exists in the serving image.\n' >&2
    exit 1
fi
script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
if [ "$#" -eq 2 ]; then
    python3 "$script_dir/test-serving.py" "$origin" "$2"
else
    python3 "$script_dir/test-serving.py" "$origin"
fi
printf 'Serving image passed representation, discovery, cache, and agent-guide checks.\n'
