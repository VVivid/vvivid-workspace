#!/usr/bin/env bash
set -euo pipefail

project_dir=/home/admin/infrastructure/google-health
cd "$project_dir"
test -f compose.yaml
test -f secrets/client.json
test -f secrets/tokens/tokens.json
test -d secrets/tokens
test -d /home/admin/data/google-health
test "$(id -un)" = admin

docker compose --project-name google-health config --quiet
docker compose --project-name google-health build importer
docker compose --project-name google-health run --rm --no-deps importer status --check --require-authorized
sudo install -m 0644 deploy/google-health.service /etc/systemd/system/google-health.service
sudo install -m 0644 deploy/google-health.timer /etc/systemd/system/google-health.timer
sudo systemctl daemon-reload
sudo systemctl enable --now google-health.timer
systemctl list-timers google-health.timer --no-pager
