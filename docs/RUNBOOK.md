# Local release and recovery runbook

This is release preparation for one application instance and one local SQLite volume. No live Keycloak, Traefik, DNS, enrollment, remote repository, registry publication or deployment change has been made. CP03/06 actual identity/client acceptance and CP10 off-server/phone/proxy acceptance remain pending. Operational readiness is a live database/configuration check; it does not certify those external acceptance criteria.

## Build and identify the release

Use Go1.26.9 and the committed npm lockfile. The Go patch replaces1.26.5 because the current Go vulnerability scan found reachable standard-library advisories; the patched source scan returned zero findings. See [Go release history](https://go.dev/doc/devel/release) and [DEPENDENCIES.md](DEPENDENCIES.md). Recheck advisories at each release.

```sh
export GOTOOLCHAIN=go1.26.9
export GOMODCACHE="$PWD/.cache/go-mod"
export GOCACHE="$PWD/.cache/go-build"
npm ci --ignore-scripts
make fmt assets check build
node --check web/static/app.js
make process-test
go test -race ./internal/store/sqlite ./internal/shopping ./internal/web ./internal/identity ./cmd/shopping-admin
```

`make check` runs the ordinary unit/integration suites. Whole-process acceptance is explicitly opted into with `make process-test`: it builds and executes both production commands using synthetic test-owned OIDC and loopback listeners. No production identity bypass is compiled into `shopping`. The test executes PKCE browser login, two owners, official SDK tools, Polar/check/no inferred spending, confirmation/history/insights, simultaneous revision conflicts, graceful restart, post-commit lost response plus SIGKILL/concurrent replay, a SQLite-writer-blocked request interrupted before commit/retry, durable encrypted sessions/preferences and active-process backup/restore. It waits on health, response and request-written barriers.

The Dockerfile pins multi-platform registry digests for Go, Node build tooling and a non-root distroless static Debian13 runtime. Tailwind4.3.0 is locked and compiled during the image build. The final image contains only production commands, CA certificates, timezone data and runtime support files; templates, JS, CSS and migrations are embedded. The Go timezone database is also embedded. Build each architecture deliberately; the server architecture still needs verification.

```sh
docker build --platform linux/arm64 \
  --build-arg VERSION=0.1.0-local \
  --build-arg COMMIT=REPLACE_WITH_REVIEWED_COMMIT \
  --build-arg BUILT_AT=REPLACE_WITH_UTC_BUILD_TIME \
  -t agentshoppinglist:local .
docker run --rm --network none agentshoppinglist:local version
make container-test
```

Use `--platform linux/amd64` for that target. Both architectures compile from the same source and pinned build inputs, using `-trimpath` and disabled build-path VCS discovery. Fixed version/commit/time values make binary identification repeatable; an uncommitted build must identify itself as dirty. `make build VERSION=... COMMIT=... BUILT_AT=...` sets the same metadata in local binaries. Never use a fabricated clean commit to label modified code. No image is published by these commands.

`make container-test` uses the local `SHOPPING_TEST_IMAGE` (default `agentshoppinglist:local`), cross-compiles a separate test executable and mounts it into the production image. The production app runs as65532 with a read-only root filesystem, a private test volume and `--network none`; fixture traffic is loopback only. It then restores into a fresh database and performs real container stop/start with identical counts/totals. The script removes its own containers/volume on exit and leaves only reports/test executables in ignored `.cache/acceptance`. It does not delete unrelated Docker resources. An arm64 run is arm64 runtime evidence; cross-compiling amd64 alone is build evidence.

Current dependency checks cover Go modules/standard library with govulncheck and npm lockfile audit. A final image/base-package advisory scan remains pending: automatic approval review rejected Docker Scout because it may send private image/package metadata to Docker's service without specific destination/payload authorization. Do not treat a pinned digest or a clean Go/npm scan as a completed image scan.

## Health, startup and stopping

- `/health/live` returns200 while the HTTP process serves requests. It makes no database or Keycloak request.
- `/health/ready` returns200 only after identity configuration/discovery succeeded, the database is available and its applied history exactly matches all embedded migrations. A completely unconfigured identity shell returns503. A transient identity-provider outage after startup does not make the HTTP process unhealthy; new authentication fails safely when required discovery/keys/tokens are unavailable.
- `shopping healthcheck live|ready` probes only the configured port on127.0.0.1 with a2s timeout. It needs no secret or database handle. Docker and Compose use the readiness command.
- Partial identity configuration, invalid/private-file failures, newer/drifted schema or failed migration fail startup before listening. An unavailable issuer during initial discovery prevents startup; the supervisor may retry.
- SIGTERM/SIGINT drains active HTTP requests within `SHOPPING_SHUTDOWN_TIMEOUT`, then closes SQLite. Compose grants20s for the configured15s drain. An abrupt interruption preserves committed SQLite state and rolls back uncommitted work. Retry the same request/key/payload to learn its authoritative persisted outcome; do not substitute a fresh idempotency key.

## Prepared Compose/Traefik configuration

`deploy/compose.yaml` publishes no host port and creates no proxy or identity service. It references an explicitly named existing Traefik network, HTTPS entrypoint and certificate resolver. It defines one persistent named shopping volume. All infrastructure names, exact issuer, client IDs, image reference and private host-file paths are required inputs; none is guessed from historical documentation.

Copy `deploy/environment.example` to an operator-owned private file outside source control and resolve every blank from the actual deployment. `SHOPPING_IMAGE` must be a reviewed, scanned registry `image@sha256:...` reference. `SHOPPING_HOST` must match `SHOPPING_ORIGIN`; API/MCP audiences must be that exact origin plus `/api/v1` and `/mcp`. Confirm existing router/service names do not collide before applying. Shopping OAuth grants must remain separate from SSH privileges; follow [AUTH.md](AUTH.md) and [KEYCLOAK.md](KEYCLOAK.md).

File-backed Compose secrets preserve host ownership/permissions: Compose `uid`, `gid` and `mode` overrides do not fix bind-mounted file permissions. On the actual Linux host, provision both secret files as private regular files owned/readable by65532, mode0400 or0600, inside protected directories. The web client secret and persistent session keyring are separate read-only mounts; never put their values in environment files, image layers, command arguments or logs. Check actual mounted permissions and startup under65532 before enabling the route. Existing named data volumes must be owned by65532 with a0700 data directory and0600 database. New volumes inherit the image's private `/data` ownership.

```sh
# Local configuration rendering only; this does not create networks/services.
docker compose --env-file /ABSOLUTE/PRIVATE/shopping.env \
  -f deploy/compose.yaml config --quiet
```

The prepared service drops capabilities, denies privilege escalation, uses a read-only root filesystem and writes only to `/data`. Secret mounts are read-only. Do not scale it past one replica or use a network filesystem for SQLite. No separate MCP gateway is needed. Preserve canonical Host and MCP authorization/streaming headers through Traefik. Exact TLS/proxy/OAuth callbacks, grants, revocation, clients and phone testing require the separately authorized live acceptance. A healthy local container is not that acceptance.

## Consistent backup, verification and isolated restoration

The operator commands use SQLite's maintained [online backup API](https://sqlite.org/backup.html), reading committed WAL pages through a read-only source connection. No raw live main-file copy is used. Each tar contains only `manifest.json` and `database.sqlite`, with application/Go/SQLite/schema versions, SHA256, exact byte size, every table count and current confirmed per-currency merchandise/tax totals and missing counts. Count checks include accounts/preferences, lists/items, encrypted sessions, source/reconciliation evidence, audit and idempotency records. Values accumulate as arbitrary-precision integers. Void/superseded/draft records remain preserved but do not contribute to confirmed totals.

Backup and restore inspect the existing database without invoking migrations or creating a missing source. Verification checks the full migration prefix/checksums, SQLite integrity, foreign keys, SHA/size and manifest counts/totals. Known older schema versions can be inspected/restored unchanged; unknown/newer/drifted schema fails. Archive paths/traversals, duplicate/unexpected entries, corrupt/truncated data and mismatched manifests fail. The current recovery tool accepts database entries up to64GiB. There are no owned assets today: product photos are external URLs, and the manifest's owned-assets list is empty. Add a consistent asset snapshot/manifest procedure before introducing owned files.

```sh
install -d -m 700 /PRIVATE/SHOPPING/backups /PRIVATE/SHOPPING/restore-drill
./bin/shopping-admin backup --db /PRIVATE/SHOPPING/shopping.sqlite \
  --archive /PRIVATE/SHOPPING/backups/NEW-UNIQUE.tar
./bin/shopping-admin verify --archive /PRIVATE/SHOPPING/backups/NEW-UNIQUE.tar
./bin/shopping-admin restore --archive /PRIVATE/SHOPPING/backups/NEW-UNIQUE.tar \
  --target /PRIVATE/SHOPPING/restore-drill/NEW-ISOLATED.sqlite
./bin/shopping-admin inspect --db /PRIVATE/SHOPPING/restore-drill/NEW-ISOLATED.sqlite
```

Commands default to a bounded2m operation (`--timeout` accepts up to1h). Source and destination parents must already exist. Verification needs private scratch space beside the archive; restore needs it beside the new target. Budget room for the snapshot, tar and isolated verification/restore copy, not just the main database. Failed/canceled work removes private staging files. Artifacts are0600; a fsynced hard link publishes the completed artifact atomically without overwriting any existing destination or symlink. Restore also rejects existing WAL/SHM/journal siblings. Use a genuinely isolated target directory with no other process opening it. Never restore into a live app database path, delete its sidecars or pass a restored old schema to the current app merely to verify it: startup may migrate it.

For a container, run the same `/shopping-admin` in the pinned image with no network and access only to the dedicated data/backup paths. The HTTP/MCP interface cannot invoke these commands. Capture inspect/manifest reports in restricted operator evidence; archives contain personal data and encrypted session/token material. Hashes detect accidental corruption; they are not an authenticity signature for an untrusted archive.

## Migration and rollback discipline

Fresh empty databases initialize normally. Before upgrading any existing known migration prefix, `shopping` and the enrollment command create and verify a private `shopping.sqlite.pre-migrate-vOLD-to-vNEW-RANDOM.tar` beside the database. Startup refuses the upgrade if that backup cannot complete. The backup retains the old schema, all recorded tables and confirmed totals. Startup logs its path; a subsequent migration error reports it too. Each SQL migration commits separately, so a later failure can leave earlier pending migrations applied. Preserve the recovery archive before fixing/retrying.

Before a release, explicitly create a backup with the old release binary, copy its encrypted version off server and perform an isolated restore drill. The automatic same-disk pre-migration artifact supplements that recovery point; it cannot protect against loss of the host/volume. Stop the app and preserve a recovery snapshot before changing data-volume ownership, replacing secrets or switching binaries.

There are no down migrations. An older binary must reject a newer schema. Rolling back an incompatible schema means stopping the app, preserving the failed/new database and its WAL/SHM together for investigation, restoring the verified pre-upgrade archive into a new isolated volume/path, inspecting it with its matching release and then explicitly switching the application to it. Do not attach an old binary to the new schema or overwrite the only copy. Confirm the release's schema support, keyring and issuer/client configuration before allowing traffic. Reconcile any purchases written after the recovery point: their absence cannot be inferred from chart totals alone.

## Encryption, retention and recovery objectives

Proposed initial operator policy: encrypted backup at least daily and before every migration; keep7 daily,4 weekly and6 monthly copies off server. Protect retention/deletion against compromise separately from app credentials. Target RPO24h and RTO1h; these are planning objectives pending a timed off-server restore drill and an authorized scheduled backup/monitor configuration. No automation/off-server transfer has been installed by this work. A local verified tar is plaintext; encrypt it with the operator's selected recovery public key before copying it to the chosen independent storage target. Verify the decrypt/restore route from that stored copy and record timestamps, byte hashes, versions, counts, totals and recovery duration before claiming the target met.

Database backups do **not** include the session encryption keyring, web client secret, OIDC registrations, proxy/DNS configuration or the off-server encryption private key. Maintain a separately encrypted recovery kit containing the session keyring (including required old decrypt-only keys), a valid web client secret, exact nonsecret identity/proxy configuration, pinned image reference and recovery instructions. Successful existing-session recovery requires the matching keyring; database contents alone cannot decrypt it. A fresh valid Keycloak client secret may replace a rotated one through the normal operator process. Test access to the encryption recovery key from a separate device/location; a lost key makes the encrypted backup unusable. Never store recovery keys alongside the sole encrypted backup or in this repository.

Remaining release acceptance: coordinator final checks and fresh independent Review of this local slice after Translation; final image/base-package advisory scan with an authorized no-surprise data flow; approved server architecture/network/secrets validation; live issuer/client/proxy/SSH isolation/phone checks; selected encrypted off-server storage, recovery kit and a timed restore drill. Keep CP10 in progress until these actually pass.

Local approval update: CP09/10 code completed Translation, final validation and independent Review Approved. Candidate image advisories were subsequently scanned with pinnedTrivy0.76.0 and a separately downloaded current publicDB inside network-disabled containers (no socket/credentials), reporting0 known OS/Go vulnerabilities on botharchitectures. Scout remains rejected/unexecuted. Use the offline method in the validationnote for finalcommitimages; live acceptance and offserverdrill remain open.
