#!/usr/bin/env bash
set -uo pipefail
cd /home/admin/infrastructure/google-health || exit 1

# Attempt both phases, even if one reports partial coverage. Keep the service
# failed when either phase failed so systemd/journal monitoring stays truthful.
sync_status=0
docker compose --project-name google-health run --rm --no-deps importer sync --timeout 5h || sync_status=$?
backfill_status=0
docker compose --project-name google-health run --rm --no-deps importer backfill --max-pages 500 --timeout 5h || backfill_status=$?
if (( sync_status != 0 || backfill_status != 0 )); then
  exit 1
fi
