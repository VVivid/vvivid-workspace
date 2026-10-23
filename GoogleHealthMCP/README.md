# Google Health → SQLite

A personal Go importer for detailed Google Health / Fitbit history. V1 provides four commands: `authorize`, `backfill`, `sync`, and `status`. It writes a persistent SQLite database and emits newline-delimited operational JSON. The future SQL interface through the existing SSHMCP is described in [ARCHITECTURE.md](ARCHITECTURE.md); it is not implemented here.

Read [root instructions](../AGENTS.md), the [shared workflow](../docs/DEVELOPMENT-WORKFLOW.md), and [architecture](ARCHITECTURE.md) before development. Resume matching ignored `.development/<task>/` records when present; fresh clones do not contain temporary handoffs.

The source implements authorization, bounded historical import, daily synchronization, read-only status, schema migrations, and atomic publication with resumable pagination. Private operational handoffs remain outside public source. The [migration report](../MIGRATION.md) records historical checks; it does not establish current OAuth, scheduled collection, history completion, or deployed-image status. Inspect runtime state before operational changes; never replay an uncertain writer dispatch.

## What is collected

The importer requests exactly these read permissions:

```text
https://www.googleapis.com/auth/googlehealth.activity_and_fitness.readonly
https://www.googleapis.com/auth/googlehealth.sleep.readonly
https://www.googleapis.com/auth/googlehealth.health_metrics_and_measurements.readonly
```

The [documented Google Health data types](https://developers.google.com/health/data-types) determine the catalog:

| Family | Data types |
| --- | --- |
| Activity | Steps, distance, floors, altitude, active energy, active/zone minutes, activity level, sedentary periods, exercise sessions with their returned detail, swim lengths, time in heart-rate zones, daily/run/sample VO2 max. |
| Sleep | Sessions, returned stages, summaries, and metadata. |
| Vitals | Heart rate and variability, daily HRV/heart-rate zones/resting HR, daily and sample oxygen saturation, daily respiration and sleep respiration summaries, nightly temperature derivations, core temperature, weight, body fat, height, blood glucose. |
| Beta capability | `skin-temperature-sensors` uses `v4beta`; availability depends on device and API access. |
| Derived daily metrics | Total calories and calories in heart-rate zones use `dailyRollUp`; these have no source-record list endpoint. |

GPS, nutrition, ECG, IRN, profile, and write scopes are excluded. Public API availability varies by device/account. Proprietary app scores, such as readiness/recovery scores absent from the public catalog, are not fabricated. A permitted endpoint returning no data means **empty**, while missing permission or a failed endpoint means **unavailable/failed**.

The collector requests `users/me/dataSourceFamilies/google-sources`, including Google/Fitbit wearables and first-party manual/Health Connect entries. Each stored row retains the requested family and the exact returned JSON element. Actual `dataSource` metadata is also stored when present; reconciled responses may omit it. Google resolves overlapping uploads and device conflicts through [`dataPoints:reconcile`](https://developers.google.com/health/reference/rest/v4/users.dataTypes.dataPoints/reconcile). The stored JSON is the original response from that endpoint. Beta skin-temperature data uses its documented `list` method.

## Build and local checks

Requires Go 1.26 or newer. SQLite uses the pure-Go `modernc.org/sqlite` driver; the application has no Python or C compiler runtime dependency.

```sh
make build
go test ./...
go test -race ./...
go vet ./...
```

Tests use local synthetic OAuth/API servers and temporary SQLite files. Environments that forbid opening loopback sockets must allow that capability to run the HTTP tests.

The command form is `bin/google-health COMMAND [flags]`. Common flags are `--db`, `--client`, `--tokens`, and `--timeout`. Equivalent path environment variables are `GOOGLE_HEALTH_DB`, `GOOGLE_HEALTH_CLIENT_FILE`, and `GOOGLE_HEALTH_TOKEN_DIR`.

```sh
bin/google-health status
```

Offline status works before credentials exist. Existing databases are opened read-only, so it remains available during long imports. It describes the saved state; only `status --check` checks Google access live. Exit codes are 0 for success or a deliberate backfill page-budget pause, 1 for a runtime/authorization/partial-coverage failure, and 2 for invalid command syntax. Error output contains controlled codes and, when available, numeric `http_status` / `sqlite_code` diagnostics. It never includes provider error bodies, identities, tokens, authorization codes, SQL text, or health values. Interactive `authorize` emits its temporary Google consent URL, which contains public client ID and one-time state/challenge values.

## Google setup and owner authorization

Create a dedicated Google Cloud project. Project creation alone does not enable the API or authorize an account. Follow [Google's setup instructions](https://developers.google.com/health/setup):

1. Enable Google Health API in that project and complete any required terms/eligibility steps.
2. Configure the OAuth consent screen and add the owner as a test user when applicable. Configure the three scopes above.
3. Create and download an OAuth client. A Desktop client supports the [PKCE loopback flow](https://developers.google.com/identity/protocols/oauth2/native-app). A Web client is also accepted by this program; register the exact redirect `http://127.0.0.1:8765/callback` for it. The client JSON must contain the normal `installed` or `web` object.
4. Sign into Google Health/Fitbit using the Google Account linked to the wearable. Sync the wearable through the mobile app.
5. Store the downloaded credentials privately:

```sh
umask 077
mkdir -p secrets/tokens data
chmod 700 secrets secrets/tokens data
# Move the downloaded OAuth JSON to secrets/client.json, then:
chmod 600 secrets/client.json
bin/google-health authorize --publishing-status testing
```

`authorize` requests offline access and uses state plus S256 PKCE. After the callback it calls [`users.getIdentity`](https://developers.google.com/health/reference/rest/v4/users/getIdentity) and reads one real schema-valid nonempty health sample. Both must succeed before `identity_and_sample_verified` becomes true. The database pins the Health user ID privately; another account cannot replace its data. Partial consent still permits collecting the granted streams and is reported explicitly.

If the account has no sample yet, tokens remain pending and the command returns `no_health_sample`. After syncing a device, recheck without another consent flow:

```sh
bin/google-health authorize --verify-only
```

`account_not_linked` means the authorized account needs a Google Health profile or legacy Fitbit migration; see [Google's account-linking guidance](https://developers.google.com/health/troubleshooting). `api_not_enabled`, `project_access_denied`, `missing_scope`, and `reauthorization_required` describe separate setup/access failures. Reauthorize when consent is revoked; repeat the appropriate setup step for the other errors.

An external OAuth app in **Testing** receives a refresh token that [expires after seven days](https://developers.google.com/identity/protocols/oauth2#expiration) with these health scopes. Testing is the safe default configuration label, and status reports that unattended operation is not ready. After the owner verifies the actual Google Cloud publishing status is **In production**, obtain new tokens with:

```sh
bin/google-health authorize --publishing-status production
```

`--publishing-status` is the owner's configuration assertion, saved with newly authorized tokens. The program does not claim to verify Cloud Console publishing status through an API. `--verify-only` retains the original token's publishing label. Revocation, time-limited access, and other Google token-expiry policies still apply after publication.

## Import history and keep it current

The owner selected **all available Google Health data from June 14, 2026 inclusive**. This applies to every supported source and derived metric in `google-sources`; it adds no Fitbit-only source filter. Save that selection once, then run the daily import:

```sh
bin/google-health backfill --history-since 2026-06-14 --max-pages 500
bin/google-health sync
bin/google-health status --check
```

Repeat `backfill --max-pages 500` to continue. A page budget pauses cleanly with `status: "paused"`; partial pages are staged and their next-page cursor is persisted. Omit `--max-pages` for an uninterrupted load. Long requests retry at most five times with bounded exponential backoff/jitter and capped `Retry-After`. OAuth exchanges are not retried automatically because a refresh token may have rotated. Invalid/expired page cursors restart only their incomplete window; completed windows remain available.

`--history-since` persists an inclusive lower bound in SQLite for default source history, aggregate history, and daily catchup/refresh. Source and aggregate settings are validated and committed together before the first import page, so even an immediate page-budget pause retains the selection. Future `backfill` and scheduled commands need no date flags. Changing the selection does not delete already collected older records or their original JSON. Future SQL access remains unrestricted by this collection floor.

Default backfill pins separate exclusive end dates for UTC and civil metrics. Its 30-day source windows retain the existing end-based alignment and their saved cursors; only a window crossing the selected floor gets clipped. A selected floor earlier than the initial year adds older bounded windows. Empty periods never terminate collection within the selected range. Crossing midnight cannot silently change a saved pagination request. Civil plans include the current date as far east as UTC+14. Explicit `--until` dates stay exact. Available history depends on what the account/device actually recorded.

The two aggregation-only metrics use the same explicit June 14 floor, with requests of at most 14 days. This is the owner's chosen collection range, not a claim that no earlier data exists. Status reports `history_scope: "selected_range"`, `history_since`, and the separate UTC/civil exclusive ends. `source_history_complete` and `aggregate_history_complete` describe completion within those bounds.

For a datastore without a saved `--history-since`, the legacy default still fetches the preceding year and an unbounded older source tail, following every page. Its aggregate floor is inferred from the earliest source record until an owner supplies:

```sh
bin/google-health backfill --aggregate-since YYYY-MM-DD --max-pages 500
```

That legacy aggregate-only choice does not set a source floor. With a saved `--history-since`, `--aggregate-since` must match it. The three approved scopes do not permit reading profile membership date, so no profile permission is requested to guess an account boundary. Earlier live acceptance found HTTP 500 / `INTERNAL` on the `time-in-heart-rate-zone` unbounded tail before `2025-10-21`. Its failure row remains for audit; it is outside the owner-selected June 14 range and no longer blocks that range's completion.

Daily `sync` refreshes seven UTC dates for timestamp metrics, including the current partial date. Sleep uses its end timestamp. Daily metrics, exercise, and rollups instead use recorded civil dates/times. Their locations can differ after travel, and the approved scopes do not expose an account timezone. The importer keeps dates from UTC−12's current date onward unsettled, and queries through the current date in UTC+14. It refreshes six dates before that earliest open date plus all potentially current civil dates: eight or nine dates in total, split into requests of at most seven dates. This follows the API's [type-specific half-open filters](https://developers.google.com/health/filters). Neither the host timezone nor the New York timer's DST changes determine record boundaries.

Each stream has a separate daily watermark, `sync_until`: dates before it have been collected after their day ended under that metric's clock policy. Potentially open dates remain eligible. For example, at October 21 01:00 UTC, civil streams retain October 20 as their frontier, covering New York's still-open date; a restart nine days later still fetches its later uploads. UTC streams retain October 21 instead. After downtime, `sync` starts at the earlier of its frontier and its clock's refresh boundary. It commits forward contiguous windows and never advances across a failed one. Historical/manual repairs cannot advance the daily frontier. The initial daily origin is retained even if the first attempt fails. A persistent systemd timer catches up one missed invocation; the collector itself fetches all missed dates.

Before building the current plan, `sync` resumes unfinished daily work within the selected range with its saved partial date. Wholly in-range transfers keep their bounds and cursors. An older transfer crossing the floor starts a new clipped snapshot; its audit row remains. Transfers wholly before the floor are retained without resuming them. Staged pages and cursors otherwise resume unless a newer overlapping publication requires a fresh snapshot. Finishing an old paginated response cannot settle its original partial date. The current plan then refreshes that date and catches up to today, with every new source and rollup request clamped to the saved floor. Daily and historical/manual transfers have separate IDs even for equal ranges.

Corrections, source deletions, reconciliation changes, and late uploads within seven dates replace the previous completed view. The last partial date also remains eligible throughout an outage, however long it lasts. For other changes older than the automatic refresh horizon, explicitly repair the affected range:

```sh
bin/google-health backfill --since 2026-09-01 --until 2026-10-01 --force
```

`--until` is exclusive. `--since` is an explicit one-time repair range and leaves the saved history scope unchanged; it cannot be combined with `--history-since`. A completed default history plan is skipped on later scheduled runs. Use `--force` only for an intentional historical refresh; with a small page budget, restarting a forced run can redo already-refreshed completed windows.

## SQLite integrity and status

`records` contains exact response elements in `original_json`, indexed type/time/recorded date/source fields, `source_family`, and `has_value`. An empty daily rollup bucket is retained with `has_value=0`; no zero measurement is invented. `windows` stores attempted coverage, state, last successful completion time, observed-record count, `purpose` (`sync` or `backfill`), and the original partial `daily_date` for daily transfers. `runs` records outcomes. `staged_records` and `page_tokens` hold incomplete transfers under their purpose-specific window ID. `stream_state` holds daily frontiers. `meta` includes the private pinned identity and history-plan metadata.

All pages of a window are staged. The final-page transaction deletes the previous records for that exact metric/range, installs the complete staged result, and marks coverage complete together. It also clears staging, cursor, and token history for other incomplete overlapping windows of the same stream. Their original bounds, daily partial dates, and failure evidence remain; the next attempt refetches their entire snapshot. Unrelated cursors and completed coverage remain intact. For daily transfers, that same transaction advances the frontier only as far as the original partial date. A failed page or final transaction leaves the previous usable records, frontier, and other pending transfers untouched. Repeated completed-window replacements are idempotent. Empty completed coverage has zero observed records; unavailable/failed coverage has a controlled reason and does not delete earlier successful data.

An adjacent OS lock serializes writers for the entire import, including migrations and authorization. A second lock in the token directory protects rotation even when two commands select different databases. A crashed process releases its locks automatically. SQLite runs in WAL mode with full synchronous durability. Existing unknown/newer databases are rejected. Before changing a recognized schema, the migration path takes a restricted consistent SQLite backup using `VACUUM INTO`, including committed WAL data. V1 now creates schema version 4. The version-1 migration identifies each window's latest attempt from its recorded run, remaps daily staging and page-token references, retains published records and historical failures, and reopens legacy daily coverage. The version-2 correction then conservatively rewinds only civil frontiers and their saved partial dates by one date. The version-3 correction resets pending transfers that overlap another window with a recorded publication, including one whose next refresh is unfinished: older schemas cannot prove which snapshot came first. Nonoverlapping staging and cursors are preserved. This last correction leaves published records, frontiers, window IDs/bounds, daily partial dates, failure evidence, and pinned history ends unchanged. Read-only status requires a current schema; a locked writer command performs the migration. A failed migration rolls back its schema and transfer changes; the pre-migration backup remains available. Future changes must retain that backup gate.

`unattended_ready` requires verified identity/sample, all three scopes, usable refresh credentials, an owner-asserted production publishing status, completed default source and aggregate plans within the reported history scope, an explicit aggregate floor, recent contiguous daily coverage, no unfinished daily work within that scope, and a successful daily run within 48 hours. Stream failure/pending counts retain historical/manual audit evidence, including attempts outside the selected range; `unfinished_daily_windows` counts work still required for selected daily recovery. A failed optional manual repair does not invalidate otherwise completed default plans and daily coverage. Capability errors in required windows keep coverage partial. Offline status is saved-state evidence, not proof that Google has not since revoked access.

## Dedicated server deployment

Deploy only this source tree to `/home/admin/infrastructure/google-health`. The database mount is `/home/admin/data/google-health`; secrets are in the project's restricted `secrets` directory. The deployment does not alter other Compose projects.

The owner chose plaintext personal storage and stopped encryption work. No encrypted volume was provisioned. Google's [Secure Data Handling policy](https://developers.google.com/health/policies/health-api-developer-user-data-policy#secure-data-handling) requires health data and OAuth credentials encrypted at rest; this deployment does not claim that requirement is met.

As `admin` on the Linux host:

```sh
umask 077
mkdir -p /home/admin/infrastructure/google-health/secrets/tokens
mkdir -p /home/admin/data/google-health
chmod 700 /home/admin/infrastructure/google-health/secrets
chmod 700 /home/admin/infrastructure/google-health/secrets/tokens
chmod 700 /home/admin/data/google-health
cd /home/admin/infrastructure/google-health
# Install the real downloaded client JSON as secrets/client.json, owned by admin.
chmod 600 secrets/client.json
docker compose --project-name google-health build importer
```

The service defaults to UID/GID 1000. If `id -u admin` / `id -g admin` differ, set `GOOGLE_HEALTH_UID` and `GOOGLE_HEALTH_GID` in this project's `.env`. The deployed admin IDs are `1000:110`. Secrets and data must be owned by that UID/GID. Compose refuses to create missing mount sources. The root filesystem is read-only; only `/data`, `/tokens`, and a 16 MiB `/tmp` filesystem are writable. The existing 768 MiB container memory limit remains. No client secret or token belongs in the image or environment.

Compose sets `SQLITE_TMPDIR=/data/scratch`. Its entrypoint creates that directory with mode `0700` and verifies an actual private temporary-file write before starting Go. A symlink or unusable directory fails with `sqlite_scratch_unavailable` before database locks/opening. SQLite can spill large final-page transactions onto the persistent data filesystem instead of exhausting `/tmp`; these spill files share the owner's plaintext storage choice. Direct local Go commands remain usable and can optionally set `SQLITE_TMPDIR` to a suitable private directory.

On a host that permits SSH forwarding, headless authorization can instead use the loopback tunnel below. Keep it open from the owner's computer (replace `HOST`):

```sh
ssh -N -L 8765:127.0.0.1:8765 admin@HOST
```

In another remote shell:

```sh
cd /home/admin/infrastructure/google-health
docker compose --project-name google-health run --rm --no-deps importer authorize --no-browser --port 8765 --publishing-status testing
```

Open the emitted URL in the owner's local system browser. The redirect reaches the remote loopback listener through SSH. Production authorization uses the corresponding owner-verified publishing flag described above. Linux host networking is used for this temporary loopback callback; normal imports open no listener.

After real authorization, run:

```sh
docker compose --project-name google-health run --rm --no-deps importer status --check --require-authorized
docker compose --project-name google-health run --rm --no-deps importer backfill --history-since 2026-06-14 --max-pages 500
docker compose --project-name google-health run --rm --no-deps importer sync
bash deploy/install-timer.sh
```

The installer validates Compose, builds this image, checks a real token file plus live verified authorization/all scopes, and installs only `google-health.service` / `google-health.timer`. The timer runs at `12:00:00 America/New_York` with `Persistent=true`, regardless of the host's local timezone. `run-daily.sh` attempts both sync and a 500-page historical continuation; it returns failure if either failed. A planned page-budget pause is successful resumable work.

To stage units **without enabling collection**, install the two unit files with `sudo install -m 0644 ... /etc/systemd/system/` and run `sudo systemctl daemon-reload`. Leave the timer disabled until owner authorization. An image-only offline smoke test needs just the data directory:

```sh
docker run --rm --user "$(id -u):$(id -g)" --read-only \
  --mount type=bind,source=/home/admin/data/google-health,target=/data \
  -e GOOGLE_HEALTH_DB=/data/health.sqlite3 google-health:local status
```

## Public project information website

The owner authorized a public information site at `https://health.vvivid.dev/` to complete the OAuth app's Branding configuration. The static pages live in `site/`: overview at `/`, privacy at `/privacy/`, and use/contact information at `/terms/`. They use the Google consent app name **Personal Health Collector**. The original pages are deployed, HTTPS access and private-path 404s were verified, and Branding URLs were saved. The relative-redirect correction described below remains pending review and redeployment; Google app verification is a separate status.

`deploy/site.compose.yaml` is a separate Compose project named `google-health-info`. It serves only `site/` through a pinned unprivileged NGINX image, with a read-only `deploy/site.conf` mount. The config uses relative directory redirects so `/privacy` and `/terms` retain the public HTTPS origin behind Traefik. It has no database, token, or secret mounts, no host port, and no application runtime. It connects to the existing external `proxy` network. Traefik routes only `health.vvivid.dev` through `immich-public-websecure`, with TLS and the existing `letsencrypt` resolver. The collector's `compose.yaml` and Go application are independent.

After review and copying these source files to the dedicated deployment directory, validate and start the website separately:

```sh
cd /home/admin/infrastructure/google-health
docker compose --file deploy/site.compose.yaml --project-name google-health-info config
docker compose --file deploy/site.compose.yaml --project-name google-health-info up -d
```

The bind sources `../site` and `./site.conf` resolve against the Compose file's `deploy/` directory. Use these commands without a `--project-directory` override; changing that base changes the mount paths. Public directories/files must be readable by NGINX (`0755` directories, `0644` files). The site requires the existing `proxy` network and public DNS for `health.vvivid.dev` to the Traefik origin. Verify all three pages, slashless redirects, and a missing path over HTTPS after deployment. The original pages have been deployed and their Branding URLs saved; the redirect correction still requires review and redeployment. Adding URLs alone does not prove Google app verification.

The public pages contain no sign-in, forms, scripts, analytics, third-party fonts, or health-data endpoint. They disclose indefinite archive retention, deletion separate from Google revocation, future explicitly authorized agent queries, and possible infrastructure request logs. Support refers to Ou David and the developer support email already shown on the Google consent screen; no private email is embedded in the site.

## Operations and recovery

```sh
systemctl list-timers google-health.timer --no-pager
systemctl status google-health.service --no-pager
journalctl -u google-health.service --since today --no-pager
docker compose --project-name google-health run --rm --no-deps importer status --check
```

An installed/enabled timer does not prove a successful scheduled collection. Capture the actual timer-triggered service result, its `runs` outcome, coverage dates, and persisted record counts before completing checkpoint 4. A manual `systemctl start google-health.service` validates manual execution separately.

Every `docker compose run --rm` uses a fresh container. Run `status --check --require-authorized`, then `sync`, then the status command again to confirm the datastore, counts, and identity survive recreation. To exercise refresh persistence, run again after the access token naturally expires; the application atomically replaces `tokens.json` within the **mounted directory**. A bind mount of the token **file itself** is unsupported because atomic rename must replace its inode. Never bake credentials into a replacement image.

For a backup, use a trusted host SQLite shell's online `.backup` command against `health.sqlite3`, or stop `google-health.timer` and the service before copying the database **together with any WAL/SHM files**. Keep backups private. Restore with the timer/service stopped, preserve the pinned account and token directory, restore ownership/mode 600, then run `status --check` before resuming the timer. Keep a database backup and the prior image before a future schema upgrade; use the matching schema-compatible image when rolling back.

On network/rate-limit failures, rerun the same command; incomplete windows resume their saved cursors unless an overlapping publication requires a fresh snapshot. Completed data remains intact until replacement commits. On `collector_busy`, inspect the active service/process; do not delete a lock file to bypass it. On `reauthorization_required`, repeat the owner authorization flow. Device uploads older than seven dates require the bounded repair command above. Disk usage grows with high-resolution historical records; operational status intentionally reports counts rather than dumping health data into journals.

Live API acceptance, actual account capabilities, Google publishing eligibility, Docker deployment validation, and a real scheduled successful run must be recorded separately from the local mock test results.
