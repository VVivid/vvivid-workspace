# Local product, list, browser and MCP contract

CP04–CP06 local implementation, October19, 2026. Builder checks pass; Translation, final coordinator validation and independent Review remain. Actual Codex/ChatGPT OAuth and deployed acceptance remain pending. The app is not beta-ready. No test account or sample product is seeded by a production command.

## Reading order

- `internal/shopping/types.go`: typed inputs/results. `service.go`: verified Principal and scope checks. `validation.go`: source/package/quantity and operation rules. `write.go`: canonical semantics, audit and idempotency.
- `internal/store/sqlite/products.go`, `lists.go`, `mutation.go`, migration0005: owner-qualified records and short atomic transactions.
- `internal/web/handler.go`, `api.go`, `web/templates`, `web/static/app.js`: authenticated templates, narrow browser API and server-authoritative interactions.
- `internal/mcp/handler.go`: ten typed official-SDK tools, bounded input, stateless HTTP and private cache policy.
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

Checking, changing quantity or removing creates no purchase, price or metric. History and Insights remain truthful empty views until their checkpoints implement confirmation and derived calculations.

## Browser API

Authenticated browser-session routes exist for `GET /api/v1/me`, `GET /api/v1/products`, `GET /api/v1/products/{id}`, `GET /api/v1/lists/active`, `POST /api/v1/lists/{id}/items`, and `PATCH`/`DELETE /api/v1/lists/{id}/items/{item_id}`. Product/purchase/preference writes have no browser endpoint. Reads require `shopping:read`; list writes require `shopping:lists:write`, exact Origin and valid CSRF. Mutations also send `Idempotency-Key`. The server supplies audit request/client identity. Bodies reject unknown fields and are limited to64KiB.

Browser add fields are `product_id`, `quantity`, `merge`, optional `retailer` and `expected_list_revision`. PATCH sends exactly one of `quantity` or `picked_up`, plus `expected_revision` for the item and `expected_list_revision`. DELETE sends both expected revisions. JSON responses contain authoritative records/revisions. Revision/idempotency conflicts return409; foreign resource IDs return404 without disclosure.

Catalog pagination uses a bounded cursor (`limit`1–50, default20), exact-owner ID ordering and literal case-insensitive substring search over saved name/brand/flavor. Activity pagination uses timestamp+ID and limits1–10 (default10). Cursors are opaque continuation positions, not authorization.

The browser waits for authoritative save results; failed/unknown outcomes never display an optimistic checkbox as synced. A failed network write retains its exact key/body for Retry and disables competing writes. Refresh reads current server state before discarding the pending attempt. Successful writes refresh the server-rendered main area, preserving keyboard focus when the control still exists. Focus/visibility and30-second visible-page revalidation update agent edits. This is not an offline queue.

## MCP

The Go application mounts the official SDK v1.8.0 directly at `/mcp` only when identity is configured. Every HTTP request validates its bearer audience/client/enrollment before stateless dispatch. No sessions are retained; supplied session IDs cannot select an owner. HTTP responses use `no-store`; protocol cache hints are private and immediately stale. The existing exact Host/Origin guard also protects reverse-proxy loopback requests.

Implemented tools: `get_context`, `search_products`, `get_product`, `save_product`, `archive_product`, `get_list`, `save_list`, `update_list_items`, `update_preferences`, `get_activity`. Input schemas bound strings/arrays and reject additional fields; services enforce field-specific semantics. Structured outputs include current records/revisions and write replay state. Audit output explicitly describes the known server snapshot alternatives instead of treating JSON bytes as an array. Add a matching output schema alternative when later checkpoints add purchase audit record shapes.

Unimplemented purchase/insight tools are not advertised. There is no arbitrary URL fetch, SQL, execution, enrollment or infrastructure tool. Actual selected-client authentication/consent/refresh interoperability remains separate from SDK protocol tests.

## Opt-in visual test fixture

For local visual QA only:

```sh
SHOPPING_UI_PREVIEW_ADDR=127.0.0.1:8092 \
  GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod GOPROXY=off \
  go test ./internal/web -run '^TestMobilePreview$' -v -count=1 -timeout=30m
```

`preview_test.go` exists only in a test binary. It generates synthetic users/data and obtains a real session through a local OIDC code+PKCE/JWKS flow. Its loopback-only wrapper injects that session and canonical test Host/Origin before the same production authentication/handlers run, avoiding a browser certificate workaround. The production executable does not read this test setting or serve fixture controls. `/__fixture/fail-writes` and `/__fixture/restore-writes` simulate503 writes for the visual check only. Stop the test process after QA; no fixture is a deployed identity acceptance test.
