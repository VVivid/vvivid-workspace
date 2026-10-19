# CP07 purchase-audit correction — October19,2026

Builder evidence only, uncommitted over21d4f35. User authorized this correction after the prior independent Review Denied. Fresh Translation, final coordinator validation and fresh independent Review remain; no beta or deployment verdict.

Coordinator verified correction Builder `/root/purchase_audit_builder` from host `turn_context` session `01a152ad-31f5-7e32-9916-815bffcc502f` as gpt-6.1-sol/max before edits. Full Modern Go Guidelines list read before Go changes.

## Correction

`internal/store/sqlite/purchases.go` previously projected full purchases into every audit before/after body, stripping only field sources. Replacement confirmation recorded four bodies containing repeated product text and inherited reconciliation evidence. The unchanged256KiB audit cap rejected a supported50-line correction.

`PurchaseAudit` in `purchase_types.go` now records purchase ID/revision/status, retailer/time/currency/source identity, replacement/void state, line count and reconciliation count/origin/applied flag. Drafts additionally retain complete submitted inputs because subsequent draft edits overwrite their current rows. Confirmed field sources, product/pack/category/prices and original reconciliation before/after snapshots remain in their authoritative owner-qualified tables. Audit references identify those immutable records; replacement and void neither repeat nor reverse list side effects.

All purchase mutation paths share this projection. `get_activity` accepts compact audit records and the prior full-Purchase shape, preserving existing stored JSON. No migration, cap increase, purchase-line limit change, transaction/authorization/idempotency change or external action.

## Executed checks

With `GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod GOPROXY=off`:

- Exact reviewer reproduction copied from `CP07-review-boundary-reproduction.go.txt` into durable `internal/web/purchase_audit_integration_test.go`. Before correction, original purchase confirmed50 lines/50 reconciliations/97394 JSON bytes; replacement confirmation failed `audit summary is too large`. After correction, the exact reproduction passes.
- `go test ./internal/web -run 'TestReviewFiftyLinkedLineReplacement|TestPurchaseAudit' -count=1 -v`: passed. The test uses real local OIDC fixture authorization, production services and official SDK activity output validation.
- `make check build`: passed formatting, vet, all package tests and both application/admin binaries.
- `go test -race ./internal/web ./internal/shopping ./internal/store/sqlite`: passed, respectively16.445s/1.569s/3.929s.
- `git diff --check`: passed.

The first restricted run failed to bind the httptest loopback listener. Scoped local test escalation resolved it; no application identity bypass or remote access was used.

## Boundary and evidence results

The expanded test confirms50 linked lines with every name300, brand150 and flavor150 bytes. Separate ASCII, HTML-escaped (`<`) and Unicode cases also use a2048-byte single catalog source URL and receipt URL,2000-byte receipt evidence,200-byte receipt and line references,128-byte retailer,200-byte store location, maximum supported monetary values and1000-byte void reason. These are synthetic values, not actual receipts or retailer observations.

| Variant | Largest serialized audit summary | Existing cap |
| --- | ---: | ---: |
| ASCII | 60428 bytes | 262144 bytes |
| HTML escaping | 207706 bytes | 262144 bytes |
| Unicode | 60428 bytes | 262144 bytes |

Executed assertions cover original reconciliation once, maximum draft before/after revision evidence, stale replacement rejection without reserving its key, replacement confirmation, immutable old/full source snapshots, inherited reconciliation evidence, full void, identical confirmation replay after void and byte-identical list state after replacement/void/replay. Replacement activity contains only the two purchase state changes; failed/replayed attempts create no duplicate event.

A separate SDK scenario reads compact create/revise/confirm/void events together with a synthetic previously stored full-Purchase event. It verifies complete old/new draft inputs, state/revision/void references, unchanged persisted historical JSON, legacy SDK output acceptance and no cross-owner activity exposure.

Normal trace: confirm a50-line original → reconcile each item once → save/revise replacement → atomically supersede old purchase and confirm new record → compact audit references preserve original evidence → replay returns the original result, with lists unchanged.

Failure trace executed: replacement confirmation with stale old revision → conflict/transaction rollback, no event or reserved key → corrected revision under the same key succeeds once. Existing full-suite atomic later-item failure and restart replay tests also pass.

## Handoff

Code/tests frozen for Translation. No browser rerun is necessary for this audit/schema-only correction. No fixture/application process remains;8092 has no listener. No live infrastructure, enrollment, OAuth, proxy, deployment, migration or data reset occurred. CP06/07/08 remain In progress until the required fresh workflow and live acceptance finish.
