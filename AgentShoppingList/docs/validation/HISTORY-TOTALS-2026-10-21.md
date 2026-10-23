# History header amounts — October 21, 2026

## Coordinator final validation

After Translation (no source edits), cached Go1.26.9/Node24 offline `make assets check build` passed: regenerated CSS,52 Node tests, formatting,vet,all Go package tests (valid caches) and both binaries. `git diff --check` passed. The existing loopback-only `TestMobilePreview` served synthetic records through actual production handlers/templates. In-app browser checks verified complete `$5.99`, unknown `—` with accessible text, and known-zero `$0.00` headers; Enter expanded/collapsed a complete record, pointer click expanded/collapsed an unknown record, original line/source details and Buy again remained visible. Actual document clientWidth/scrollWidth were320/320 and390/390. All amount right edges aligned (302 at320,367 at390); retailer/amount bounds did not overlap at320. A390px screenshot was visually inspected; browser error logs were empty. Browser tab closed, viewport reset and opt-in fixture intentionally stopped with Ctrl-C after verification (not a completed long-running test result). No persistent service or live data was used. Fresh independent Review subsequently returned **Approved** with no blocking findings.

## Scope and state

The owner approved the History portion of the conversation preview: price at the right of the collapsed retailer heading, date underneath, the same tap-to-expand interaction, no visible “Total” label, and an em dash when the amount is unavailable. This slice implements that History UI only. Aisles, agent list ordering, the separate shared-product-details worktree and deployment are outside this change.

Builder, Translation, coordinator final build/browser validation and fresh independent Review are complete on root `main`, baseline `cee0d4f815809600bd8b9d1665328fd9921225fc`. Final verdict: **Approved**, no blocking findings. This is local implementation completion, not deployment. Preexisting CHECKPOINTS.md/RESUME.md preview and shared-product-details notes were preserved. Existing CP03/06/10 beta acceptance gaps remain open.

## Behavior and reading order

1. `internal/web/history.go`: `dictPurchase` supplies display-only `TotalText`. `purchaseTotalText` adds each recorded after-discount `LineTotalMinor` and separate `TaxMinor`, using checked nonnegative int64 arithmetic. A supported currency, at least one line, current `confirmed` status and every merchandise/tax amount with `excluded` tax treatment are required. Missing/invalid facts, other statuses or overflow produce `—`; no amount is inferred from unit price or quantity. Discounts are already included in line totals and are not subtracted again.
2. `web/templates/history.gohtml` and `web/styles/app.css`: retailer and amount share a flex heading; the date remains beneath it. Native details/summary controls retain tap and keyboard expansion, with the arrow reflecting open state. Missing amounts have accessible text “Total unavailable”. The status label, expanded immutable line/source details and Buy again markup remain intact. USD uses `$`; other supported currencies reuse the existing `moneyText` format. Existing money formatting outside this History amount is unchanged.
3. `internal/web/history_test.go` and `internal/web/history_totals_integration_test.go`: total correctness and real authenticated-handler rendering regressions. The existing opt-in `TestMobilePreview` already supplies complete, unpriced and known-zero confirmed purchases and was left unchanged.

Normal input-to-result, executed: confirmed USD lines with net merchandise 499 and 599 minor units, an already allocated 100-unit discount and tax 35 plus zero → `$11.33` in the collapsed header. Expanded details still show `USD 4.99 merchandise`, `Allocated discount: USD 1.00` and `Tax: USD 0.35`. Both `/history` and the direct purchase view are checked.

Failure input-to-result, executed: a confirmed purchase with one missing merchandise amount or tax amount → `—`, preserving known expanded line data without presenting a partial sum as a complete amount. A direct draft view also shows `—` and its draft status. Known zero remains `$0.00`.

This is a presentation calculation over existing owner-authorized reads. No schema, API/MCP contract, authentication, purchase write, list mutation, audit, persistence or Insights computation changed.

## Builder validation actually executed

- Read the applicable project/resume/contract files and the complete Modern Go Guidelines list for Go1.26 before Go edits. Used the existing cached Go1.26.9 and Node24.19.0 runtimes and project module/build caches, with `GOTOOLCHAIN=local GOPROXY=off` and offline npm for the full build.
- Focused `go test -count=1 -run 'TestPurchaseTotalText|TestHistoryPurchaseSummaryTotals' -v ./internal/web` passed:21 arithmetic cases plus5 real-handler scenarios. Arithmetic includes discounts, known zero, currency decimal formats, absent/invalid currency, empty lines, unknown merchandise/tax, nonconfirmed status, invalid negative values, overflow within/across lines and the maximum exact int64 sum. Render scenarios exercise9 authenticated route responses, native collapsed summary markup, heading amount/date order, draft status, source/item details and compatible Buy again.
- Initial focused execution passed all21 arithmetic cases, then the synthetic OIDC fixture could not bind a loopback port under the sandbox. Scoped local-test escalation allowed the same suite to pass. This was an environment failure before the render assertions, not a code failure.
- After the final regression-boundary assertion edit, `make assets check build` passed: Tailwind4.3.0 CSS generation, Go formatting check,52 existing production-script Node regressions, `go vet ./...`, all ordinary Go tests and both application/admin binaries. The uncached web package completed in7.782s. Other tested packages also passed. No package installation or dependency change was needed.
- `git diff --check` passed after the source/assets changes and again after this handoff.

No actual browser/layout, race, process restart, container, real receipt or live acceptance run was executed by Builder. The static/render tests do not prove viewport alignment or keyboard interaction in a browser. The coordinator subsequently used the existing synthetic mobile fixture after Translation; see final validation above. Test-only databases/loopback OIDC servers belong to the test lifecycle; no persistent preview server was started.

## Translation

Translation independently inspected the changed helper, tests, template and styles, then followed the existing authenticated handler, service ownership checks, purchase reads and stored money contract. No correctness issue or unresolved readability blocker was discovered. `purchaseTotalText` keeps eligibility, complete amounts, checked arithmetic and display formatting together; the existing comment explains why discounts are not subtracted again. No source, test or generated asset edit was warranted. Only this evidence and the two handoff files changed during Translation.

- Normal trace, inspected: authenticated `GET /history` → owner-scoped confirmed purchase search (or `GetPurchase` for the direct view) → shell `dictPurchase` → `purchaseTotalText` adds 499 + 35 + 599 + 0 → `$11.33` → native summary shows retailer/amount with date below. Expanding reads the same immutable lines and source; rendering performs no domain write.
- Failure trace, inspected: the same confirmed record with a missing second merchandise amount reaches the nil guard → `—` with `Total unavailable` accessible text. The original known line/source details remain available on expansion; no partial total or price is persisted. Missing tax and nonconfirmed direct views use the same unavailable result. Existing load errors still use the shell's page-error branch before purchase rendering.
- `git diff --check` passed. No Translation test/build/browser/race/process run is claimed. A plain `go version` probe attempted the automatic Go1.26.9 download but could not open the default module-cache lock under sandbox permissions; no dependency was installed. The coordinator subsequently used the established cached runtime for final validation.

A contributor can follow the entry point in `internal/web/handler.go`, the two shell call sites in `web/templates/shell.gohtml`, the amount policy in `internal/web/history.go`, and its template/styles without this conversation. Future amount-policy changes belong in the helper and focused regressions; purchase writes and Insights remain separate existing services.

## Independent Review

Fresh read-only reviewer `/root/history_review` inspected both History consumers, owner-scoped reads, the monetary contract,21 arithmetic/5 rendering scenario definitions, template/style behavior and generated CSS. Coordinator native AX evidence confirmed the accessible unknown amount. No blocking correctness, scope, readability or required-validation finding. Reviewer diff check passed; no files edited or suite rerun. Exact verdict: **Approved**.

## Runtime verification

| Stage | Agent | Session | Effective model | Effective effort | Evidence |
| --- | --- | --- | --- | --- | --- |
| Builder | `/root/history_builder` | `01a15f07-aa25-7a00-9d8e-f2a03af257ff` | `gpt-6.1-sol` | `max` | Coordinator verified host `turn_context` before authorizing edits; host session `/Users/oudavid/.codex/sessions/2026/10/21/rollout-2026-10-21T11-15-00-01a15f07-aa25-7a00-9d8e-f2a03af257ff.jsonl` |
| Translation | `/root/history_translation` | `01a15f10-5d31-76c2-957a-f34787ffb5eb` | `gpt-6.1-sol` | `xhigh` | Coordinator verified host `turn_context` before authorizing edits; host session `/Users/oudavid/.codex/sessions/2026/10/21/rollout-2026-10-21T11-24-30-01a15f10-5d31-76c2-957a-f34787ffb5eb.jsonl` |
| Review | `/root/history_review` | `01a15f13-fa7d-7a23-aa7e-204760ee5e2e` | `gpt-6.1-sol` | `max` | Coordinator verified host turn_context in `/Users/oudavid/.codex/sessions/2026/10/21/rollout-2026-10-21T11-28-27-01a15f13-fa7d-7a23-aa7e-204760ee5e2e.jsonl` before review |

Requested launch arguments and agent self-report were not used as runtime proof. All three stages were verified against host turn_context metadata.

## Side effects and next action

Changed the History view helper/template/styles, generated embedded CSS and added the two focused regression files plus these handoff records. The build refreshed ignored local binaries. No commit, push, deployment, live database, server data, infrastructure, Keycloak, Traefik or shared-product-details worktree changes occurred.

Next: commit/integrate/release when requested. This reviewed local History slice is complete; it remains uncommitted and undeployed. Aisles/reordering remain preview-only. Existing beta acceptance gaps remain open.
