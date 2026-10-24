# User requirements

## Intended use

The app is mainly managed by an agent through MCP. The user opens a ready shopping list when planning or having an opportunity to shop, mainly at Walmart. The UI should demand very little typing or management.

## Product requirements

| ID | Requirement | Acceptance checkpoint |
| --- | --- | --- |
| R01 | Mobile-first, compact UI. No sidebar/dashboard/stat-card landing page or descriptive marketing. | CP05, CP09 |
| R02 | My list is the default screen. Four small tabs: My list, Products, History, Insights. | CP05 |
| R03 | List rows show category icon, name, pack, quantity and checkbox. | CP04, CP05 |
| R04 | Tap an item to see a recognizable product image, description, exact brand/flavor/pack, nutrition and source/retailer link. | CP04, CP05 |
| R05 | Use predefined category icons; a cherry-flavored drink is a drink, not fruit. | CP04, CP05 |
| R06 | Search saved products and add them to the list; agents maintain product records. | CP04, CP05, CP06 |
| R07 | Preserve Walmart preference per account; retailer context does not imply local availability. | CP02, CP04 |
| R08 | Show purchase history by date and store with purchased items; Buy again adds products without accidental duplicate requests. | CP07 |
| R09 | Show weekly spending, category spending, and comparable quantity trends from purchase records. | CP08 |
| R10 | Labels distinguish purchases from consumption; unknown facts stay unknown. | CP07, CP08 |
| R11 | The web can read personal data and primarily mutate list items; MCP manages the shopping domain. | CP03–CP09 |
| R12 | Multi-user from the start: private data and authorization per account. | CP02, CP03, CP09 |
| R13 | Reuse existing Keycloak with OAuth authorization code + PKCE and separate shopping clients/scopes/audiences. | CP03, CP06, CP10 |
| R14 | Go + templates + SQLite; small JS only for useful interactions. | CP01, CP05 |
| R15 | One Go service exposes web, API and MCP, sharing business services. The approved optional shared MCPGateway may front MCP access while Shopping retains its tools and authorization. | CP01, CP06, CP10 |
| R16 | Deploy behind existing Traefik; keep shopping data and runtime secrets on the server, persist SQLite across restart, and preserve exact retries. Do not create backups, snapshots, off-server copies, backup schedules or Mac recovery keys. | CP09, CP10 |
| R17 | Preserve progress in files and resumable checkpoints so a new chat can continue. | All checkpoints |
| R18 | CP00 preserves docs and reference assets; development continues from the first incomplete checkpoint without recreating the project. | CP00, all resume work |
| R19 | Explicitly select list items and share a separate public checklist through a five-letter link for exactly six hours; preserve private owner data, checkoff, purchases and derived metrics. | Temporary sharing slice |

## Agreed reference example

Polar Black Cherry Seltzer, 8 × 12 fl oz cans, listed at https://www.walmart.com/ip/187251201. It uses the Drinks icon. The detail view has the actual package image, concise description and sourced nutrition. This is a recognition example, not proof that the user purchased it or that a local store has stock.

## Scope decisions proposed in the spec

- V1 has private per-user accounts; shared households are deferred. Explicit temporary item links are an approved browser-only exception, with selected immutable snapshots and separate guest checkoff. See docs/SHARING.md.
- Product pack/flavor variants are distinct.
- Agents supply source-backed data; the server validates it.
- Checkoff records picked-up intent. Purchase confirmation requires receipt data or user confirmation; no estimated price silently enters actual spending.
- Backend metrics are predefined computations. Agents maintain source records and preferences.
- Server-side web sessions, optimistic concurrency, idempotent mutations, audit records and confirmed purchase snapshots are required.
- No in-app AI chat or continuously running agent is required.
- The user's October 19 server-only, no-backup policy supersedes the earlier backup, restore-drill, retention and recovery-key requirements. Migration startup must not create archives; failed migrations retain transactional rollback and history validation.

## Deferred

Stock-management UI and stock domain, budgets, meal plans, consumption logging, arbitrary custom metrics, shared households, automated Walmart account imports, checkout/purchasing, offline mutation queues, and multi-server scaling.

These deferrals keep the accepted small UI. They are not assertions that future agents can never support them.
