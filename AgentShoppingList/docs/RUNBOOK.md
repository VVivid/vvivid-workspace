# Local release and persistence runbook

This is release preparation for one application instance and one local SQLite volume. No live Keycloak, Traefik, DNS, enrollment, remote repository, registry publication or deployment change has been made. CP03/06 actual identity/client acceptance and CP10 phone/proxy/deployment acceptance remain pending. Operational readiness is a live database/configuration check; it does not certify those external acceptance criteria.

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

`make check` runs the ordinary unit/integration suites. Whole-process acceptance is explicitly opted into with `make process-test`: it builds and executes both production commands using synthetic test-owned OIDC and loopback listeners. No production identity bypass is compiled into `shopping`. The test executes PKCE browser login, two owners, official SDK tools, Polar/check/no inferred spending, confirmation/history/insights, simultaneous revision conflicts, graceful restart, post-commit lost response plus SIGKILL/concurrent replay, a SQLite-writer-blocked request interrupted before commit/retry, durable encrypted sessions/preferences. Existing manual backup/restore compatibility is tested only against these disposable synthetic fixtures; it does not authorize production backups. The test waits on health, response and request-written barriers.

The Dockerfile pins multi-platform registry digests for Go, Node build tooling and a non-root distroless static Debian13 runtime. Tailwind4.3.0 is locked and compiled during the image build. The final image contains only production commands, CA certificates, timezone data and runtime support files; templates, JS, CSS and migrations are embedded. The Go timezone database is also embedded. Build each architecture deliberately; read-only discovery found the target server is amd64.

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

`make container-test` uses the local `SHOPPING_TEST_IMAGE` (default `agentshoppinglist:local`), cross-compiles a separate test executable and mounts it into the production image. The production app runs as65532 with a read-only root filesystem, a private test volume and `--network none`; fixture traffic is loopback only. For its disposable synthetic fixture only, it then restores into a fresh database and performs real container stop/start with identical counts/totals. The script removes its own containers/volume on exit and leaves only reports/test executables in ignored `.cache/acceptance`. It does not delete unrelated Docker resources. An arm64 run is arm64 runtime evidence; cross-compiling amd64 alone is build evidence.

The prior reviewed commit05ff303 passed Go/npm and offline pinned-Trivy image scans. Those reports describe that commit, not the no-backup correction. After its required local workflow, rebuild, test and scan the final replacement images. Docker Scout was rejected because it may transmit private package metadata and remains unexecuted; the recorded offline method scans with no network, Docker socket or credentials. See [the historical validation note](validation/CP09-CP10-2026-10-19.md).

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

## Server-only persistence policy

The user explicitly selected on October 19: keep everything on the server and do not back up anything. Keep the shopping SQLite volume and runtime secrets on that server. Do not create or schedule production backups, snapshots, off-server copies, retention jobs, restore drills or Mac recovery keys. This supersedes the earlier recovery objectives and off-server acceptance requirements.

Startup and enrollment do not invoke backup code. Existing operator-only backup/verify/restore capabilities remain available for a future explicit request; they are outside the current operational procedure and must not be invoked on production. The HTTP/MCP interfaces cannot invoke them. Their synthetic unit/process/container tests remain local compatibility evidence, not a production backup plan. No production data has been copied by this work.

Keep the persistent session keyring stable on the server across application restarts, separate from the database and image. Rotated decrypt-only keys remain on the server for the session overlap period in [AUTH.md](AUTH.md). Do not regenerate the keyring on ordinary startup or copy it to a recovery device.

## Migration and rollback discipline

Fresh empty schemas initialize normally. Startup and enrollment validate an existing application's consecutive migration versions, names and checksums, then apply supported pending migrations without creating an archive or backup scratch directory. An existing nonempty schema without application migration history is rejected. Drifted, gapped and newer histories still fail closed before serving requests.

Each SQL migration commits in its own short transaction. Failure rolls back that migration's SQL, data and history row; previously committed migrations remain applied. Startup reports the migration error and stops before listening. Preserve the existing server volume and inspect its applied history before retrying. Ordinary shutdown/restart and exact mutation retries must preserve records, sessions, audit and original idempotency outcomes.

There are no down migrations or promised recovery point under the selected policy. An older binary must reject a newer schema. Switch application versions only when their supported schema is compatible; an incompatible upgrade needs a compatible forward correction against the existing server database. Do not overwrite or reset the data volume, delete WAL/SHM sidecars, or assume that switching binaries reverses a committed migration. Release notes must identify the actual binary/image and schema support.

For the separately prepared OAuth compatibility patch, follow its reviewed [apply/rollback plan](../deploy/oauth-compat/README.md). It uses a rebuilt authorized baseline binary and removes only the added shopping_origin property; it creates no copy of live config, binary or database. An independent administration channel and an approved rollback watchdog remain prerequisites before any gateway restart.

## Remaining release acceptance

The no-backup correction is in its local Builder → Translation → final validation → fresh independent Review workflow. Prior05ff303 beta images and their test/scan reports are historical; replacement final-commit images must be rebuilt, tested and scanned after approval. Live issuer/client/proxy/SSH isolation/phone checks, server secret/mount validation and deployment approval remain pending. Keep CP03/06/10 in progress. No live rollout, backup or infrastructure write is authorized by these local instructions.
