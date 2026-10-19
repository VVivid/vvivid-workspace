# AgentShoppingList specification

Draft specification for a mobile shopping app managed primarily by AI agents through MCP. The user opens a ready shopping list, identifies products, and checks items while shopping. The agent maintains products, lists, purchase records, and preferences. The server derives metrics from purchase records.

Status: October 19, 2026. CP01/02 contain a Go shell and tenant-safe SQLite. CP03 local identity Builder checks pass; review/live OAuth integration remain. Domain workflows and beta acceptance are pending. See CHECKPOINTS.md for evidence.

# Product behavior

The mobile app opens My list by default. Four compact tabs remain: My list, Products, History, and Insights. No management dashboard or chat interface is required.

- My list: show category icon, product name, pack size, requested quantity, and checkbox. Tap the product to see its photo, description, nutrition, and retailer link.

- Products: search saved products and add an existing product to the active list.

- History: browse purchases by date and store, inspect purchased items, and add items back to the list.

- Insights: show weekly spending, spending by category, and comparable purchase quantities over time.

Walmart is the initial preferred retailer, configurable per user. A list can contain retailer-specific items; the default view includes Walmart and items without a retailer restriction. An unavailable or unchecked retailer listing must not appear as confirmed local stock.

Category icons come from a fixed application mapping. The agent assigns a category; it does not supply arbitrary icon code. Photos identify the exact brand, flavor, and pack in the details view.

Example: Polar Black Cherry Seltzer uses the Drinks icon. Its details identify Polar, Black Cherry, and 8 × 12 fl oz cans, with a sourced photo and Walmart listing. Different flavors and pack sizes are separate product variants.

# System boundaries

One application backend exposes two authenticated interfaces over shared domain services and storage:

| Interface | Primary caller | Allowed work |
| --- | --- | --- |
| Web API under /api/v1 | Mobile browser | Read personal data; add, remove, resize, and check list items |
| MCP at /mcp using Streamable HTTP | Authorized AI clients | Manage the authenticated user's products, lists, purchase records, and preferences |
| Identity provider | Browser and MCP OAuth clients | Existing Keycloak handles sign-in and token issuance |

MCP tools call the same domain services as the Web API. They do not automate the UI or bypass validation through direct database access.

“Manage everything” means the user's shopping domain. It does not grant Keycloak administration, server access, cross-user access, arbitrary code execution, or arbitrary database queries.

# Implementation stack and infrastructure

The selected stack is Go, html/template, Tailwind CSS, a small JavaScript layer, and SQLite. One Go application serves mobile pages, the Web API, and the built-in MCP endpoint. No separate MCP gateway is deployed.

Go templates own reusable page components. Preserve the accepted mobile design; the React/Radix reference demo is a visual and interaction reference, not the production frontend. Recreate equivalent accessible controls in semantic HTML, CSS, and small JavaScript. This is shadcn-inspired styling, not direct use of React shadcn components. Product dialogs need keyboard, focus, and screen-reader behavior.

Use the official Go MCP SDK. Web handlers and MCP tools call shared application services with authenticated owner context. Neither interface bypasses ownership, validation, idempotency, or transaction rules.

Deployment starts with one Go application container on the existing personal server behind existing Traefik HTTPS routing. Reuse the deployed Keycloak instance; create separate shopping clients, scopes, and audience identifiers. The application validates browser sessions and MCP tokens. The existing SSHMCP remains a separate service and is not on the shopping tool execution path.

Illustrative routes, with final host still to be selected: / for pages, /api/v1 for browser JSON endpoints, /mcp for Streamable HTTP, /auth for web sign-in callbacks/session management, and OAuth protected-resource discovery metadata at its standards-compliant location. Traefik must forward required MCP headers and streams without inappropriate buffering or timeouts. Prefer stateless MCP request handling unless selected client requirements demand sessions; any sessions must be bound to their authenticated owner and client.

SQLite runs in process with a persistent local data directory such as /data. Enable foreign keys on every connection, use WAL, bounded busy handling, short transactions, and a controlled write connection/pool. Keep network calls outside transactions. SQLite has no PostgreSQL-style row-level security: enforce owner predicates in repositories, composite ownership constraints, services, and tests.

Persist user and web-session data and idempotency outcomes across process restarts. Use SQLite online backup or an equivalent consistent snapshot, including assets if managed locally; do not copy only a live WAL database main file. Back up off server and prove restoration. Gracefully shut down, finish or roll back writes, and close database connections.

Browser behavior needs minimal JavaScript for checkboxes, product details, search, and refreshing on page visibility/focus. Poll lightly while the list is visible; no WebSocket system is required. Show unsaved/error state on failed mutations. V1 does not promise offline editing.

Build Go binary, CSS, and embedded templates/assets into an immutable image. Tailwind tooling is build-time only; no Node frontend server is required in production. Include migrations, readiness/liveness endpoints, request IDs, structured logs without credentials, and existing observability integration where appropriate. No new Redis, queue broker, separate frontend service, or AI runtime is required for v1.

An external agent or scheduler initiates MCP calls. The shopping server is not an autonomous continuously running agent.

Move to PostgreSQL or another topology only if measured write contention, multiple application instances, or availability requirements justify it. Multi-user functionality is required immediately; horizontal scaling is not.

References: [SQLite deployment guidance](https://sqlite.org/whentouse.html), [SQLite WAL](https://sqlite.org/wal.html), [Go HTML templates](https://pkg.go.dev/html/template), [official Go MCP SDK](https://go.sdk.modelcontextprotocol.io/server/).

# Multi-user ownership

V1 has independent private accounts. Shared households and cross-user editing are deferred.

- Identify an external account by the verified pair of issuer and subject, mapped to an internal user ID. Email is display/contact information, not an ownership key.

- Derive the acting user from the authenticated session or access token. No tool or request may choose another owner through a user_id argument.

- Every personal record, media reference, query, cache entry, import job, and audit event is scoped to its owner. Relationships must also stay within the same owner.

- Enforce ownership in domain services, owner-scoped SQLite repository queries, and composite ownership constraints. SQLite does not supply PostgreSQL-style row-level security; cross-user isolation must be demonstrated by tests.

- Resource IDs alone never authorize access. Foreign IDs return a non-disclosing not-found result.

- V1 catalogs are private per user. Global mutable product records are deferred.

- Logout/account switching clears local cached data. Responses containing personal data must not enter shared caches.

- App enrollment is explicitly controlled; a successful Keycloak login alone must not enroll every existing realm user automatically.

The same user can connect multiple AI clients. Each acts under that user's grant and records its own client identity.

# Core records

| Record | Required information |
| --- | --- |
| Account preferences | Owner, timezone, default currency, preferred retailer, week start |
| Product variant | Owner, stable ID, name, brand, flavor, category, package count and size, description, archive state, revision |
| Product media and nutrition | Image URL or owned asset, source URL, observation time; nutrition values, units, serving basis, verification status |
| Retailer listing | Product ID, retailer, product URL and retailer item ID; optional store/location and price observation with currency and checked time |
| Shopping list | Owner, ID, title, retailer context, active or archived state, revision |
| List item | List and product IDs, requested quantity, picked-up state, revision, timestamps and actors |
| Purchase | Owner, store, purchased time, currency, draft/confirmed/void state, external receipt reference, revision |
| Purchase line | Immutable-at-confirmation product snapshot, category, quantity and unit, package size, price, discounts, tax treatment, source list item |
| Audit event | Owner, actor subject and client, operation, affected IDs, request ID, time, change summary |

Purchase snapshots preserve the name, category, package size, and prices used at purchase time. Updating a saved product must not rewrite history.

Store monetary values as integer minor units with currency. Unknown price is null, never zero. Store measured quantities as decimal values with explicit units. Only comparable units can be aggregated.

Nutrition requires a serving basis such as per can, per 100 ml, or per 100 g. Missing facts remain unknown. Record field provenance so facts from different sources are distinguishable.

# Web API contract

All routes operate on the authenticated account. Read collections use bounded cursor pagination. The active-list response includes enough product summary data to render without one request per row.

| Method and path | Purpose |
| --- | --- |
| GET /me | Account display information and preferences |
| GET /lists/active?retailer=... | Ready list with product summaries |
| GET /products?q=...&cursor=... | Search saved products |
| GET /products/{product_id} | Product photo, description, package, nutrition, sources |
| POST /lists/{list_id}/items | Add a saved product with an explicit quantity and merge policy |
| PATCH /lists/{list_id}/items/{item_id} | Set quantity or picked-up state |
| DELETE /lists/{list_id}/items/{item_id} | Soft-remove an item while preserving its audit trail |
| GET /purchases?from=...&to=...&cursor=... | Purchase history |
| GET /purchases/{purchase_id} | Purchase details |
| GET /insights?from=...&to=...&interval=week | Derived spending and quantity series |

“Buy again” uses the list-add operation. The UI has no direct product, nutrition, price, purchase-history, or metric write endpoints.

Mutation requests carry an idempotency key and expected revision where applicable. Set checked=true/false rather than issuing a toggle. Specify whether adding a matching product increments quantity or leaves the existing quantity unchanged. Replaying a successful request must not increment again.

Return authoritative updated records and revisions. Use 409 for stale revisions and explain which record changed; the browser refreshes rather than silently overwriting agent edits.

# MCP tools

Tools have typed input and output schemas, explicit field validation, bounded results, and documented side effects. No generic execute, SQL, arbitrary URL fetch, or unrestricted JSON patch tool is exposed.

| Tool | Behavior | Required scope |
| --- | --- | --- |
| get_context | Return current account preferences, allowed categories and capabilities | shopping:read |
| search_products | Search the user's saved catalog | shopping:read |
| get_product | Read a full product and sources | shopping:read |
| save_product | Create or revise a product variant, nutrition, image, and retailer listing | shopping:products:write |
| archive_product | Archive or restore a product without deleting purchase history | shopping:products:write |
| get_list | Read active or specified list and revision | shopping:read |
| save_list | Create a list or revise title, retailer context, or archive state | shopping:lists:write |
| update_list_items | Apply a bounded atomic batch of add, set quantity, set checked, remove, or restore operations | shopping:lists:write |
| search_purchases | Find purchase records by date, retailer, product, or source reference | shopping:read |
| get_purchase | Read a purchase and its lines | shopping:read |
| save_purchase | Create or correct a draft with source evidence and line-level prices | shopping:purchases:write |
| confirm_purchase | Validate and confirm a draft; reconcile linked list items atomically | shopping:purchases:write |
| void_purchase | Void a confirmed purchase with a reason, preserving audit evidence | shopping:purchases:write |
| get_insights | Return computed metrics and their definitions and completeness | shopping:read |
| update_preferences | Update timezone, currency, retailer, and week start | shopping:preferences:write |
| get_activity | Inspect the user's domain changes for reconciliation | shopping:read |

A product update returns its current ID and revision. Creation supports a stable external source key to prevent duplicates. All writes include a request ID, idempotency key, and relevant expected revisions. Atomic batches either all apply or none apply.

Confirmed purchase corrections use a replacement draft plus an atomic supersede operation in confirm_purchase. The previous version remains auditable and only the current confirmed version contributes to metrics.

Read and write scopes are separate; a write scope does not implicitly grant reading. An agent normally receives shopping:read plus the specific write scopes the user grants. Browser clients cannot request product or purchase write scopes.

The agent finds retailer information using its own authorized research capabilities, then submits sourced structured data. V1 MCP stores and validates that data; it does not log into Walmart, import orders automatically, schedule its own work, or place purchases.

# Shopping and purchase lifecycle

Checking an item records picked-up intent immediately and durably. It does not establish a receipt price or create confirmed spending.

1. The agent maintains the active list.

2. The user opens the list at Walmart, views a product if needed, and checks it.

3. The agent reads picked-up items and records a draft purchase using a receipt or the user's confirmation.

4. confirm_purchase validates the purchase and commits it once.

5. Confirmed purchased quantities reconcile linked list items in the same transaction. Partial purchases leave the remaining quantity on the list.

Only reconcile items at the expected revisions. If the user changed an item meanwhile, return a conflict without partially confirming or clearing it.

Removing or archiving a checked item preserves the picked-up event for later reconciliation. A future Clear checked action must not silently erase that evidence.

A receipt identifier is unique per owner and retailer/source when available. Repeated imports and retried confirmation must not double-count. Purchases without known prices may still be confirmed as purchases, but their missing amounts remain explicit in insights.

# Insights rules

Metrics are computed by server-owned definitions from current confirmed purchase records. The agent maintains source data; it cannot write chart totals or submit executable metric formulas.

Initial metrics:

- Weekly spend: known merchandise line totals after allocated discounts. Show receipt tax and other charges separately when available.

- Category spend: the same merchandise basis grouped by the category snapshot. Categories reconcile with the included merchandise total.

- Purchase quantity trends: units, packs, weight, or volume for a product or compatible category, such as liters of seltzer bought per week.

Use the account timezone and week-start setting. Compare the current partial week only with equivalent elapsed periods, or label an explicit completed-week comparison. Return date boundaries, currency, basis, and completeness with each metric.

Unknown prices or quantities are excluded only from affected calculations and accompanied by missing-data counts. Never show an incomplete spend amount as a complete total. Missing historical coverage is not a zero-purchase week.

Do not combine currencies without an explicit exchange-rate policy. Do not combine packs of different sizes as comparable volumes unless package measurements are known.

Label quantity charts “bought” or “purchased.” Purchase history does not prove food or drink consumption.

# Authentication using existing Keycloak

Read-only server inspection on October 18, 2026 confirmed Keycloak 26.8.1 and live discovery for issuer https://ssh-mcp.vvivid.dev/realms/sshmcp. Existing Codex and ChatGPT clients have authorization code flow enabled, PKCE S256 enforced, and password grants and service accounts disabled. The ChatGPT client requires consent.

The current protected-resource metadata names https://ssh-mcp.vvivid.dev/mcp and scope sshmcp:admin. The deployment realm file contains SSH-specific audience mappings. These are existing SSH settings, not shopping permissions.

Proposed integration:

- Reuse the Keycloak instance and existing issuer initially. Create distinct shopping web, shopping Codex, and shopping ChatGPT client registrations with exact permitted callbacks and shopping-only scope assignments.

- Give the shopping Web API and MCP endpoint distinct audience identifiers. Shopping tokens must not grant SSH access, and SSH tokens must not authorize shopping operations.

- Use authorization code flow with PKCE S256. MCP uses bearer access tokens over HTTPS; each request is authenticated. Verify signature, exact issuer, intended audience, expiry, and required scopes. ID tokens are not API access tokens.

- Publish MCP protected-resource metadata and appropriate authentication challenges. Discover Keycloak endpoints from its OIDC metadata. These requirements follow the current [MCP authorization specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).

- The same Go application handles web login and a same-origin server-side session. Keep OAuth tokens server-side, use secure HttpOnly session cookies with an appropriate SameSite policy, protect mutations against CSRF, and restrict origins.

- Handle login, refresh, revocation, and session termination through Keycloak. Its [OIDC documentation](https://www.keycloak.org/securing-apps/oidc-layers) describes discovery, token, signing-key, and revocation endpoints.

- For unattended agents, use a per-user delegated refresh/offline grant only when supported and explicitly consented to. Do not use one shared service-account identity for all users. Define a revocation propagation target before release.

Integration prerequisite: deployed source includes an authorization proxy with SSH-specific client matching, a configured resource guard, and special refresh-resource handling. Extend this deliberately with an explicit client-to-allowed-resource mapping, or select an isolated shopping authorization route. Do not assume registering a shopping client alone is sufficient. Preserve existing SSH behavior.

Before implementation acceptance, test shopping authorization, PKCE, audience binding, scope issuance, refresh resource handling, and client registration compatibility against the deployed version and selected MCP clients. Client registration strategy starts with explicit pre-registration; broader discovery-based registration support is deferred.

# Data integrity and operational requirements

Browser and agent edits may happen simultaneously. Use optimistic concurrency, atomic transactions, and tenant-scoped idempotency records. Same key plus different payload must fail; repeat identical requests return their original result.

Audit changes with user, OAuth client, operation, affected records, and request ID. Never log access tokens, refresh tokens, secrets, or receipt contents unnecessarily.

Product descriptions and imported retailer text are data, not agent instructions. Validate external links and image URLs. If the server later fetches external media, allow HTTPS and defend against private-network access and unsafe redirects. Never render imported HTML directly.

Archived products remain resolvable in history. Metric recomputation follows confirmed record corrections. Background jobs preserve owner identity and original authorization context; a job ID does not establish ownership.

Connection loss must never display an unsaved checkbox as synced. V1 may require connectivity; durable offline mutation queues are a later decision.

# V1 acceptance criteria

- Two users with the same product names cannot read or modify one another's records through either interface, guessed IDs, jobs, or caches.

- The browser can read products, history, and insights and edit list items, but cannot edit products, purchase records, or metric definitions.

- A scoped agent can manage its user's domain records without granting identity or infrastructure administration.

- A shopping token is rejected by SSHMCP and an SSH token is rejected by the shopping service.

- Wrong issuer, audience, expiry, scopes, PKCE verifier, or refresh target fails closed. Signing-key rotation and grant revocation behavior are verified.

- Repeated list updates, receipt imports, and purchase confirmation are idempotent.

- Concurrent browser and agent edits produce recoverable conflicts, with no silent loss.

- Checking Polar shows picked-up state without inventing a price. Confirming a sourced purchase updates History and Insights once.

- Changing a product description or pack size does not rewrite past purchase snapshots.

- Partial purchases preserve remaining list quantities. Missing prices, refunds/corrections, units, timezone boundaries, and partial weeks are represented accurately.

# Decisions before implementation

1. Choose the shopping origin, API and MCP audience identifiers, and client callback URLs.

2. Confirm first MCP clients and delegated background-access needs; Codex and ChatGPT are the proposed initial targets.

3. Choose app enrollment policy, default timezone/currency, and how receipts or user confirmations reach the agent.

4. Pin the Go version, official MCP SDK, SQLite driver, Tailwind build tooling, and migration tool during CP01. Go + html/template + SQLite with one app behind existing Traefik and Keycloak is already selected; no separate MCP gateway.

5. Keep stock tracking, shared households, automated Walmart imports, custom metrics, and offline writes out of v1 unless explicitly added.

# Development checkpoints and resume

The canonical project is /Users/oudavid/works/opensource/AgentShoppingList. Local SPEC.md, REQUIREMENTS.md, ARCHITECTURE.md, CHECKPOINTS.md, and RESUME.md are the development handoff. The Page is a reference mirror; update it deliberately when project requirements change.

CP00–CP02 are complete. CP03 local identity implementation has passed Builder, Translation and final validation; independent review and live Keycloak/phone/client acceptance remain pending. CP04 product/list services, CP05 mobile templates/API, CP06 MCP, CP07 purchases, CP08 insights, CP09 end-to-end/restart and CP10 deployment/recovery remain unimplemented. CHECKPOINTS.md records authoritative evidence.

At a pause or new-chat handoff, update CHECKPOINTS.md and RESUME.md with completed work, remaining work, actual validation results, failures, relevant commit IDs if Git exists, and the next concrete action. Mark a checkpoint complete only after its exit criteria pass. Partial work remains in progress; resuming verifies the current files and continues unfinished work rather than restarting or deleting state.

Local development has started at CP01 under the development chat's authorization. The original planning handoff did not authorize changes to live Keycloak/Traefik; live integration and deployment remain a separate scope.
