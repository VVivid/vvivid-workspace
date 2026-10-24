# REST and MCP contracts

Ingress and OAuth REST/MCP management are injectable HTTP handlers composed by
the standalone daemon. The machine readable ingress and
management-envelope schemas are in [OpenAPI](openapi.json).
All examples and tests must use synthetic data. No endpoint sends during preview.

## HTTP delivery and workers

The server's exact-origin allowlist applies to configuration approval and every
frozen send. Public destinations require HTTPS; HTTP and private/loopback/CGNAT
addresses require an explicit exception for that origin. Exceptions never allow
metadata, link-local, unspecified, multicast, documentation or reserved networks.
DNS is resolved at connection time; every returned address must pass policy
before the connection is pinned to numeric addresses. TLS verifies the original
hostname. Environment proxies and redirects are disabled. Receiver bodies are
closed without reading or logging, and response headers are bounded to 16 KiB.

Each attempt sends only its frozen sanitized JSON and immutable secret versions,
with the delivery ID as `Idempotency-Key`. The configured timeout covers secret
resolution, DNS, connecting, TLS and HTTP response headers. `Retry-After` accepts
nonnegative delta seconds and HTTP dates, capped at one hour. Workers apply their
own maximum retry delay and bounded exponential backoff with jitter.

Workers claim on startup, after completed attempts, on wakeup hints and on a
fallback timer. Concurrency is bounded. Heartbeats renew ownership while sending;
lease loss, lease/payload expiry and shutdown cancel the in-flight attempt.
`2xx` completes receiver acceptance. Network errors, timeouts, `408`, `429` and
`5xx` may retry with the same ID and frozen body; other statuses and policy,
secret or payload failures are terminal. Max attempts record `retries_exhausted`.
A retry whose delay reaches payload expiry terminates with
`retry_window_exhausted`; no retry is scheduled at or after that deadline.
Actual payload expiry makes a pending or leased delivery `expired`.
Only fixed machine error labels enter status metadata.

## Ingress

`POST /v1/hooks/{source_id}` requires `Content-Type: application/json` (optional
`charset=utf-8`) and one uncompressed JSON object, at most 1 MiB. Arrays, scalars,
null, trailing values, malformed JSON, depth above 64 and more than 65,536 JSON
values are rejected. IDs are 1–128 ASCII letters, digits, `.`, `_` or `-`.
`Idempotency-Key` is mandatory: 1–128 visible ASCII bytes (`!` through `~`), with
no spaces or control characters. Duplicate authentication/key headers are rejected.

The active source selects exactly one authentication method. Bearer mode uses
`Authorization: Bearer <source secret>` and requires a server-provisioned secret
of 16–4096 bytes. HMAC mode requires a 32–4096 byte secret, canonical decimal Unix
seconds in `X-Webhook-Timestamp` (no leading zeroes or plus sign), and
`X-Webhook-Signature: sha256=<64 hex digits>`. The timestamp must be within five
minutes of the injected current clock after the complete body has been read.
Compute HMAC-SHA256 with the source secret over these exact bytes:

```text
WebhookControl/v1\n{source_id}\n{Idempotency-Key}\n{X-Webhook-Timestamp}\n{raw body}
```

Here `\n` means one LF byte. Braces mark substituted values and are not signed.
There is no extra newline after the raw body; whitespace in the body is signed.
The source ID and idempotency key are signed as well as timestamp and body, so
changing any of them invalidates authentication. Comparisons use constant-time
digest checks before JSON parsing or CEL. Authentication failures consume the
per-source quota. The default limit is 60 requests per fixed one-minute window,
configurable from 1 to 10,000. The limiter tracks at most 1,024 known sources;
full live capacity rejects new windows until old ones expire. `429` includes
`Retry-After: 60`. Unknown sources do not allocate limiter state.

Source ID namespaces deduplication. A separate server-owned 32-byte key computes
HMAC-SHA256 digests over `WebhookControl/v1/dedup\n{source_id}\n{key}` and
`WebhookControl/v1/body\n{source_id}\n{raw body}`. Raw keys and originals never enter
dedup records. The default window is seven days; a configured positive window
must not exceed the storage adapter's reported window. Changed raw bytes,
including whitespace, conflict even when their parsed objects are equivalent.

For durable mode, `202` means the atomic acceptance transaction committed, including
every initial job. It returns `event_id`, `delivery_ids`, `duplicate`, and
`mode: durable`. An identical retry within the dedup window returns the original
IDs with `duplicate: true` and creates no job or external effect. Reusing the key
with different bytes returns `409`. No matching route still commits acceptance
and returns an empty delivery list.

Receipts are looked up after current source authentication but before current
rules/templates are compiled. A retry returns the original mode and IDs even if
the source has since changed templates or delivery mode. Source disablement and
current authentication still apply. Concurrent first requests are serialized by
the acceptance transaction; only the winner may trigger immediate sends.

For delivery TTL `0`, `200` with `mode: immediate` reports the single foreground
attempt's receiver results; a failed attempt returns `502`. It offers no crash
recovery or durable retry. An identical retry reports the original event and
`duplicate: true` without sending again. Results may be unknown if the first
process died. `202` is never used to promise delivery in this mode.

Immediate acceptance commits metadata and deduplication before any send, with
no rendered payload row and no claimable queue job. Minimal delivery metadata
starts as `unknown`; a completed attempt records only state, status and a fixed
error code. Each destination receives at most one foreground attempt. The entire
fanout has a 30-second default budget (configurable up to one minute), plus each
destination's shorter timeout. Once exhausted, no further attempt starts and
remaining results stay `unknown`; the first response is `502`. A known failed
receipt also returns `502` on duplicate requests. Unknown duplicate receipts
return `200` with `unknown` results and **never send again**. A crash, disconnected
sender, network ambiguity, or failed outcome write can leave unknown receipts;
sender retries with the same key do not redeliver them. Outcome-write failure
returns `503`; another key is a new event, not recovery of this attempt.

Other errors use `{ "code": "stable_code", "message": "safe explanation" }`:
`400` malformed input, `401` failed source auth, `403` disabled source or forbidden
destination, `413` oversized input, `422` failed rule/template evaluation, and
`429` source quota exceeded, and `503` unavailable storage/configuration. Errors
must not echo secrets or payloads.
An ambiguous commit outcome can be resolved by retrying the same idempotency key.

## CEL and template limits

The `engine` package validates and copies a whole configuration before use.
Every immutable positive resource revision must exist in the snapshot and belong
to the source. Duplicate resource revisions and duplicate bindings are invalid.
HTTP settings require an HTTP(S) URL without userinfo or fragments, a timeout of
1–60 seconds and explicit injected destination-policy approval. A policy may
allow private HTTP endpoints explicitly; the production Transporter must enforce
current DNS/IP restrictions again on every attempt. Literal headers are limited
to `Accept`, `Content-Type: application/json`, and `User-Agent`; other allowed
headers use immutable secret references. Hop-by-hop, host, length, idempotency,
content encoding and content type overrides cannot come from secret headers.
Receiver secret values are never resolved by the rule/template engine or ingress.

Rules use `event`, the incoming object, and must return a Boolean. Enabled routes
match when every referenced rule is true; all enabled routes are considered.
Templates contain a `fields` map of output names to CEL expressions. Only mapped
fields appear; selecting the whole `event` is an explicit disclosure choice.
Standard deterministic CEL operators and functions and `has()` are supported.
Comprehension macros (`all`, `exists`, `exists_one`, `map`, `filter`) are disabled
in this MVP to avoid map iteration order affecting results and budgets. There is
no network, clock, randomness or secret variable. `mask(string)` always returns
the fixed string `***`, revealing neither value nor length. JSON output supports
string-keyed objects, arrays, null, booleans, finite numbers and strings. CEL-only
types require explicit string formatting. Integer input within int64 preserves
precision; other representable JSON numbers use double precision.

For example, rule `event.sender == 'synthetic-sender'` and template
`{"message":"event.message","contact":"mask(event.contact)"}` forward only
message and the masked constant, discarding other fields. Use
`has(event.subject) ? event.subject : 'notice'` for an optional input field.

Bounds are 256 KiB serialized configuration, 32 resources of each kind, 256 total
rule/field expressions, 4 KiB per expression, parser recursion 64, 32 fields per
template, 16 Transporters per route and 128 total route bindings. Evaluation uses
the request context, at most 5,000 CEL cost units per expression and 50,000 across
the whole event. A render has a 64 KiB encoded-byte budget, including the encoder's
trailing LF removed from the returned body, with at most 1 MiB across all fanout
bodies. Escaped strings and keys, scalar encodings and container punctuation are
charged during conversion before the encoder buffers output; repeated references
are charged for every occurrence. Output depth and value counts use the input
JSON bounds. Any rule, template, conversion or limit error rejects the event before acceptance; there
is no partially queued fanout. Preview can call this same engine without sending.

## Management

`POST /v1/manage/{operation}` is the shared REST command surface. MCP exposes each
operation below as a tool over `POST /mcp`; the CLI calls the same application
operations. Management requires OAuth bearer authentication and a source-specific
grant. Local administrator grant/secret provisioning is a separate privileged CLI
capability. Tools cannot grant themselves access.

Each source operation supplies `source_id` and a typed `input`. `sources_list`
and `health` need only `{"input":{}}` and require at least one current source grant.
Mutating configuration and replay also require `expected_revision`; stale writes
return `409`. Other operations reject that field. The operation's action
must appear in the caller's unexpired grant. Referenced objects must belong to the
requested source. Responses cannot include secret values. Pagination uses bounded
`limit` and opaque `cursor`; expiry does not turn a list into unrestricted access.
Grants also cap original and delivery retention separately. A nonnegative
`max_seconds` permits TTLs up to that ceiling, while TTL `-1` requires explicit
`allow_indefinite: true`. Missing ceilings permit only TTL `0`. Configuration writes,
activation, and replay check these ceilings so source write access cannot expand
retention. Each route binds one or more Transporter revisions and fans out into
independent jobs sharing its sanitized render.

| REST operation / MCP tool | Input | Result | Required action |
| --- | --- | --- | --- |
| `config_get` | Source ID | Active configuration and revision | `config.read` |
| `config_put` | Expected revision and draft `Configuration` | New draft revision | `config.write` |
| `config_activate` | Expected active revision and current draft revision | New active revision | `config.activate` |
| `config_rollback` | Expected active revision and retained target revision | New active revision selecting retained config | `config.activate` |
| `preview` | Draft revision and synthetic JSON event | Matched routes and sanitized renders; never sends or queues | `preview` |
| `events_list` | Cursor and limit | Event metadata page | `events.read` |
| `events_get` | Event ID | Metadata only | `events.read` |
| `events_replay` | Event ID, selected snapshot revision, expected active revision and request idempotency key | New event/delivery IDs using explicitly selected config | `events.replay` |
| `deliveries_list` | Optional event ID, cursor and limit | Status page without bodies or secret refs | `deliveries.read` |
| `deliveries_get` | Delivery ID | Status, attempts, safe error code and frozen revision IDs | `deliveries.read` |
| `deliveries_retry` | Failed delivery ID and request idempotency key | New delivery ID with same frozen render/config and original expiry | `deliveries.retry` |
| `monitor` | Source ID and empty input | Source-scoped retained queue/state, age, retry, dispatch and ciphertext metrics; optional process ingress counters | `deliveries.read` |
| `sources_list` | Empty input | Only source IDs with current explicit grants | Any current source/action grant |
| `health` | Empty input | Management storage readiness | Any current source/action grant |

Replay fails with `410` once original bytes expire or when they were never stored.
Retry fails with `410` once the rendered payload expires. It cannot bypass current
server destination restrictions. Neither operation mutates the original history.
Management replay is queue-only: if the selected configuration matches a route
with delivery TTL `0`, replay returns `422 durable_replay_required` and commits
nothing. Ordinary incoming TTL-zero events retain their foreground attempt behavior.
Retry preserves the original rendered-payload expiry rather than starting a new TTL.
Draft validation rejects invalid TTLs, unknown or cross-source revisions, invalid
CEL/mappings, unapproved destinations, and unapproved secret references before
activation. Rules and templates have no network, clock, randomness, or secret access.

MCP `tools/list` advertises only permitted tools with the same JSON schemas as REST.
`tools/call` repeats authorization for every invocation; a stale tool listing confers
no access. MCP tool errors carry the same stable code as REST and `isError: true`.
Protocol negotiation does not bypass OAuth or source grants. The local admin CLI
is responsible for configuring OAuth and grants before remote management starts.

### Configuration revisions and requests

Snapshot revisions are separate from resource revisions. `config_get` returns
`head.latest_revision`, `head.draft_revision`, `head.active_revision`, and an
optional active `snapshot`. Before first activation, the active snapshot is absent.
`config_put` compares `expected_revision` with latest and creates a new immutable
draft. Activate compares with active and requires the selected draft still be
current. Activate and rollback each create a new monotonic snapshot selecting
retained content. They return the new head and snapshot. Rollback never rewrites
resource revisions, old snapshots, events or frozen deliveries. Changing any
historical resource content requires a new resource revision; moving its ID to a
different source is forbidden across all revisions.

For example, the first draft is revision `1` and its first activation is revision
`2`. A later draft becomes `3`; activating it creates `4`. Rolling back to content
from `1` with expected active `4` creates a new active selection `5`.

```json
{"source_id":"source-a","expected_revision":2,"input":{"draft_revision":3}}
```

Use that envelope for `config_activate`. Other typed inputs are:

| Operation | `input` fields |
| --- | --- |
| `config_get`, `sources_list`, `health` | `{}` |
| `monitor` | `{}` with an explicitly authorized `source_id` |
| `config_put` | `configuration`: complete `domain.Configuration` |
| `config_rollback` | `target_revision`: retained snapshot revision |
| `preview` | `revision`: snapshot revision, `synthetic`: `true`, `event`: synthetic JSON object |
| `events_list` | Optional `limit`, `cursor` |
| `events_get` | `event_id` |
| `events_replay` | `event_id`, `revision`: selected snapshot revision, `idempotency_key` |
| `deliveries_list` | Optional `event_id`, `limit`, `cursor` |
| `deliveries_get` | `delivery_id` |
| `deliveries_retry` | `delivery_id`, `idempotency_key` |

Replay's `expected_revision` compares with the current active snapshot even when
the selected `input.revision` is retained history. Request keys are 1–128 visible
ASCII bytes, scoped to principal, source and operation. Repeating the same key and
selection returns the first IDs with `duplicate: true`; a changed selection returns
`409 idempotency_conflict`. Only the first mutation creates lineage and audit.
Current authorization is checked again before resolving a receipt. Missing or
expired retry payloads still return `410`. No token, key or payload enters audit.
Replay records `replay_of` on new events; retry records `retry_of` on new deliveries.

Lists return `{ "items": [...], "cursor": "..." }`, omitting cursor at the end.
The default limit is 50; other supplied limits clamp to 1–100. Cursors bind the
source, list kind and optional event filter; mismatches return `400 invalid_cursor`.
Every database list still predicates on the authorized source, including forged
cursors. Delivery status contains only IDs, safe revision references, times,
retention, state, attempts, HTTP status and fixed error codes. Status never returns
original/rendered bodies, URLs, headers, secret refs or secret values. Preview
returns only route/Transporter IDs and its explicitly synthetic sanitized renders.
Requests are bounded to 2 MiB, unknown JSON fields are rejected, and authenticated
responses use `Cache-Control: no-store`. Error bodies use the same safe `code` and
`message` fields as ingress; engine/configuration failures are `422`.

### OAuth access-token profile

Configure a trusted external issuer URL and a pinned resource URL ending in `/mcp`.
Both REST and MCP require the **same resource URL as audience**. Issuer discovery
and JWKS are verified with `go-oidc`; supported signatures are RS256. The signed
JWT protected header must have `typ: at+jwt`. Tokens need matching `iss` and `aud`,
a nonempty `sub`, a future `exp`, and space-separated scope
`webhook-control.manage`. If `token_use` is supplied it must be `access`. ID tokens
with `typ: JWT`, missing access-token typing, or `token_use: id` are rejected.
The issuer must implement this access-token profile; there is no token passthrough
to receiver destinations. Identity is the verified issuer/subject pair, not caller
JSON, session IDs or a subject shared across issuers.

HTTPS is required for issuer, discovery redirects, JWKS and resource URLs.
`AllowInsecureLoopback` is an explicit trusted configuration option for ephemeral
local tests only; it permits HTTP only on loopback. Missing/invalid OAuth on REST,
MCP initialization and tool listing gets an OAuth challenge advertising the pinned
protected-resource metadata URL and required scope. The public
`GET /.well-known/oauth-protected-resource` (also available with `/mcp` appended)
advertises configured values independently of the request Host. Requests with
Origin must match the configured resource origin or an explicit server-owned
allowed origin. Grants must already exist before remote initialization succeeds.

### Local administration

Build `./cmd/webhook-control-admin` or run it with `go run`. Commands operate on
the local SQLite database using a supplied raw 32-byte encryption key file. They
do not generate keys, log in to live accounts, start a daemon or provision live
infrastructure. Keep all supplied files outside source control. The actor arguments
record the local administrator in audit; `invoke` uses that actor's actual grants.

```sh
go run ./cmd/webhook-control-admin \
  -db /srv/webhook-control/state.db -key-file /run/secrets/database-key \
  -actor-issuer https://issuer.example -actor-subject local-admin \
  grant-put -issuer https://issuer.example -subject automation-agent \
  -file /secure/supplied-grant.json

go run ./cmd/webhook-control-admin \
  -db /srv/webhook-control/state.db -key-file /run/secrets/database-key \
  -actor-issuer https://issuer.example -actor-subject local-admin \
  secret-put -name source-a-auth -version v1 -purpose source-auth \
  -value-file /secure/supplied-source-secret

go run ./cmd/webhook-control-admin \
  -db /srv/webhook-control/state.db -key-file /run/secrets/database-key \
  -actor-issuer https://issuer.example -actor-subject automation-agent \
  invoke -policy-file /secure/server-origin-policy.json \
  -operation config_get -file /secure/config-get-request.json
```

A grant file contains `id`, explicit `source_ids`, `actions`,
`destination_origins`, `secret_names`, `original_retention`, `delivery_retention`
and optional `expires_at`. The CLI derives `principal_id` from its explicit
`grant-put -issuer/-subject`; a supplied principal must match. Empty lists grant
no corresponding access. Omitted retention ceilings permit only `0`; granting
`-1` requires `allow_indefinite: true`. For example, this grants configuration read:

```json
{"id":"read-source-a","source_ids":["source-a"],"actions":["config.read"],"destination_origins":[],"secret_names":[],"original_retention":{"max_seconds":0,"allow_indefinite":false},"delivery_retention":{"max_seconds":0,"allow_indefinite":false}}
```

The server-origin policy file is an array such as
`[{"origin":"https://receiver.example","allow_private":false}]`. `secrets-list`,
`grants-list` and `audit-list` inspect metadata locally. Secret versions are
immutable across restart: changing bytes or purpose under the same name/version
is rejected. `source-auth` credentials may only authenticate ingress;
`http-header` credentials may only populate outbound headers. Granting an inbound
secret name never authorizes exporting its bytes through a destination header.
Configuration writes and activation verify secret availability and source-auth
minimum length (16 bytes bearer, 32 bytes HMAC), without returning values.
Daemon composition must pass purpose-scoped `Store.SecretsFor` readers to ingress
and the HTTP Transporter, as described in the architecture.

## Runtime operations

`webhook-control -config /absolute/operator.json` requires explicit private raw
32-byte database and ingress digest key files and trusted OAuth issuer/resource
URLs. The digest key is separate and pinned to the database across restarts;
management receipt hashes use their existing domain-separated database-key
derivation consistently in CLI and daemon. HTTP defaults to loopback. The daemon
supervises ingress, REST/MCP, workers and retention cleanup together; unexpected
queue/cleanup errors clear readiness, cancel active requests and stop serving.
Startup scans durable work without relying on channel hints.

Public `GET /livez` and `GET /readyz` expose only a fixed status. `monitor` is an
OAuth/grant-protected source operation, available through REST and MCP, and never
returns global disk totals. Its counts and first durable-claim latency aggregates
cover retained records. Optional accepted-event, duplicate-request and rejected
known-source ingress counters are bounded, process-local and reset on restart.
Local `storage-stats` alone exposes physical database/WAL usage. No payload,
credential value or cross-source metric appears in these status surfaces.

The administrator CLI adds `backup -destination /absolute/NEW.backup` and
`restore -backup-file /absolute/snapshot.backup` with `-db /absolute/NEW.db`.
Backups use an online consistent SQLite snapshot including committed WAL pages;
restore verifies the supplied key and integrity and refuses overwrite. Both
preserve ciphertext and require separately retained operator keys. See the
[runbook](RUNBOOK.md) for provisioning, limits, Compose ownership, reverse-proxy
TLS, recovery, backup retention and logical-expiry/physical-erasure limits.
