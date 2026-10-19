# Resume AgentShoppingList

Updated October 19, 2026, America/New_York.

## Current state

Project: /Users/oudavid/works/opensource/AgentShoppingList

**CP01/02/04/05 complete. CP07/08 local implementation independently Approved; CP03/06 live acceptance remains pending. Continue CP09 whole-process restart and CP10 local release/recovery preparation. V1 is not deployed or beta-ready.**

Continue the user's full deployable daily-use v1 goal after each local stage. Checkpoint boundaries are not stop requests. Preserve files/data and continue; do not recreate the app. No live infrastructure change is authorized by this local handoff.

Git: `main`, baseline `21d4f35` (shared shopping/mobile/MCP), previous `a3f8c76` (foundation/identity), no remote. Current CP07/08 changes are uncommitted. Migration6 extends existing purchase tables without resetting products/lists/accounts or inventing receipt evidence. The app still reports readiness503.

Go1.26.5 serves four compact template views, narrow browser API and protected stateless official SDK v1.8.0 MCP in one application. Authentication/per-owner scopes, durable encrypted sessions, audit and account-wide idempotency are preserved. No production data/fixture identity is seeded.

## Read in order

1. AGENTS.md, this file and CHECKPOINTS.md.
2. REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md.
3. docs/DOMAIN.md for current service/price/reconciliation/insights contracts.
4. docs/validation/CP07-CP08-2026-10-19.md and prior CP04–CP06 evidence.
5. docs/AUTH.md, docs/DECISIONS.md, docs/DEPENDENCIES.md and README.md.

## Next action

Continue CP09 whole-process/two-user web+MCP restart and interrupted retry verification, then CP10 local immutable container, real readiness, consistent backup/restore and deployment artifacts. Preserve all existing records and source evidence. Live infrastructure access still requires bounded authorization after local artifacts are reviewable.

Latest correction completed Builder → Translation → final validation → fresh independent Review. Purchase writes now persist explicit bounded outcomes; full source arrays remain on owner-scoped reads. Exact replay uses saved outcomes, never current mutable rows. See docs/validation/CP07-result-correction-2026-10-19.md.

## Implemented in this slice

- Sourced revisioned purchase drafts, confirmation, full void with reason, and atomic replacement supersede. Each line pins `expected_product_revision`; catalog drift before confirmation conflicts instead of changing receipt pack facts silently.
- Confirmed product/pack/category/source/price snapshots remain immutable after catalog edits, replacement or void. Source identity is unique per owner/retailer/source-kind/reference lineage, including retries and voids.
- Original confirmation atomically compares linked product/list/item revisions, preserves checked/removed evidence and reconciles partial quantities. Replacement returns original reconciliation evidence with `reconciliation_applied:false`; corrections and voids never repeat/reverse list changes. Any needed list adjustment is a separate explicit mutation. Partial refunds are deferred.
- Known unit price × quantity rounds half up before allocated discount. Merchandise line totals exclude tax; tax is separately known or null. Unknown values remain null, distinct from zero. No price is inferred from checkoff.
- History reads current confirmed records and sourced snapshots. Buy again uses the existing list-add service only for compatible pack counts/current variant facts; it allows description-only edits, hides changed packs/measured lines, and pins the current product revision against click-time drift.
- Insights derive solely from current confirmed snapshots: separate currencies, matching weekly/category merchandise basis, tax/missing counts, rational compatible-unit quantities, checked integer money sums and timezone/week-start/DST boundaries. Coverage is recorded purchases only; absent weeks are unknown. Human charts round quantity labels and say bought.
- All16 typed MCP tools are exposed; browser purchase/metric writes remain absent. Actual nonempty purchase/insight and confirmation/replacement/void audit outputs passed official SDK schema validation.
- Purchase audit events now retain complete editable draft inputs and compact immutable purchase/reconciliation references, counts and state transitions. Full catalog/source/price snapshots and reconciliation before/after evidence remain in their authoritative owner-qualified tables. Older full-Purchase audit JSON remains readable and accepted by the SDK output schema. The256KiB audit cap and50-line purchase limit are unchanged.

## Actual validation

- Full Modern Go Guidelines list for Go1.26 read before edits.
- `gofmt`, CSS rebuild, `node --check web/static/app.js`, project-cache `GOPROXY=off make check build` passed (format/vet/all tests/both binaries).
- Race suite passed: `go test -race ./internal/web ./internal/shopping ./internal/store/sqlite ./internal/identity ./cmd/shopping-admin`.
- Real loopback local OIDC/session + official SDK/browser tests executed agent Polar creation/list → browser sourced photo/check → no spend from checkoff → sourced confirmation → History/Insights once. Also scopes/two-user reads, atomic later-item rollback, partial remainder, source collision/concurrent confirmation, replacement changed quantity without list replay, immutable old/current snapshots, full void, reopen replay after later void, null/zero/discount/tax/currencies/category reconciliation, product drift and safe reorders,167/169hour DST weeks, unknown coverage, exact unit conversion and overflow failure.
- Coordinator built-in browser on final preview at360/390px: no horizontal overflow; human source/category labels; unknown-only versus knownzero; partial/coverage labels; weekly/category totals reconcile; rounded quantity and actual meter bars; History snapshots/source; Buy again increased Polar1→2 once. Screenshots are in the validation note.
- Initial schema-version assertion5→6 failure was corrected. Initial restricted httptest listener denial was resolved by scoped local escalation; final checks pass. No unresolved Builder validation failure.

Use `GOMODCACHE=$PWD/.cache/go-mod GOCACHE=$PWD/.cache/go-build`. Loopback tests may require scoped local escalation. No network research runs inside purchase transactions.

Correction validation executed: exact reviewer reproduction failed before the fix (`audit summary is too large` after50-line original confirmation), then passed unchanged after it. Durable maximum ASCII/HTML-escaped/Unicode cases exercise50 linked lines,300/150/150-byte name/brand/flavor,2048-byte catalog/receipt URLs,2000-byte receipt evidence,200-byte receipt/line references,128-byte retailer,200-byte location and1000-byte void reason. Largest audit60428 bytes for ASCII/Unicode and207706 for HTML escaping, below262144. Draft revisions, stale replacement/no reserved key, replacement/void/replay with byte-identical reconciled list state, complete immutable sources, draft before/after evidence and historical SDK activity all passed. `make check build` and race suites for web/shopping/SQLite passed. No post-Translation validation or new independent verdict yet.

## Processes and authorization

No application or fixture preview is running. Original Builder stopped preview76489 after the prior review; correction Builder again verified `lsof -nP -iTCP:8092 -sTCP:LISTEN` returns no listener. Local test-only OIDC/SDK listeners close when their tests finish. No production application listener was started for this correction.

User chose `https://shopping.vvivid.dev`, own-account-only initial enrollment, Codex/ChatGPT, and own preferences America/New_York, USD, Walmart, Monday. These are not global defaults and no live account is enrolled. Exact subject/registrations/callbacks, live issuer/resource/proxy compatibility, optional background consent, remote repository and recovery targets remain pending. October18 Keycloak observations are historical.

Automatic approval review rejected an earlier production SSHMCP connection as outside local authorization; it was not retried or bypassed. Local artifacts/tests are authorized. Live identity/Traefik/DNS/proxy/enrollment/deployment needs the separately recorded bounded authorization. No live infrastructure, OAuth registration, secret or deployment changes occurred.

## Latest independent review — Denied

P2: a supported50-line linked purchase replacement exceeded the256KiB audit summary cap because ConfirmPurchase stored four full snapshots and repeated reconciliation evidence. Existing suites passed; reviewer isolated overlay reproduced failure. Evidence: CHECKPOINTS.md and docs/validation/CP07-review-boundary-reproduction.go.txt. The user subsequently authorized correction. Builder has fixed and tested the audit projection; this historical Denied verdict remains the latest independent verdict until the fresh workflow completes.

Next: Translation, final validation and fresh Review of the corrected code; then resume CP09/10 local release/recovery preparation. Current CP07/08 changes are uncommitted over21d4f35. Docker Desktop29.8.2 was started locally earlier; no application container or live deployment. Live CP03/06/10 acceptance still pending and prior automatic rejection of production shell access remains in effect.

Pause cleanup: no correction processes remain, and8092 has no listener. No live infrastructure, deployment, migration or data reset occurred.

## Latest result-boundary review — Denied

The prior256KiB audit defect is corrected and independently verified. A new P2 blocks supported50-product purchases with12 source URLs of2048 bytes each: SavePurchase full source snapshots exceed Store.Write1MiB persisted-result cap.30-line independent case804145 bytes passed;50-line case fails. Evidence: docs/validation/CP07-result-boundary-reproduction.go.txt. Current source/replay shape must be aligned without discarding immutable evidence or shrinking50-line support just to pass.

That review stopped the earlier workflow for the user's next instruction. The authorized result correction now returns/persists explicit `shopping.PurchaseOutcome` records for save/confirm/void. Receipt/header facts, line identity/package/quantity/price snapshots and full reconciliation remain in outcomes; per-line `sources_snapshot` is available through full owner-authorized `get_purchase`/browser reads. No cap or supported50-line/12-source limit changed; replay reads the original persisted outcome, including historical full-record JSON, instead of current mutable rows.

New actual Builder evidence: unchanged independent reproduction now passes;50 distinct products with12 unique2048-byte sources each pass all purchase writes/full reads through official SDK for ASCII/escaped/Unicode data. Largest persisted outcomes118131/412781 bytes; full immutable records1430181/7763831 bytes. Conservative escaped128-byte-ID outcome bound874618 bytes is below1MiB. All six original attempts replay exactly after database/identity-manager/handler reopen through fresh SDK clients; draft edits, catalog drift, supersede, void, foreign ownership and write-only/read scopes are covered. Maximally escaped product creation is staged through the service because it exceeds the existing128KiB MCP request-body cap; ASCII/Unicode product creation and every purchase operation use SDK. This is not whole-process/container or actual Codex/ChatGPT acceptance.

`GOPROXY=off make check build`, relevant web/shopping/SQLite race suite, targeted reproduction/new regressions and formatting/diff checks passed Builder validation using project caches. Post-Translation final coordinator checks/Review remain required. All current work is preserved uncommitted over21d4f35. No application listeners or live changes; release/container/recovery and live acceptance remain incomplete.

Result correction Translation completed: coordinator verified `/root/purchase_result_translation`, host `turn_context` session `01a152bd-b497-7670-b514-f27f1de06bbf`, as gpt-6.1-sol/xhigh. Descriptive outcome locals, explicit field mappings and expanded conservative fixture preserve behavior. Post-edit bound test (874618 bytes), source-rich durable SDK/reopen/legacy tests and audit regressions pass; initial restricted loopback bind was resolved by scoped local escalation. No correctness/readability blocker was found in this correction's scope. Final coordinator checks/build and fresh independent Review remain required; CP06/07/08 stay In progress. See docs/validation/CP07-result-correction-2026-10-19.md for actual normal/failure traces and validation.

## Latest local verdict — Approved

Fresh read-only Review /root/purchase_result_review found no blocking findings and independently reran original reproduction, maximum-source SDK/reopen/legacy/audit regressions and web/shopping/SQLite suites. Coordinator final make fmt assets check build, JavaScript parsing and diff checks passed after Translation. Largest actual outcome412781 bytes; conservative escaped bound874618 below unchanged1MiB. Full source-bearing evidence retained. This supersedes the historical Denied entries above for the local code only.

Runtime verified from host turn_context: Builder /root/purchase_result_builder session01a152b6-c50c-75b0-b777-fec93e3f7b0c gpt-6.1-sol/max; Translation /root/purchase_result_translation session01a152bd-b497-7670-b514-f27f1de06bbf gpt-6.1-sol/xhigh; Review /root/purchase_result_review session01a152bf-e1f8-7aa3-8b12-f7ec984bb030 gpt-6.1-sol/max. No application listeners or live infrastructure changes.
