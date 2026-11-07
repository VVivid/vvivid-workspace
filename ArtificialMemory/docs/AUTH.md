# Private MCP service and account administration

ArtificialMemory is a private backend for the existing
[MCPGateway v1 identity contract](../../MCPGateway/docs/IDENTITY.md). The gateway
owns public OAuth discovery, provider login, external token verification, and
gateway signing keys. The backend has pinned public keys only. It exposes three
memory tools and no browser UI, SQL interface, login flow, or enrollment tool.

## Configuration

Build with `make build`; the executable is `bin/artificial-memory`. Configure:

| Variable | Meaning |
| --- | --- |
| `MEMORY_DATABASE` | Required private disk SQLite path. New directories/files use 0700/0600; use a dedicated private directory. |
| `MEMORY_LISTEN` | Listener, default `127.0.0.1:8080`. An explicit `:8080` supports private service networking. |
| `MEMORY_BACKEND_ORIGIN` | Required exact HTTP(S) origin without path/query/fragment, with lowercase canonical Host and optional port. Its Host must equal the gateway route's fixed `upstream_host`. |
| `MEMORY_GATEWAY_ISSUER` | Required exact HTTPS gateway public origin, without trailing slash. |
| `MEMORY_AUDIENCE` | Required exact sole backend audience minted by the gateway route, such as `artificial-memory-mcp`; distinct from the public route's external OAuth resource. |
| `MEMORY_IDENTITY_ISSUER` | Required exact original HTTPS OIDC issuer, including its realm path when applicable. |
| `MEMORY_GATEWAY_JWKS_FILE` | Operator-installed public gateway JWKS; no private key or provider token. |
| `MEMORY_ALLOWED_CLIENT_IDS` | Required comma-separated exact external client allowlist, 1–32 distinct IDs; no surrounding whitespace. |

Missing/partial trust, an invalid key set, unsupported storage, or an invalid
canonical origin prevents listening. There is no authentication fallback, remote
JWKS fetch, token-chosen key URL, synthetic signer, or test-auth option in the
production command. Private upstream HTTP is supported only behind trusted
networking; bearer tokens remain replayable until their short expiry. Do not
publish the backend listener directly.

The gateway route must use the same audience/client allowlist and fixed Host.
Its `required_scopes` must be nonempty. A simple route requires `memory:read` and
sets `forward_scopes` to `memory:read` and `memory:write`; hosted writers then
carry both scopes. `retrieve` and `list_spaces` require read, while `remember`
requires write **before any replay or memory lookup**. No other forwarded scopes
are accepted. Route/client provisioning and live deployment are separate actions;
no route or identity is created by these commands.

For example, these are illustrative configuration values, not existing resources:

```sh
export MEMORY_DATABASE=/var/lib/artificial-memory/memory.db
export MEMORY_LISTEN=127.0.0.1:8080
export MEMORY_BACKEND_ORIGIN=http://artificial-memory.internal:8080
export MEMORY_GATEWAY_ISSUER=https://mcp.example.test
export MEMORY_AUDIENCE=artificial-memory-mcp
export MEMORY_IDENTITY_ISSUER=https://identity.example.test/realms/agents
export MEMORY_GATEWAY_JWKS_FILE=/etc/artificial-memory/gateway-public.jwks
export MEMORY_ALLOWED_CLIENT_IDS=coding-agent
```

The gateway strips the external Origin and identity headers and supplies its
signed bearer token and the fixed upstream Host. The backend compares `Host`
exactly with the configured backend origin. An absent `Origin` is allowed; a
present Origin must equal that exact origin and may occur only once. Forwarded
Host/user/issuer headers never change origin checks or ownership.

## Enroll or disable an account locally

Enrollment is explicit local administration, independent of the memory surface.
Obtain the original issuer and subject from the existing provider's verified
administration. Put only that subject in a private regular 0600 file, optionally
ending in one newline. Do not pass it as a command-line argument. The command
rejects symlinks, public files, malformed identities, and subjects over 255 bytes.

```sh
bin/artificial-memory admin enroll \
  --database "$MEMORY_DATABASE" \
  --issuer "$MEMORY_IDENTITY_ISSUER" \
  --subject-file /secure/original-subject.txt

bin/artificial-memory serve
```

Enrollment prints only the stable generated internal `account_id` and active
state. Repeating it, including after disabling, retains that ID and its memories.
An unknown but valid signed identity is never auto-enrolled. The same subject
under another issuer is another identity and cannot share ownership implicitly.
Current configuration trusts exactly one provider issuer.

To disable the same original identity:

```sh
bin/artificial-memory admin disable \
  --database "$MEMORY_DATABASE" \
  --issuer "$MEMORY_IDENTITY_ISSUER" \
  --subject-file /secure/original-subject.txt
```

Disable changes administrative state only; memories and stable identity remain.
Every HTTP request resolves active enrollment afresh. Requests already authorized
in flight may finish; subsequent requests fail. Clients whose SDK closes after
an authorization failure reconnect after re-enrollment. Local admin commands can
briefly open the same database for enrollment updates; run one memory service,
not multiple independently writing service replicas.

SQLite schema v2 adds account and enrolled-identity tables transactionally. A v1
journal upgrades without changing immutable records, FTS, replay results, space
tables, or its cursor key. V1 trusted adapter account IDs remain intact and do not
become provider enrollments automatically. Account reassignment, arbitrary SQL,
and automated legacy-owner rebinding are not supported administration commands.

## Verification and key rotation

Each request must carry exactly one `Authorization: Bearer` header containing a
compact RS256 JWS with protected `typ=mcp-gateway+jwt` and exact pinned `kid`.
The installed JWKS contains 1–8 distinct public RSA signing keys, each marked
`use=sig`, `alg=RS256`, with 2048–8192-bit modulus; maximum file size is 64 KiB.
Duplicate JSON fields, private keys and token-supplied key sources are rejected.

The verifier checks the exact gateway issuer, sole audience, configured original
issuer/nonempty subject, caller allowlist, allowed scopes, and positive integer
`iat`/`nbf`/`exp`. Both total lifetime and remaining lifetime are at most 60
seconds; only issued-at receives up to five seconds of future clock skew.
Not-before must already be valid and expiry must be in the future. Direct
provider access tokens and plain identity headers are rejected.

Only the gateway holds private signing material. To rotate, install old and new
public keys on all backends and restart, rotate the gateway signer, and retain
the old public key until all 60-second tokens and in-flight requests expire.
Emergency removal requires replacing pinned public keys and restarting every
backend. Keep clocks synchronized. Signing-key compromise can impersonate an
enrolled identity; protect gateway signing and route administration.

## Health, shutdown, limits and errors

`GET /health/live` reports a running process. `GET /health/ready` reads migrated
storage/settings with a one-second deadline, without writing memories. It reports
503 when storage is unavailable. Probes need no bearer token or canonical Host
and must remain on the private network. `/mcp` uses stateless Streamable HTTP with
JSON responses; GET/DELETE transport operations are unsupported. OAuth discovery
endpoints belong to the public gateway.

SIGINT/SIGTERM stops accepting requests and drains HTTP work for up to ten
seconds, then closes storage. Client cancellation and the 30-second request
deadline propagate to the journal for both modern and older SDK protocol
requests. Modern `server/discover` is supported; receiving middleware preserves
the original HTTP lifetime when older SDK session semantics detach cancellation.
Each stateless POST remains the complete operation lifetime.
The server limits headers to 32 KiB, request
bodies to 4 MiB, and concurrent authenticated requests to eight. Header/read/write/
idle timeouts are 5/15/35/60 seconds. Body limits apply to chunked requests too.
Storage's count and field bounds still apply; see [README](../README.md#bounds).

Tool responses are limited to 16 MiB including their text copy. `remember`
returns only a bounded receipt, while full records remain durable. Retrieve can
return `result_too_large`; start a fresh query without a cursor and with smaller
`limit`, `context_limit`, and `link_limit`. It returns no misleading partial page
in that case. Follow
`next_cursor` with the same query until complete. A span lookup's completeness
describes that lookup, not an entire topic; use its topic/root ID for history and
explicitly expand linked topics in their own selected spaces.

HTTP errors use fixed safe messages: invalid token 401, unknown/disabled account
or forbidden Origin 403, incorrect Host 421, malformed envelope 400, media type
415, body size 413, and unavailable/busy storage 503. Tool errors carry safe codes
such as `invalid_arguments`, `invalid_input`, `not_found`, `invalid_cursor`,
`idempotency_conflict`, `forbidden`, `storage_unavailable`, and `result_too_large`.
They do not reveal SQL, tokens, source text, keys, or private paths. Service logs
contain lifecycle events only. Preserve the same key/input/client after a write
timeout or lost acknowledgment; a successful commit may have preceded the error.

Stored sources/payloads remain untrusted assertions. Record only actual explicit
reasons and sources; use empty reasoning arrays or explicit unknown historical
time instead of inventing evidence. No tool input chooses account/client
ownership. Extra, duplicate, wrongly cased or null envelope fields and trailing
JSON are rejected. Omitted nondecision payload defaults to `{}`, omitted/zero
schema version defaults to 1, and nested payload nulls/arbitrary fields remain
data. Exact JSON numeric values survive MCP, persistence, retrieval and replay.

`make check test race build integration demo chart-test container-build container-test` exercises synthetic RSA identities,
two-owner isolation, operation scopes, topic/reuse reasoning, strict input and
time checks, exact large numbers, v1 migration, safe errors, large escaped-source
receipts, and the real command through graceful restart. It does not validate a
real Keycloak login, provision an identity, publish a route, or deploy anything.
The [operator runbook](RUNBOOK.md) covers the project-only pinned image, private
Helm chart and existing JWKS ConfigMap, storage permissions, supported consistent
SQLite backup/isolated restore and runnable synthetic example. Container tests
use the default nonroot UID and read-only root, reject anonymous/read-only writes,
and recreate the service on the same owned temporary volume. No signer or demo
package enters the production image. These are local synthetic checks; actual
gateway route, provider/client configuration, filesystem-driver behavior and
live deployment require their own operator verification.
