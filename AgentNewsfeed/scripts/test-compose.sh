#!/bin/sh
# Resolve only synthetic configuration; never load an operator .env or secret file.
set -eu
artifact_dir=.development/private-feed/container
mkdir -p "$artifact_dir"
mkdir -p "$artifact_dir/docker-config"
docker_config="$(pwd)/$artifact_dir/docker-config"
if env -i PATH="$PATH" DOCKER_CONFIG="$docker_config" docker compose --env-file /dev/null -f deploy/compose.yaml config --quiet >"$artifact_dir/compose-missing.txt" 2>&1; then
  echo 'Compose accepted missing required configuration' >&2
  exit 1
fi
env -i PATH="$PATH" DOCKER_CONFIG="$docker_config" \
  NEWSFEED_IMAGE=example.com/newsfeed@sha256:0000000000000000000000000000000000000000000000000000000000000000 \
  NEWSFEED_HOST=newsfeed.example.com NEWSFEED_ORIGIN=https://newsfeed.example.com \
  NEWSFEED_OIDC_ISSUER=https://identity.example.com/realms/synthetic \
  NEWSFEED_WEB_CLIENT_ID=newsfeed-web NEWSFEED_API_AUDIENCE=https://newsfeed.example.com/api/v1 \
  NEWSFEED_MCP_AUDIENCE=https://newsfeed.example.com/mcp NEWSFEED_MCP_CLIENT_IDS=newsfeed-chatgpt \
  NEWSFEED_DATA_VOLUME=newsfeed-synthetic-data NEWSFEED_TRAEFIK_NETWORK=synthetic-existing-traefik \
  NEWSFEED_TRAEFIK_HTTPS_ENTRYPOINT=synthetic-https NEWSFEED_TRAEFIK_CERTRESOLVER=synthetic-resolver \
  NEWSFEED_WEB_CLIENT_SECRET_HOST_FILE=/not-created/newsfeed/web-client \
  NEWSFEED_SESSION_KEYS_HOST_FILE=/not-created/newsfeed/session-keys \
  docker compose --env-file /dev/null -f deploy/compose.yaml config --format json >"$artifact_dir/compose.json"
python3 - "$artifact_dir/compose.json" <<'PY'
import json, sys
with open(sys.argv[1]) as source:
    config = json.load(source)
service = config['services']['newsfeed']
assert service['image'].endswith('@sha256:' + '0' * 64)
assert service['user'] == '65532:65532' and service['read_only'] is True
assert service.get('ports', []) == [] and 'ALL' in service['cap_drop']
assert service['healthcheck']['test'] == ['CMD', '/newsfeed', 'healthcheck', 'ready']
assert service['environment']['NEWSFEED_DB_PATH'] == '/data/newsfeed.sqlite'
assert service['environment']['NEWSFEED_WEB_CLIENT_SECRET_FILE'] == '/run/secrets/web-client'
assert service['environment']['NEWSFEED_SESSION_KEYS_FILE'] == '/run/secrets/session-keys'
assert service['environment']['NEWSFEED_THUMBNAIL_HOSTS'] == ''
assert service['labels']['traefik.http.services.newsfeed.loadbalancer.server.port'] == '8091'
assert config['volumes']['newsfeed-data']['name'] == 'newsfeed-synthetic-data'
assert config['networks']['traefik']['external'] is True
assert len(service['secrets']) == 2
print('Compose required inputs, no backend host port, file-secret paths, dedicated volume, existing network and readiness configuration passed (synthetic resolution only).')
PY
