# WebhookControl

A standalone Go webhook router for one self-hosted server. JSON input is matched
by deterministic CEL rules, mapped to an explicit privacy template, and delivered
through independent routes. SQLite holds encrypted payloads and a durable
leased queue behind separate storage and queue interfaces.

The executable composes authenticated ingress, bounded CEL/rendering, encrypted
SQLite storage, leased Go workers, the HTTP Transporter and OAuth REST/MCP
management under one lifecycle. Local administration, online backup and restore
use a separate executable. No other workspace project is a runtime dependency.

Start with the [operator runbook](docs/RUNBOOK.md) for keys, OAuth, provisioning,
TLS, Docker/Compose, monitoring, retention and recovery. [Example files](examples/)
contain public placeholders and synthetic configuration; operators supply every
credential and approve each destination.

Read [architecture](ARCHITECTURE.md), the [REST/MCP contract](docs/CONTRACTS.md),
and the [OpenAPI schema](docs/openapi.json). Public Go packages are
[`domain`](domain/domain.go), [`ports`](ports/ports.go),
[`engine`](engine/engine.go), [`ingress`](ingress/handler.go),
[`httptransport`](httptransport/http.go), [`worker`](worker/worker.go), and
[`clock`](clock/clock.go), [`management`](management/service.go),
[`managementhttp`](managementhttp/handler.go), and [`admincli`](admincli/run.go).

Requires Go 1.26 or newer and Make. From this directory:

```sh
make check   # Formatting and go vet
make test    # Race tests, including real executable/issuer/receiver acceptance
make build   # bin/webhook-control and bin/webhook-control-admin (ignored)
```

The repository root delegates these commands through `make webhook-control-check`,
`make webhook-control-test`, and `make webhook-control-build`.

After supplying a trusted config and separate private 32-byte database/digest key
files, run the service and its minimal local readiness probe:

```sh
./bin/webhook-control -config /secure/webhook-control.json
# In another shell:
./bin/webhook-control -config /secure/webhook-control.json -check-ready
curl --fail http://127.0.0.1:8080/readyz
```

The default listener is `127.0.0.1:8080`. Configure the external OAuth issuer and
pinned resource URL explicitly; both REST and MCP require RS256 `at+jwt` access
tokens and source grants. Place production HTTP behind an external TLS reverse
proxy. The container example explicitly listens on `0.0.0.0:8080` and publishes
only the host loopback port. It runs as UID/GID 1000 with writable data and
read-only supplied key mounts. See the runbook before starting Compose.

```sh
docker build -t webhook-control:local .
docker compose config --quiet
# Start only after completing the runbook's operator setup:
docker compose up -d
```

`monitor` over REST/MCP returns only the authorized source's retained event/queue
counts, retry attempts, queue age, first-dispatch timings and logical ciphertext
usage. Bounded ingress counters reset on process restart. Global physical database
and WAL usage is available only through local `webhook-control-admin storage-stats`.
Public `/livez` and `/readyz` disclose only `status`. Queue or cleanup failure
closes serving and exits; a restart scans persisted work without needing wakeups.

Native acceptance tests build and launch both race-instrumented executables with
an ephemeral local RS256 issuer and consumer. The healthy latency assertion is
under one second for 20 small signed events with two concurrent ingress requests,
four workers and the default 250 ms queue poll. This is a local acceptance target,
not a production SLA.

This MVP supports one SQLite server and HTTP destinations. PostgreSQL, Telegram,
email/Gmail and a UI remain deferred. Management replay is queue-only and rejects
a matched delivery TTL `0` with `422`; retry cannot recover never-stored or expired
payloads. Immediate incoming TTL `0` permits one foreground attempt and no durable
retry. Expiry removes active ciphertext logically; old backups and SQLite/WAL or
filesystem remnants require their own retention and erasure policy.

The [`internal/sqlite`](internal/sqlite/store.go) adapter opens a disk database with
`Open(path, key, options...)`; the caller supplies a server-owned 32-byte key.
`WithClock` injects read/acceptance time, and `WithRetention` selects independent
finite metadata and deduplication windows (both default to seven days). Reopening
requires the same key. `Close` releases connections; `Cleanup(ctx, now)` deletes
expired payload rows and old status/deduplication rows. Workers must poll the queue
on startup and timers because wakeups are local, coalesced hints.

Acceptance requires SHA-256-shaped hex digests, explicit valid TTL/expiry pairs,
and pending frozen jobs. Original TTL `0` requires nil original bytes; a durable
delivery with TTL `0` is rejected. The adapter validates all initial records and
commits them in one transaction. Integration tests use temporary databases,
synthetic JSON, and ephemeral random keys; they create no runtime credentials.

`engine.Compile(ctx, config, destinationPolicy)` validates and copies a source
snapshot; `plan.Evaluate(ctx, rawJSON)` returns sanitized frozen fanout snapshots
without persistence or sends. CEL sees only `event`, supports `has()` and a fixed
`mask(string)` helper, and has bounded size, depth and execution cost. Templates
are explicit output-field maps. The [contract](docs/CONTRACTS.md) lists the
supported subset and every bound.

`ingress.New(dependencies, options)` returns an `http.Handler` for
`POST /v1/hooks/{source_id}`. Inject configuration and versioned secret readers,
Storage with receipt/retention support, destination policy and a clock; inject a
Transporter for immediate mode. Supply a separate 32-byte server digest key.
Bearer or canonical timestamped HMAC authentication and a bounded ASCII
`Idempotency-Key` precede processing. Durable `202` follows one atomic acceptance
of every sanitized job. Matching retries retain original IDs and mode, even when
active templates change. Rate limits, JSON limits and safe fixed errors apply.

Delivery TTL `0` commits metadata and deduplication, then permits one immediate
attempt within a bounded overall time budget. No rendered body enters storage or
the queue. Duplicate keys never send again, including after a crash or unknown
outcome. Known immediate failures return `502`; this mode offers no durable
retry. SQLite schema v2 adds receipt mode and safe immediate status metadata;
opening a v1 database migrates it transactionally without changing ciphertext.

`httptransport.NewPolicy` copies an exact-origin server allowlist. Public origins
require HTTPS; an explicit `OriginRule.AllowPrivate` permits HTTP and private,
loopback or CGNAT addresses for that origin. Metadata, link-local, unspecified,
multicast and reserved addresses remain forbidden. Static approval performs no
DNS or sends. Origin comparison normalizes host case, trailing DNS dots, IP
notation and default ports; paths and queries confer no additional permission.
Agent grants must narrow this server policy before configuration activation.

`httptransport.New(policy, secretReader, options)` implements one send. Every
attempt rechecks its frozen URL, validates the complete DNS answer, and dials
only validated numeric addresses while preserving TLS hostname verification.
It uses no environment proxy, follows no redirect, and reads no receiver body.
Only versioned secret references are resolved at send time; header validation
prevents injection and overrides of transport-owned headers. `Idempotency-Key`
is the stable delivery ID. Timeout and network failures can be ambiguous, so
receivers must deduplicate that ID. Returned errors are fixed safe sentinels.

`worker.New(dependencies, options).Run(ctx)` consumes only Queue, Clock, and
Transporter. Defaults are four workers, 30-second leases renewed every ten
seconds, one-second fallback polling, eight attempts, and one-second exponential
backoff capped at one minute with half-to-full jitter. `Retry-After` accepts delta
seconds or HTTP dates, capped at one hour by the Transporter and at the worker's
maximum delay. Startup scans and timers recover committed jobs and abandoned
leases without wakeup hints. Lease loss, deadline expiry and shutdown cancel the
send before that worker can claim again. Queue failures stop the pool with a
safe error; a supervisor can restart it to recover unexpired jobs.

Workers complete `2xx`, retry network failures, `408`, `429` and `5xx`, and fail
other statuses or security/secret/payload errors. Exhaustion records
`retries_exhausted`; a retry delay reaching payload expiry records
`retry_window_exhausted` without another send. An elapsed payload deadline
records `expired`. Metadata never stores receiver bodies or raw error strings.
`clock.Real{}` supplies production time. Tests use local receivers, explicit
loopback rules, temporary SQLite files and synthetic ephemeral keys.
