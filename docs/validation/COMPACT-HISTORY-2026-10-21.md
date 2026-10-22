# Compact expanded History — October 21, 2026

## Coordinator final validation

After Translation (no source edits), offline cached Go1.26.9/Node24 `make assets check build` passed: Tailwind CSS, formatting,52 Node regressions,vet,all Go packages (valid caches),both binaries. Log `.cache/acceptance/compact-history-final.log`; diff check passed. Existing loopback-only `TestMobilePreview` then served the actual production handlers/templates/scripts with synthetic data. Actual in-app browser at320px measured document clientWidth/scrollWidth320/320; receipt amount right edges302, no name/amount overlap, zero History product SVGs, Receipt details initially closed. Enter opened purchase and secondary details; original pack/discount/tax/source and Buy again appeared. Actual Buy again click returned Saved through production script/services using synthetic data only. An initial click found no match because the existing polling refresh had reclosed disclosures; reopening both and clicking immediately passed. This is existing refresh behavior, not a new preserved-open guarantee.

Unknown compact amount and Tax showed `—` with native AX Amount unavailable/Tax unavailable. At390px clientWidth/scrollWidth390/390 and all six visible line/Tax right edges367; known-zero row/Tax both `$0.00`. Header totals/dates remained unchanged. No browser errors. Screenshot `.cache/previews/compact-history-implemented.jpg` captured after reload with first receipt expanded and details closed. Temporary tab closed and viewport reset; fixture intentionally stopped with Ctrl-C after inspection (not a completed long-running test pass). No production access, receipts or writes. Fresh independent Review subsequently returned **Approved**, no blocking findings.

Status: Builder, Translation, coordinator final/browser validation and fresh independent Review complete. Final verdict **Approved**, no blocking findings. Root `main` baseline `5025980`; uncommitted and undeployed. Preexisting preview notes are preserved. The owner's selection of the original `shopping-aisles-history.html` receipt supersedes the rejected icon and secondary pack-line preview variants.

## Behavior and reading order

1. `internal/web/history.go`: `dictPurchase` creates display-only receipt rows from saved line names, exact decimal quantities and recorded net line totals. One pack omits the suffix; two packs show `× 2`; measured quantities retain their units, including six-decimal values. No current catalog lookup supplies the receipt name. USD uses `$`; other currencies retain their existing code/decimal formatting. Unknown or invalid money is `—`; known zero is `$0.00`.
2. `purchaseTaxText` sums only separately recorded, nonnegative, complete tax amounts with checked int64 arithmetic. It does not include merchandise or subtract the already allocated discount. Complete tax can remain known when merchandise is missing. Missing/unsupported tax, invalid currency, no lines or overflow yields `—`. The existing confirmed-only header total policy is preserved and shares only the money formatter.
3. `web/templates/history.gohtml`, `web/styles/app.css`, generated `web/static/app.css`: the native purchase header and store/date/status remain. Expanded rows show name/quantity left and amount right, with no icons, secondary pack lines or merchandise label. One separated Tax row finishes the receipt. A closed native **Receipt details** disclosure retains pack facts, discounts, per-line tax, source evidence/link, correction/void text and the existing Buy again controls/conditions/attributes. The header's CSS now targets its direct summary so the secondary disclosure retains its own native marker.
4. `internal/web/history_test.go` and `history_totals_integration_test.go`: arithmetic, exact quantities, actual authenticated History rendering, saved names after a catalog rename and foreign-owner non-disclosure.

Normal executed trace: saved USD line amounts 499 and 599, an already allocated discount of 100, and separate taxes 35 and 25 → compact `$4.99`, `$5.99`, one Tax `$0.60`, header `$11.58`; discount is not subtracted twice. Recorded pack quantity 2 and measured quantity 500 g render inline. The secondary disclosure retains the original detailed amounts and Buy again availability.

Failure executed trace: one missing line amount → that compact amount and the header are `—`, while independently complete tax remains `$0.35`. One missing tax → Tax and header are `—`, while known line amounts remain. Accessible unavailable labels are present. A renamed catalog product leaves the saved receipt name intact; another user's list/direct view reveals no receipt rows or source evidence.

No schema, API/MCP contract, Insights calculation, authentication, persistence, purchase write or list-action implementation changed. The template's Buy again rules/actions are preserved; its controls moved into the secondary disclosure.

## Builder validation actually executed

- Read project/resume/spec/architecture/auth guidance and the complete Modern Go Guidelines list for Go 1.26 before Go edits.
- Focused uncached `go test -count=1 -run 'TestPurchaseTotalText|TestReceiptLineLabel|TestReceiptMoneyText|TestPurchaseTaxText|TestHistoryPurchaseSummaryTotals|TestHistoryCompactReceiptKeepsSnapshotAndOwnership' -v ./internal/web` passed: 51 helper cases, 10 History scenarios covering 19 route responses, plus saved-name/ownership checks over 4 route responses. Existing header/date/status/detail assertions remain. Cases include two packs, 500 g, exact `0.123456 kg`, unknown/zero, EUR, tax aggregation, overflow, missing tax and discount treatment.
- Initial focused run passed all 51 helper cases, then the synthetic OIDC fixture could not bind a loopback port in the sandbox. The identical scoped local-test escalation passed. This was an environment failure before rendering assertions.
- Cached Go 1.26.9/Node 24.19.0 and project caches, `GOTOOLCHAIN=local GOPROXY=off`, offline npm: `make assets check build` passed once after the final source/test changes. Tailwind 4.3.0 CSS, formatting, all 52 existing Node regressions, `go vet ./...`, all ordinary Go package tests (web 6.634s), and both binaries passed. No package installation or dependency change.
- `git diff --check` passed before the handoff and after the final source/test changes.

No Builder browser/layout, race, process/restart, container or live acceptance run is claimed. The rendering tests do not establish 320/390px layout or native keyboard interaction; coordinator checks remain. No persistent fixture listener was started; test-owned OIDC servers/databases used normal test cleanup.

## Translation inspection

Translation independently followed `NewHandler`'s authenticated History branch → owner-scoped purchase services/store reads → `shell` → `dictPurchase` → the compact receipt and secondary details. The helpers, view fields, template branches, CSS selectors and focused regressions are clear without hidden conversational context. No correctness issue or unresolved readability blocker was found, and no source/test/asset edit was warranted. Quantity and money display changes belong in the small named helpers; detailed receipt content remains together in the secondary disclosure.

Normal trace inspected, not re-executed: an authorized confirmed USD purchase with saved quantities `2 pack` and `0.123456 kg`, recorded net lines 499/599 and separate tax 35/25 produces exact inline quantity labels, `$4.99`/`$5.99`, Tax `$0.60` and header `$11.58`. The allocated discount is already in the stored line total. Opening Receipt details reveals the saved pack/source evidence and unchanged Buy again conditions/attributes; rendering adds no domain write.

Failure trace inspected, not re-executed: a missing line amount yields an accessible `—` for that row and the header, while complete separate tax remains visible. Missing tax instead makes Tax/header unavailable without hiding known merchandise. A foreign direct purchase ID fails the existing owner-scoped read and selects the shell's page-error branch before receipt rendering. The retained source text remains template-escaped.

`git diff --check` passed. Translation ran no test/build/browser/race/process/container checks and started no listener or dependency link. Builder's executed results above remain Builder evidence; coordinator final validation follows this frozen source. Translation changed only this evidence and the checkpoint/resume handoffs.

## Runtime and next action

Builder `/root/compact_history_builder`, session `01a16103-4618-73f1-9c53-47208618b949`, effective `gpt-6.1-sol` / `max`: coordinator verified host `turn_context` before authorizing edits. Source: `/Users/oudavid/.codex/sessions/2026/10/21/rollout-2026-10-21T20-29-26-01a16103-4618-73f1-9c53-47208618b949.jsonl`. Requested launch arguments and Builder self-report are not the verification source.

Translation `/root/compact_history_translation`, session `01a16109-617a-74b0-86b9-a6ff300c1eb8`, effective `gpt-6.1-sol` / `xhigh`: coordinator verified host `turn_context` before substantive work; Translation also read the matching model/effort fields. Source: `/Users/oudavid/.codex/sessions/2026/10/21/rollout-2026-10-21T20-36-07-01a16109-617a-74b0-86b9-a6ff300c1eb8.jsonl`.

Next: package the exact reviewed source and apply the selected layout to the live app following the owner's implementation request. Local slice is complete; deployment acceptance is pending. No commit, push, deployment, live data/infrastructure, aisles/reordering or separate shared-product-details worktree action occurred. Existing beta.4 rollout and CP03/06/10 beta follow-ups are unchanged by this local work.

## Independent Review

Fresh read-only `/root/compact_history_review` inspected the actual delta against the original preview, saved quantities/net amounts, tax arithmetic, unknown/zero handling, ownership, secondary details and focused regression/final validation evidence. No blocking findings. `git diff --check` passed; no files edited or test suite rerun. Exact verdict: **Approved**.

Coordinator verified host `turn_context` for Review session `01a1610d-1904-73e3-bb5a-9676967705d6`, effective `gpt-6.1-sol/max`, source `/Users/oudavid/.codex/sessions/2026/10/21/rollout-2026-10-21T20-40-10-01a1610d-1904-73e3-bb5a-9676967705d6.jsonl`. Builder `/root/compact_history_builder` was verified `gpt-6.1-sol/max`; Translation `/root/compact_history_translation` was verified `gpt-6.1-sol/xhigh`; their sessions are recorded above. All stage settings came from actual host metadata. No independent implementation approval is inferred from a coordinator pass.
