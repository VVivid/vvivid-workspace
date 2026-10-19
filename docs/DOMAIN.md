# Local shopping contract

CP07/CP08 Builder implementation, October19, 2026, based on independently Approved CP04/05 and prior MCP tools. Builder checks pass; Translation, final coordinator validation and independent Review remain for this slice. Actual Codex/ChatGPT OAuth and deployed acceptance remain pending. The app is not beta-ready. No test account, sample product or purchase is seeded by a production command.

## Reading order

- `internal/shopping/types.go`: typed inputs/results. `service.go`: verified Principal and scope checks. `validation.go`: source/package/quantity and operation rules. `write.go`: canonical semantics, audit and idempotency.
- `internal/store/sqlite/products.go`, `lists.go`, `mutation.go`, migration0005: owner-qualified records and short atomic transactions.
- `internal/web/handler.go`, `api.go`, `web/templates`, `web/static/app.js`: authenticated templates, narrow browser API and server-authoritative interactions.
- `internal/shopping/purchases.go`, `insights.go`; `internal/store/sqlite/purchases.go`, `purchase_types.go`, `insights.go`, migration0006: source claims, pinned immutable snapshots, reconciliation/corrections and recorded-purchase calculations.
- `internal/mcp/handler.go`:16 typed official-SDK tools, bounded input, stateless HTTP and private cache policy.
- `internal/web/domain_integration_test.go`: real local OIDC/session + browser + SDK protocol scenarios. Fixtures are synthetic, not retailer or purchase evidence.

## Shared rules

Every service call requires a verified Principal. Each service checks its own scope and re-resolves active enrollment; callers cannot provide an owner, subject or OAuth client override. Read and write scopes remain separate. A write returns its own result; it does not authorize other read operations.

All writes supply `request_id`, `idempotency_key` and applicable expected revisions. One account-wide idempotency namespace covers all operations. The canonical payload contains every semantic input and expected revision, excluding correlation/request and idempotency identifiers. An identical retry returns its original result with `replayed:true`, even after SQLite is reopened or a later mutation changes current state. A different payload/operation with the same key conflicts. Failed transactions reserve no key and create no audit event. Domain changes, before/after audit and the replay result commit together.

Product creation uses an empty `product.id` and `expected_revision:0`; the server generates the ID. Revision requires its existing ID and current expected revision. `product.revision` is output metadata; `expected_revision` controls the write. There is no merge by name. Optional stable `source_key` identifies one exact variant; colliding creation conflicts. Flavor and pack variants remain separate. Saved details replace the prior product's current details atomically; purchase snapshots remain separate.

Known basic fields require field provenance (`fields`, HTTPS URL, observation time and verification). Image, nutrition and listing observations have their own sources. Nutrition facts in one record share that record's serving basis/source/observation; mixed-source facts must not be silently presented under one source. `null` means unknown for pack size/count, price, currency, observation and nutrition fact values. Zero remains a real numeric value. Times are Unix milliseconds. Quantities use exact decimal strings with at most nine integer and six fractional digits. The server never fetches retailer/image URLs. It rejects executable/non-HTTPS/credential-bearing/literal local URLs; a future server fetch would additionally need DNS/redirect protection.

A listing's availability is `unknown`, `in_stock` or `out_of_stock`. A stock observation requires a store location and observation time; retailer context alone is never local stock evidence. Money remains integer minor units with an explicit supported ISO currency and observation time. The browser formats it using that currency's fraction digits.

## Lists and selection

`save_list` creates with empty `list_id` and revision0, or revises an existing ID at its current revision. `make_active:true` additionally compares `expected_selection_revision` (0 for a new account). Creation alone does not select a list. Selection has its own monotonically increasing revision. Archiving the selected list clears selection. A migration never chooses an old list implicitly.

`get_list` returns the selected list by default, or a specified owned list. It includes product summaries and the selection revision. Browser reads default to the account's preferred retailer; optional item restrictions include matching and unrestricted rows. MCP can read all items by omitting `retailer`, or request the same filter. `include_removed:true` exposes retained reconciliation evidence to authorized reads.

`update_list_items` requires the current list revision and 1–50 ordered operations. A stale item/list aborts the whole batch. Each successful batch advances the list revision once; each changed item advances its own revision. Multiple operations on one item must supply the sequential expected revisions.

| Kind | Fields |
| --- | --- |
| `add` | `product_id`, positive decimal `quantity`, explicit `merge:"increment"` or `"keep"`, optional `retailer`; no item ID/revision |
| `set_quantity` | `item_id`, current `expected_revision`, positive decimal `quantity` |
| `set_picked_up` | `item_id`, current `expected_revision`, explicit boolean `picked_up` |
| `remove` | `item_id`, current `expected_revision` |
| `restore` | removed `item_id`, current `expected_revision` |

`keep` leaves a live matching product unchanged. `increment` adds exact quantity and retains current picked-up intent; change that intent only with an explicit set operation. Different retailer restrictions cannot be merged by increment. Removal is soft and retains picked-up state plus audit evidence. Adding a removed product creates a new item; restoring its old item conflicts if a live matching product exists. Archived products cannot be added/restored. Lists are bounded to500 retained items; agents should create/select another list rather than erase evidence.

Checking, changing quantity or removing creates no purchase, price or metric. Only a separate sourced confirmation contributes to History/Insights.

## Browser API

Authenticated browser-session routes exist for `GET /api/v1/me`, `GET /api/v1/products`, `GET /api/v1/products/{id}`, `GET /api/v1/lists/active`, `POST /api/v1/lists/{id}/items`, `PATCH`/`DELETE /api/v1/lists/{id}/items/{item_id}`, `GET /api/v1/purchases`, `GET /api/v1/purchases/{id}` and `GET /api/v1/insights`. Product/purchase/preference writes have no browser endpoint. Reads require `shopping:read`; list writes require `shopping:lists:write`, exact Origin and valid CSRF. Mutations also send `Idempotency-Key`. The server supplies audit request/client identity. Bodies reject unknown fields and are limited to64KiB.

Browser add fields are `product_id`, `quantity`, `merge`, optional `retailer`, optional `expected_product_revision` and `expected_list_revision`. The product revision pin protects History reorders against a changed variant after rendering; its omitted nil field preserves prior list idempotency semantics. PATCH sends exactly one of `quantity` or `picked_up`, plus `expected_revision` for the item and `expected_list_revision`. DELETE sends both expected revisions. JSON responses contain authoritative records/revisions. Revision/idempotency conflicts return409; foreign resource IDs return404 without disclosure.

Catalog pagination uses a bounded cursor (`limit`1–50, default20), exact-owner ID ordering and literal case-insensitive substring search over saved name/brand/flavor. Activity pagination uses timestamp+ID and limits1–10 (default10). Cursors are opaque continuation positions, not authorization.

The browser waits for authoritative save results; failed/unknown outcomes never display an optimistic checkbox as synced. A failed network write retains its exact key/body for Retry and disables competing writes. Refresh reads current server state before discarding the pending attempt. Successful writes refresh the server-rendered main area, preserving keyboard focus when the control still exists. Focus/visibility and30-second visible-page revalidation update agent edits. This is not an offline queue.

## MCP

The Go application mounts the official SDK v1.8.0 directly at `/mcp` only when identity is configured. Every HTTP request validates its bearer audience/client/enrollment before stateless dispatch. No sessions are retained; supplied session IDs cannot select an owner. HTTP responses use `no-store`; protocol cache hints are private and immediately stale. The existing exact Host/Origin guard also protects reverse-proxy loopback requests.

Implemented tools: `get_context`, `search_products`, `get_product`, `save_product`, `archive_product`, `get_list`, `save_list`, `update_list_items`, `search_purchases`, `get_purchase`, `save_purchase`, `confirm_purchase`, `void_purchase`, `get_insights`, `update_preferences`, `get_activity`. Input schemas bound strings/arrays and reject additional fields; services enforce field-specific semantics. Structured outputs include current records/revisions and write replay state. Audit output explicitly describes product/list/preferences/purchase snapshots instead of treating JSON bytes as an array. Actual nonempty confirmation/replacement/void activity passed SDK output-schema validation.

There is no arbitrary URL fetch, SQL, execution, enrollment, metric-formula or infrastructure tool. Actual selected-client authentication/consent/refresh interoperability remains separate from SDK protocol tests.

## Purchases and corrections

`save_purchase` creates with empty ID and revision0, or replaces an existing draft at its expected revision. It requires an explicit purchase time/currency, retailer and receipt or explicit user-confirmation evidence, stable source reference and observed time. Sources are structured agent-supplied data; the server never fetches or imports retailer receipts. One `(owner, retailer case-insensitive, source_kind, source_reference)` identity belongs to a correction lineage, including after full void. Duplicate keys replay the original result; different requests cannot record the same source twice.

Each of1–50 lines requires an owned product, `expected_product_revision`, exact decimal quantity, explicit unit, nullable prices/discount/tax and optional list link. Snapshot name/brand/flavor/category/pack and field-source references derive from the catalog at confirmation. A product change between draft save and confirmation conflicts; explicitly revise the draft to accept revised facts. Confirmed, superseded and voided headers/lines are immutable. Audit records bounded facts/state transitions; full immutable field sources remain on the purchase lines.

`save_purchase`, `confirm_purchase` and `void_purchase` return a bounded `PurchaseOutcome`, rather than a full `Purchase`. It includes the authoritative receipt/header, line IDs and product revisions, identity/package/quantity/price snapshots, and original list reconciliation before/after evidence. It deliberately has no per-line `sources_snapshot` property. `get_purchase` (and browser purchase reads) return the full record with every stored field-source URL; confirmed source/receipt evidence remains immutable. This distinction keeps supported50-line purchases with12 long sources per line within the unchanged1MiB idempotency-result budget.

The exact compact outcome is persisted in the same transaction as the domain writes and audit. Repeating a request returns that original outcome after draft revision, catalog change, supersede, void or database reopen; it never hydrates from current mutable purchase rows. Previously persisted full-Purchase results decode to the same narrower outcome without rewriting them. Write-only purchase grants can receive their own write outcome, while full evidence reads still require `shopping:read`.

Unit price is before discount; line total is merchandise after allocated discount and excludes separately recorded tax. Known unit and line prices require explicit discount including zero. Round unit×quantity half up to a minor unit before subtracting discount; reject impossible/overflowing amounts. Tax is `excluded` when separately known (including zero), otherwise `unknown` with null. Missing unit price, line total or discount remains null; no inferred actual amount enters metrics.

Original confirmation compares all linked list/item revisions in one short transaction, matches products/retailer, and only accepts `pack` links. Full quantity soft-removes the item; partial quantity reduces the remainder and preserves picked-up state. An explicitly removed item stays removed with its remaining evidence. Every touched list advances once. Any stale product/list/item or invalid quantity rolls back status, all list changes, immutable reconciliation before/after evidence, audit and idempotency result.

Confirmed corrections create a draft with `replaces_id`, same retailer/source identity and no list links. Confirmation requires the old current revision and atomically marks it superseded. Replacement quantities may change, but lists are neither replayed nor automatically reversed: `reconciliation_applied:false`, original reconciliation purchase ID and before/after records make this explicit. Full void requires a reason and retains source claims/snapshots/reconciliation. Any required list adjustment is a separate explicit idempotent list mutation. Partial refunds are deferred.

History defaults to current confirmed records; purchase reads can inspect other states. Buy again uses the normal list-add operation, compatible pack-count lines, current non-archived variant facts and an expected current product revision. Description/source/nutrition-only edits remain reorderable; changed brand/flavor/count/size/unit and measured purchases require agent reconciliation instead of silent pack conversion. Retailer restrictions are preserved; incompatible active-list contexts offer no reorder.

## Insights

Fixed owner-qualified SQL projects current `confirmed` purchase snapshots, excluding draft/void/superseded records. The service computes weekly and category merchandise on the same after-discount/before-tax basis; currencies remain separate. Tax and missing merchandise/tax counts are separate. Null-only groups retain null amounts; a known zero stays zero. Checked int64 aggregation rejects overflow. Categories reconcile to the included merchandise subtotal for each currency.

`from` is inclusive and `to` exclusive Unix milliseconds. Default is eight calendar weeks through now; explicit ranges are at most371 days and25,000 lines (larger inputs require narrowing, never truncation). Account timezone and week-start drive midnight/AddDate boundaries, including167/169hour DST weeks. Incomplete calendar weeks are labelled; no misleading percentage comparison is calculated. All amounts concern recorded purchases only; missing historical coverage never means a zero-spend week.

Compatible category quantities use grams, liters or each. Pack conversion requires explicit count and known size/unit where measured; six known units without size contribute six `each`, while an unknown count/unit is excluded with its own missing count. The US fluid ounce factor is exactly0.0295735295625L; kg/g, oz/lb and ml/L use defined exact factors. Rational arithmetic aggregates before rounding output to18 fractional digits; the human view rounds to about two places. Labels say bought, not consumed. Weekly SVG plus category/quantity bars are server-rendered; no metric write or executable formula exists.

## Opt-in visual test fixture

For local visual QA only:

```sh
SHOPPING_UI_PREVIEW_ADDR=127.0.0.1:8092 \
  GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod GOPROXY=off \
  go test ./internal/web -run '^TestMobilePreview$' -v -count=1 -timeout=30m
```

`preview_test.go` exists only in a test binary. It generates synthetic users/data and obtains a real session through a local OIDC code+PKCE/JWKS flow. Its loopback-only wrapper injects that session and canonical test Host/Origin before the same production authentication/handlers run, avoiding a browser certificate workaround. The production executable does not read this test setting or serve fixture controls. `/__fixture/fail-writes` and `/__fixture/restore-writes` simulate503 writes for the visual check only. Stop the test process after QA; no fixture is a deployed identity acceptance test.
