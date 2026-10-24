# Operating WebhookControl

Run one daemon per SQLite database. Use an encrypted, access-controlled local
disk, an external TLS reverse proxy and a trusted OAuth issuer. Containers and
native binaries use the same configuration and administration commands. Neither
binary generates credentials, connects an account, or offers an auth bypass.

## Build and operator files

From the independent `WebhookControl/` build context:

```sh
make check test build
./bin/webhook-control -version
install -d -m 0700 data secrets
install -m 0600 /secure/supplied-database-key secrets/database.key
install -m 0600 /secure/supplied-digest-key secrets/digest.key
cp examples/config.json config.json
chmod 0600 config.json
```

Supply two different, cryptographically random **raw 32-byte** files. Hex,
base64, trailing newlines and group/world-readable key files are rejected. Keep
the encryption key and ingress digest key separate from source HMAC/bearer and
outbound header credentials. Keep recoverable copies in separate secure key
storage, outside the database and its backup location. Key loss makes ciphertext
unrecoverable. Database startup and restore reject a wrong encryption key.
The daemon also pins an ingress-digest key check in SQLite: changing that key
would break retained webhook idempotency, so restart rejects it. There is no key
rotation/migration command in this MVP. Do not replace keys in place.

Edit `config.json` before use. For native execution replace the three `/data` and
`/run/secrets` paths with absolute host paths. For Compose keep the container
paths and supply the mounts described below. Database directories must exist and
be writable by the daemon user; key and configuration files must be readable.
The CLI uses the same database encryption key as the daemon; its management
receipt digests use an independent domain-separated derivation from that key.
The supplied ingress digest key belongs only to webhook receipt hashing.

Configuration is strict JSON, at most 256 KiB, with unknown fields rejected.
Paths must be absolute and clean, and the three paths must differ. Limits must
be in range. A missing `listen` selects `127.0.0.1:8080`; any nonloopback bind
requires `allow_non_loopback: true`. Only literal IP listener addresses are
accepted. Source rules/configuration and grants cannot change these operator
settings. Restart the daemon after changing operator configuration.

## TLS, OAuth and destination approval

Set `oauth.issuer` to your external issuer and `oauth.resource` to the public
management URL ending exactly `/mcp`, such as `https://webhooks.example/mcp`.
REST and MCP use that same resource as audience. The issuer must publish OIDC
discovery/JWKS and issue RS256 access tokens with protected header `typ: at+jwt`,
matching `iss`/`aud`, nonempty `sub`, future `exp`, and space-separated scope
`webhook-control.manage`. Optional `token_use` must equal `access`. ID tokens are
rejected. The daemon performs issuer discovery before opening its listener;
unavailable or invalid issuer configuration fails startup. No live issuer or
account setup is performed by the examples or tests.

HTTPS is required for issuer, JWKS, redirects, resource and management origins.
`allow_insecure_loopback: true` permits HTTP loopback URLs only for synthetic
local tests. Leave it false in production. Explicit management origins must be
exact origins without paths; browser Origin headers must match one of them or
the resource origin. Public protected-resource metadata derives only from
configured values, never request Host or forwarded headers.

Terminate production TLS at your existing reverse proxy. Forward `/v1/hooks/`,
`/v1/manage/`, `/mcp`, and `/.well-known/oauth-protected-resource` (also its `/mcp`
variant) to the loopback listener. Preserve Authorization and idempotency/HMAC
headers and exact raw request bytes. Set proxy body, header and timeout limits
consistent with the daemon; never log request bodies, OAuth tokens, source
credentials or receiver Authorization headers. Do not expose the backend port
directly. `/livez` and `/readyz` return only `{"status":"ok"}` or unavailable;
keep probes local when practical.

Configure an exact `destination_origins` allowlist and a matching local CLI
policy file. Public destinations require HTTPS. Approving a private receiver
requires `allow_private: true` for its exact origin; that also permits HTTP to
that origin. Server approval and the caller's source grant must both permit the
destination. DNS is checked on every actual attempt and dialed by validated IP.
Metadata/link-local/reserved/unspecified destinations remain blocked even with
private approval. Redirects are never followed; receiver response bodies are
not read or stored. Transporter egress deliberately uses no environment proxy.
The daemon uses system CA trust, which can be configured for an approved private
PKI; it never disables certificate or hostname verification.

## Provision before accepting traffic

All commands below run locally and use files supplied by the operator. Example
issuer, subject and receiver values must match your actual approved configuration.
Update `examples/grant.json`, `examples/policy.json` and `examples/source-put.json`
in private copies; preserve versioned IDs and create new resource revisions for
later edits. Keep credential bytes out of configuration snapshots and shell
arguments. The raw outbound header secret file contains the complete header value
(for example a provider-approved Authorization scheme and credential).

```sh
./bin/webhook-control-admin -db "$PWD/data/state.db" \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject local-admin \
  secret-put -name source-a-auth -version v1 -purpose source-auth \
  -value-file /secure/supplied-source-hmac

./bin/webhook-control-admin -db "$PWD/data/state.db" \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject local-admin \
  secret-put -name receiver-a-header -version v1 -purpose http-header \
  -value-file /secure/supplied-receiver-header

./bin/webhook-control-admin -db "$PWD/data/state.db" \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject local-admin \
  grant-put -issuer https://issuer.example -subject automation-agent \
  -file /secure/source-a-grant.json

./bin/webhook-control-admin -db "$PWD/data/state.db" \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject automation-agent \
  invoke -policy-file /secure/server-policy.json -operation config_put \
  -file /secure/source-put.json

./bin/webhook-control-admin -db "$PWD/data/state.db" \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject automation-agent \
  invoke -policy-file /secure/server-policy.json -operation config_activate \
  -file /secure/source-activate.json

./bin/webhook-control -config /secure/webhook-control.json
```

Provisioning commands may initialize SQLite before the daemon starts. A grant is
an explicit issuer/subject, source, action, destination, secret-name and retention
intersection. Use narrower actions for monitoring-only agents. Only the local CLI
can provision grants/secrets; remote tools cannot expand their own authority.
`source-auth` secrets can authenticate ingress only; `http-header` versions can
be exported as outbound headers only. References are immutable and purpose is
checked both during configuration validation and at actual runtime secret reads.

For MCP, configure your MCP client's Streamable HTTP server URL as the pinned
`https://webhooks.example/mcp` and request the advertised issuer, resource/audience
and `webhook-control.manage` scope through that client's OAuth support. Preprovision
the client's verified issuer/subject grant. Initialization, tool listing and each
call require a current credential/grant. `sources_list` discovers only granted
sources; `monitor` takes the same envelope as `examples/monitor.json`. REST calls
use `POST /v1/manage/monitor` with that JSON body and the same OAuth access token.
The admin CLI `invoke` uses the identical authorization/application service.

## Docker and Compose

The Dockerfile builds both binaries from this directory, copies public CA roots,
and runs as numeric UID/GID 1000. Compose publishes `127.0.0.1:8080`, deliberately
binds `0.0.0.0:8080` inside the container, mounts supplied keys read-only, and
requires a writable data bind mount. It has a read-only root filesystem, a small
private `/tmp`, no Linux capabilities, and no new privileges.

On a UID 1000 host, the earlier `install` commands create compatible ownership.
On other hosts provision data/key/config ownership and read permissions for
container UID 1000 before starting; do not make private key files world-readable.
Mount sources must already exist; Compose refuses to create missing host paths.
The `WEBHOOK_CONFIG_FILE`, `WEBHOOK_DATA_DIR`, `WEBHOOK_KEY_FILE` and
`WEBHOOK_DIGEST_KEY_FILE` variables select alternate host mounts.

```sh
docker build -t webhook-control:local .
docker compose config --quiet
docker compose up -d
docker compose exec webhook-control /usr/local/bin/webhook-control \
  -config /etc/webhook-control/config.json -check-ready
docker compose stop
```

When building behind a managed HTTPS proxy, preserve inherited proxy settings
and mount its public CA only during dependency download:

```sh
docker build --secret id=proxy_ca,src="$CODEX_PROXY_CERT" \
  -t webhook-control:local .
WEBHOOK_BUILD_CA_FILE="$CODEX_PROXY_CERT" docker compose build
```

Compose defaults its build CA secret to the host's public
`/etc/ssl/certs/ca-certificates.crt`; set `WEBHOOK_BUILD_CA_FILE` to a supplied
combined trust bundle where necessary. This secret is a build-only mount and
does not enter image layers. The second build step has networking disabled. An
issuer/private receiver using an additional runtime CA needs an approved public
trust bundle mounted read-only and the Go `SSL_CERT_FILE` setting; do not place
credentials in that bundle. The local image build and version smoke need no
OAuth, runtime keys, live endpoints or running Compose service.

## Bounds, monitoring and failures

The default daemon has four workers, a 250 ms fallback queue scan, 30 s leases,
10 s renewal, eight attempts, one-second initial backoff and a one-minute cap.
Retries use half-to-full jitter. Wakeups are hints; startup scans and polling
recover committed pending work and expired leases after a crash. A receiver must
deduplicate the stable delivery-ID `Idempotency-Key`: HTTP ambiguity permits
at-least-once attempts, and `2xx` records receiver acceptance only.

Default HTTP bounds are 128 connections, 64 in-flight requests, 16 KiB headers
(plus Go's bounded parser allowance), 5 s header reads, 15 s total reads, 30 s idle,
70 s request contexts and 75 s writes. Webhook bodies are at most 1 MiB; REST/MCP
bodies are at most 2 MiB. Config validates bounded overrides, worker settings,
request quota (default 60/minute/source) and the 30 s immediate-mode budget.
SIGTERM/SIGINT clears readiness, drains HTTP for up to 10 s by default, then
cancels and joins workers/cleanup before closing SQLite. If drain times out the
process exits with a safe error and unfinished leases recover on restart. Choose
an orchestrator stop grace period exceeding the configured shutdown timeout.

An unexpected queue error or retention-cleanup error clears the readiness gate,
cancels requests/sends, closes the listener and exits nonzero. A supervisor may
restart it after repairing storage. The daemon never continues accepting with a
dead worker pool. It emits fixed safe startup/failure messages only, without
event bodies, token values, header secrets, raw driver errors or receiver bodies.
Use your supervisor for exit/restart alerting.

`monitor` requires `deliveries.read` on the supplied source. REST and MCP return
only that source's retained event count, delivery state counts (including logical
expiry), oldest unexpired queued age, additional retry attempts, first durable
claim count/sum/max latency from acceptance, and ciphertext row bytes (including
expired rows awaiting cleanup). Metrics
do not include IDs from other sources, payloads, destinations or credentials.
Aggregates are over retained records, not lifetime counters. Schema-v3 historical
attempts without a first-attempt timestamp do not contribute to first-dispatch
timings. A retry attempt means an additional claim after the first, including
recovery of abandoned leases.

Optional `ingress` counters count accepted new events, duplicate receipt requests
and rejected known-source requests since daemon start. Immediate outcomes that
later fail still count as accepted events. Invalid/unknown sources and malformed
requests rejected before source lookup are excluded. This map is bounded to
1024 sources; counters for additional sources are omitted. No credential or
input value labels enter metrics. Global physical totals are privileged locally:

```sh
./bin/webhook-control-admin -db "$PWD/data/state.db" \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject local-admin storage-stats
```

Inspect failed deliveries via `deliveries_list`/`deliveries_get` and fixed codes
such as `http_permanent`, `policy_denied`, `secret_unavailable`,
`retries_exhausted` or `retry_window_exhausted`. Correct receiver/configuration
problems before requesting `deliveries_retry`. That creates new lineage from
the frozen render and preserves its original expiry; it cannot recover expired
or never-stored bytes. `events_replay` needs retained originals, an explicit
configuration revision, current active revision and a new request key. It is
queue-only: matching delivery TTL `0` returns `422 durable_replay_required` with
no partial acceptance. Incoming TTL `0` instead makes one bounded foreground
attempt; duplicate/unknown receipts never authorize another send.

## Retention, capacity, backup and restore

Original and delivery TTLs are independent: `0` never stores those payload bytes,
positive seconds expire from acceptance, and `-1` requires an explicit indefinite
grant. Defaults recommended in examples are one day of originals and seven days
of delivery payloads. A 60 s cleanup loop and startup cleanup remove expired
ciphertext; reads and claims enforce deadlines even before cleanup. Finite event/
delivery metadata and ingress deduplication default to seven days independently;
configure `metadata_retention_seconds` and `deduplication_retention_seconds` from
1 s through one year. Metadata needed by a retained payload/delivery survives
its nominal window. Configuration revisions, grants, secret versions, audit and
management request receipts remain retained in this MVP; monitor disk usage and
plan administrative archival outside the service. No hidden indefinite payload
retention is implied by those metadata records.

`max_database_pages` optionally limits logical SQLite growth (zero disables this
additional bound). It applies on every connection and rejects startup if the
database exceeds it. Each page is normally 4096 bytes; WAL, SHM, temporary files,
filesystem metadata, free-space reserve and backups require additional disk.
On capacity/commit failure, ingress returns `503`, with no false `202` or partial
fanout; retry the identical key/raw body once storage recovers. An ambiguous
commit can already have succeeded, so always preserve the key for that retry.
Reserve enough storage for the largest accepted originals, encrypted fanout,
retention windows and maintenance. Tests use `max_page_count` to trigger real
`SQLITE_FULL` in a small temporary DB without filling the host disk.

Use only the privileged local backup/restore commands for live database copies.
`backup` uses SQLite `VACUUM INTO` to produce an online, internally consistent
snapshot including committed WAL pages. It preserves encrypted payload/secret
bytes, configuration, grants, pending jobs, receipts and key checks; no decrypted
export is produced and no key is included. It flushes the completed file and
directory before success, and refuses an existing file or sidecar. Concurrent
changes after the snapshot point are not included. Backup requires additional
free disk and may contend with writes; schedule it for modest traffic and
monitor readiness/leases. Do not copy a running `state.db` alone or mix database,
WAL and SHM files from different points in time.

```sh
./bin/webhook-control-admin -db "$PWD/data/state.db" \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject local-admin \
  backup -destination /secure/backups/webhook-control-2026-10-24.backup

./bin/webhook-control-admin -db /secure/recovery/NEW-state.db \
  -key-file "$PWD/secrets/database.key" \
  -actor-issuer https://issuer.example -actor-subject local-admin \
  restore -backup-file /secure/backups/webhook-control-2026-10-24.backup
```

Restore opens the backup read-only, verifies SQLite integrity/schema and the
supplied encryption key, and writes a consistent snapshot into a **new** absolute
destination. The destination and its SQLite sidecars must not exist; it never
overwrites an active file. Keep restore directories private. For production
cutover, stop the old daemon, verify recovery against the new destination with
the same master/digest key files and approved policy, then change the configured
path. Restored pending/unexpired jobs resume and abandoned leases wait until
their persisted deadline. Restored expired jobs cannot be sent. A snapshot can
predate deliveries already accepted by receivers, so restoration can repeat
attempts with the same delivery ID; consumer deduplication remains necessary.

Test restores periodically in an isolated local environment with synthetic
receivers and separate destinations. Keep backups and keys independently
protected, and expire backups explicitly. Restoring an older backup may restore
credential versions and grants that were later changed; review them before
exposing management or allowing dispatch. In-service expiry is logical deletion,
not guaranteed physical erasure: old backups, WAL/free pages, snapshots and
filesystem/SSD behavior can retain prior ciphertext. A backup can also retain
payloads whose active TTL later expires; protect it and delete it on its own
retention schedule. Use disk encryption and suitable media/key erasure controls
for physical-erasure requirements. TTL `-1` requires an intentional storage plan.
