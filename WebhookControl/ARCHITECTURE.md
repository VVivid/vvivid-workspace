# Architecture and durable boundaries

The public `cmd/webhook-control` executable composes ingress, management REST/MCP,
the HTTP Transporter, Go workers and SQLite under `daemon.Run`. Trusted JSON
configuration requires separate explicit master/digest key files and OAuth
issuer/resource URLs. Startup scans persisted work, runs retention cleanup and
pins the ingress digest key; queue or cleanup failure closes readiness and
serving. HTTP defaults to loopback with bounded connections, bodies, headers and
timeouts; external production TLS is an operator reverse-proxy responsibility.
The independent Docker/Compose build runs as UID 1000 with supplied key mounts.

SQLite schema v4 adds first-attempt timestamps and a digest-key check without
changing payload ciphertext. Source-scoped `monitor` executes aggregates in the
same transaction as grant checks. Global storage usage and consistent online
backup/restore remain local administrator capabilities, outside worker Queue and
remote management boundaries. See the [runbook](docs/RUNBOOK.md) for operational
contracts and backup/physical-erasure limits.

WebhookControl is an independent module on one server. Configuration, event
metadata, deduplication, encrypted payloads, and delivery jobs live in SQLite.
There is no PostgreSQL adapter, broker, dashboard, or non-HTTP Transporter in the MVP.

## From event to delivery

1. Authenticate a source, validate JSON and size limits, and derive a keyed
   deduplication digest and input fingerprint. Load one active configuration snapshot.
2. Evaluate deterministic CEL rules. Enabled routes match when all their rules
   return true; an empty rule list matches all valid events. Templates output only
   explicitly mapped fields. Evaluation errors reject acceptance before any job exists.
3. Render each match once. Each route binds one or more Transporters and creates
   one job per Transporter. Freeze source, rule, route, template and Transporter
   revisions, HTTP settings, secret versions, and sanitized JSON for every job.
4. Call `ports.Storage.Accept` once. Its transaction commits the event metadata,
   deduplication record, permitted original bytes, and **all** initial durable jobs.
   No partial acceptance is visible. A durable acknowledgement follows the commit.
5. Wake workers after commit. Channel signals only reduce latency; startup scans
   and timers recover pending work even when a signal is lost or the process dies.
6. Workers claim, send, and transition individual jobs through `ports.Queue`.
   One route's retry or failure cannot block another route's job.

`domain` contains data contracts. `ports` keeps storage, configuration reads, queue
leases, time, and transport distinct. A SQLite adapter can implement multiple ports
and share its transaction internally. Callers cannot enqueue outside acceptance.
Clock and Transporter injection will allow deterministic worker and recovery tests.

`engine` owns bounded validation, deterministic CEL and explicit JSON rendering;
it returns copied frozen fanout snapshots without sending or storing. `ingress`
owns source-specific authentication, bounded per-source request windows, receipt
lookup and acceptance assembly. Both use injected configuration and policy ports.
Ingress reads source secrets only; receiver secrets are left as immutable refs.
The supported CEL subset and canonical HMAC format are in the REST contract.

## SQLite adapter

`internal/sqlite.Store` implements Storage and Queue. A versioned schema migration
creates separate event/delivery metadata, original ciphertext, frozen-delivery
ciphertext, and deduplication tables. The full frozen record, including its body
and HTTP settings, is AES-256-GCM encrypted with a fresh random nonce. Additional
authenticated data binds ciphertext to its record ID and kind. Opening with the
wrong server-owned key fails; keys and plaintext are never database columns.

Every connection enables foreign keys, a five-second busy timeout, and full
synchronous commits. The database uses WAL. Each adapter has one pooled connection;
immediate write transactions serialize acceptance and claims across adapters.
Database errors use fixed messages, and delivery errors accept only these fixed
codes: `network_error`, `timeout`, `http_retryable`, `http_permanent`,
`policy_denied`, `secret_unavailable`, `invalid_payload`, `retries_exhausted`,
`retry_window_exhausted`, and `canceled`. Receiver responses and arbitrary error
messages cannot be stored.

Acceptance validates representable timestamps, overflow-safe TTLs, matching
expiry times, source identities, and initial pending state. Deduplication receives
64-character hex key/fingerprint digests, with expiry later than acceptance and
within the configured finite window. A matching duplicate returns the original
event and delivery IDs; a changed fingerprint conflicts. Any failed insert rolls
back metadata, deduplication, payloads, and all jobs together.

Read time is injectable because the Storage read port has no time argument.
Original reads hide expired bytes. Delivery reads return metadata with an empty
snapshot after payload expiry, projecting pending/leased jobs as `expired`.
Unexpired Delivery reads include the decrypted frozen snapshot for trusted callers;
future management handlers must explicitly select authorized metadata fields.
Cleanup removes expired ciphertext rows; reads already enforce the deadline.
Minimal metadata defaults to seven days and remains while referenced by retained
payloads or jobs. Deduplication defaults independently to seven days and can expire
while metadata remains. SQL deletion does not erase historical ciphertext from
old WAL pages or backups; backup handling belongs to the deployment runbook.

Schema v2 migrates existing v1 records in one transaction, adding original receipt
mode and safe immediate HTTP-status metadata. `ReceiptReader` resolves a keyed
receipt before current rendering, preserving the first mode/IDs when active
configuration changes. `ImmediateStorage` records a single result for TTL-zero
metadata. Such metadata starts as `unknown`, contains no frozen payload/settings,
and is never claimable. Missing results after a crash mean unknown and cannot
authorize a resend. The initial acceptance still commits all immediate IDs and
deduplication together. Its overall foreground budget stops additional sends on
expiry, leaving remaining metadata unknown; retries return the retained receipt.

Claims also expire old payloads, atomically acquire one due pending job or expired
lease, and increment its attempt count. Lease deadlines are capped at payload
expiry. Token/deadline predicates fence renewals and every terminal/retry update.
Retry times must be in the future and strictly before payload expiry. Only `2xx`
can complete a delivery. Notifications follow successful commits and do not cross
adapter instances; durable polling provides recovery and cross-instance discovery.

## Retention and privacy

Retention values are integer seconds measured from event acceptance. Values below
`-1` are invalid. Missing configuration receives explicit defaults before activation;
the domain zero value is the deliberate privacy choice `0`, never a default marker.

| TTL | Original payload | Rendered delivery payload |
| --- | --- | --- |
| `0` | Never written, including transient database rows | Never written; immediate send only, no durable retries |
| Positive | Expires after that many seconds | Expires after that many seconds; cannot be claimed or retried afterward |
| `-1` | Retained until explicit deletion | Retained until explicit deletion |
| Recommended default | `86400` (one day) | `604800` (seven days) |

Original TTL `0` still permits durable sanitized delivery when delivery TTL is
positive or `-1`. Original deletion never deletes a still-valid rendered job.
Delivery TTL `0` permits one foreground attempt after committing metadata and
deduplication, with no queued payload. A process crash or ambiguous send can lose
this immediate delivery. It cannot receive a durable delivery acknowledgement.
Mixed immediate and durable delivery modes within one source are outside this MVP.

Minimal event/delivery status and keyed deduplication digests have separate bounded
retention. They never retain original JSON or rendered bodies through error strings,
logs, audit fields, response bodies, or dead-letter metadata. Deduplication has a
finite server-configured window independent of payload TTL; after that window the
same input can be accepted again. Cleanup removes expired payloads; reads and queue
eligibility enforce expiry even before cleanup. Durable payloads are encrypted at
rest, and encryption keys remain server-owned.

## Immutable configuration and authority

All configuration objects have a stable ID and immutable positive revision.
Rules, templates, routes, and Transporters belong to exactly one source; cross-source
references are invalid. Activation atomically selects a validated snapshot using
an expected revision. Editing or rolling back affects future events only. Existing
jobs keep their rendered body, destination, headers, and versioned secret references.
Deleting configuration cannot remove revisions or secrets needed by unexpired jobs.

REST, MCP, and CLI management share authorization and revision checks. OAuth
identifies the principal; explicit grants intersect source IDs, actions, expiry,
destination origins, permitted secret names, and separate original/delivery retention
limits. Empty scope lists grant nothing. Each retention limit permits `0` through
its nonnegative `max_seconds`; `-1` additionally requires `allow_indefinite: true`.
Missing limits permit only `0`. Writes, activation, and replay cannot extend either
lifetime beyond the principal's limits, even within an otherwise permitted source.
Only a local administrator can issue grants and provision secret values. Agents
can select approved server-owned references and cannot write or retrieve secrets.
Public literal headers cannot contain credentials; credentials use secret headers.

Schema v3 adds immutable snapshot selections and resource versions, heads for
latest/draft/active selections, grants, audit, encrypted immutable secret versions,
and source/principal/operation-scoped management receipts. A snapshot revision is
separate from the resource revisions it selects. Draft writes compare latest;
activation and rollback compare active. Activation also requires the current draft.
Every selection gets a new monotonic snapshot, including rollback; retained resource
content cannot be changed under its old kind/ID/revision or moved to another source.
Frozen revision IDs have separate safe metadata columns that survive payload expiry.
Migration recovers those IDs from retained pre-v3 ciphertext; already-deleted old
payloads cannot supply historical IDs that were never separately recorded.

`management.Service` owns the command registry and authorization for REST, MCP
and CLI. `ports.ManagementStore.Manage` serializes grant checks, configuration
validation, selection, event/retry insertion, idempotency receipts and audit in
one transaction. A callback failure rolls everything back; queue hints follow
commit. Grants authorize one issuer/subject tuple, encoded as an unambiguous JSON
pair. At least one complete grant must authorize the selected configuration;
permissions from different grants are not combined to widen one selection.
Activation, rollback, replay and retry recheck retained content against current
grant ceilings, destinations, secret names and server policy. Preview only evaluates
explicitly synthetic caller input and never obtains originals or starts a send.

Secret versions carry immutable purposes, `source-auth` or `http-header`. Purpose,
name and version are bound into AES-GCM additional authenticated data. Management
rejects selecting source-auth secrets as outbound headers, even if their names
appear in a grant. Daemon composition must inject `Store.SecretsFor(source-auth)`
into ingress and `Store.SecretsFor(http-header)` into the HTTP Transporter. Those
readers repeat purpose checks at use time. Local provisioning requires the database
key and supplied files; there is no remote grant/secret provisioning operation.

REST and official Go SDK stateless streamable HTTP MCP share pinned OAuth issuer
verification. The supported JWT access-token profile is RS256 with `typ: at+jwt`,
the configured resource audience, nonempty subject, future expiry and required
scope. ID tokens are rejected. Protocol initialization and tool discovery use the
same authentication and current-grant checks. Tool discovery is filtered, while
execution always repeats source/action authorization. Metadata uses the configured
resource URL; an untrusted Host cannot change it. See the management contract for
request envelopes, replay restrictions, local commands and authorization setup.

The server also owns a destination-origin allowlist. Agent grants can narrow it,
never expand it. A URL must satisfy both. Origin matching and DNS/IP checks on each
attempt, disabled or revalidated redirects, and blocked local/private/metadata
destinations prevent a previously valid configuration from bypassing SSRF controls.
Receiver URLs cannot contain userinfo. These checks remain required on frozen jobs.
Secret values are resolved only for sending and never copied into durable jobs.

## Queue ownership and acknowledgements

`pending -> leased -> http_accepted` is the normal durable path. A retry releases
the lease into `pending` with a next-attempt time. A permanent failure enters
`failed`. Payload expiry enters `expired` and prevents further sends. Terminal jobs
are not claimable. Explicit authorized replay creates a new delivery ID and records
its lineage; it does not silently reopen a terminal job.

Claiming is atomic and increments the attempt counter. Each claim has a fresh
fencing token and a finite lease. Renew, complete, retry, and terminate require the
current unexpired token. Recovery may reclaim expired leases; an old worker cannot
update them. Retry times must be earlier than payload expiry. Adapters reject invalid
durations and timestamps. Workers stop an attempt when renewal fails.

Network effects and SQLite cannot share a transaction. A crash after receiver
acceptance and before `Complete` causes a retry: durable delivery is **at least
once**. Every attempt sends the stable delivery ID as `Idempotency-Key`, allowing
the receiver to deduplicate. Retrying keeps the frozen body and config; replay gets
a new ID. Receiver idempotency is needed even with leases because an old request
may still reach a receiver after lease ownership changes.

HTTP `2xx` records `http_accepted`: the receiver accepted the request. An HTTP `202`
from a receiver or from ingress does not establish that an external agent completed
its business work. This MVP has no agent-completion acknowledgement protocol.
