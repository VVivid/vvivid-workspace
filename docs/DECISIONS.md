# Decisions and open details

## Selected by the user

- Project: /Users/oudavid/works/opensource/AgentShoppingList.
- Go + templates + SQLite.
- One application contains website, API and MCP.
- Reuse the existing Keycloak deployment.
- No separate MCP gateway.
- Mobile-first minimal interface, primarily read/check list.
- Agent-managed products, shopping data and metrics derived from data.
- History and Insights tabs.
- Predefined category icons, with photos/descriptions in item details.
- Durable docs/checkpoints for a new development chat.

## Implementation recommendations adopted in the draft

- html/template, Tailwind styling, minimal JS and server-rendered charts.
- One application instance with persistent local SQLite, WAL and controlled writes.
- Existing Traefik HTTPS routing.
- Same-origin persistent server-side web sessions.
- Official Go MCP SDK and scoped remote bearer authorization.
- Shared services; owner-scoped data from day one.
- Fixed server-derived metrics, source-backed fields and immutable purchase snapshots.
- Reference demo remains React only as a design artifact.

## Still to resolve

| Detail | Needed by | Current position |
| --- | --- | --- |
| Repository destination | Before publishing source | Local module `agentshoppinglist`; Git initialized on `main`; no remote selected |
| Public app hostname and API/MCP audiences | CP03 | User selected https://shopping.vvivid.dev and exact /api/v1 and /mcp audiences; not deployed |
| OAuth callbacks and client registration IDs | CP03 | Separate shopping clients required; names not provisioned |
| OAuth proxy extension vs isolated route | CP03 | Reuse same Keycloak; preserve SSH behavior |
| App enrollment | CP03 | Own account only initially, local operator CLI; exact subject/preferences pending |
| First agent clients and offline grants | CP03/06 | Codex and ChatGPT selected; registrations/callbacks pending; offline consent unselected |
| Account timezone/currency/week start | CP03 enrollment, CP08 | CP02 requires explicit validated per-user inputs; no inferred global defaults |
| Receipt/user-confirmation ingestion | CP07 | Agent supplies structured data; file upload/import pipeline not selected |
| Partial refunds | CP07 | Define explicit return/adjustment model before supporting them; voiding a full purchase is not a partial refund |
| Backup destination/retention/recovery targets | CP10 | Off-server consistent backup and tested restore required |

Do not block local CP01 on deployment details. Resolve each choice before its dependent implementation. Record new decisions here with rationale and update SPEC.md if behavior changes.

## CP01 implementation choices, October 19, 2026

- Pin installed Go 1.26.5; use local module `agentshoppinglist` until a repository destination is selected.
- Pin Tailwind CSS/CLI 4.3.0 in npm manifests to avoid the vulnerable watcher dependency chain observed in 4.3.3. Scan production templates explicitly; embed the generated CSS and Go templates. The reference React app remains isolated.
- Select official MCP Go SDK v1.8.0 for CP06 and pure-Go `modernc.org/sqlite` v1.60.1 for CP02. Record exact future dependency pins in [DEPENDENCIES.md](DEPENDENCIES.md); CP01 does not invent uses for unfinished adapters.
- Use application-owned embedded numbered SQL migrations with transactional version/checksum bookkeeping in CP02. No standalone migration service or extra runtime dependency is needed.
- Until persistence and identity are implemented, serve only a public signed-out shell and return 503 from readiness. No runtime user header, test identity, or personal-data fixture is introduced.

## Deferred scope

Stock UI/domain, shared households, automatic Walmart login/order import, arbitrary custom metrics, checkout, offline mutation queues, multi-server topology.

## CP02 persistence choices

- Private WAL file/FULL synchronous mode; one immediate writer/four read-only connections; per-connection FK/busy configuration. No pool/raw SQL handle exposed outside the store.
- Separate explicit provisioning from issuer+subject resolution. No runtime identity/enrollment route at CP02. Timezone/currency/retailer/week start are explicit inputs; timezone data is embedded.
- Category slugs: produce, meat, drinks, pantry, dairy, other. CP04/05 map these predefined values to icons; no arbitrary icon code.
- Successful idempotency keys use one per-account namespace, bound to operation/exact canonical semantic JSON. Result/audit/domain writes commit together. Failed requests do not reserve keys; no expiry policy yet.
- Initial insert/read primitives exercise storage. Merge, receipt uniqueness, reconciliation and confirmation/correction workflows remain at CP04/CP07.

## CP03 local identity choices

- Optional complete identity bundle; partial config/discovery failure fails startup. No runtime fixture identity. Readiness503 until live/domain acceptance.
- Confidential web code+PKCE S256, one-use persisted encrypted state/nonce/verifier, 12hour durable AES-GCM sessions, external stable keys, host-only secure cookies, CSRF and exact Origin.
- Exact distinct API/MCP resources and clients; RS256 Keycloak access JWTs enforce issuer/audience/type/time/scope. Browser read/list permissions only. Shared verified Principal resolves active enrolled owners.
- Operator enroll/disable CLI, no HTTP/MCP enrollment. Browser refresh rechecks grants at most60seconds apart on requests; bearer revocation bound300seconds. Confirm actual Keycloak behavior before release.
- Prepare isolated shopping client/resource bindings without changing the SSH proxy speculatively. JSON fragments are not a realm import. See AUTH.md.
