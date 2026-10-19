# Resume AgentShoppingList

Updated October 19, 2026, America/New_York.

## Current state

Project: /Users/oudavid/works/opensource/AgentShoppingList

**CP00–CP02 complete. CP03 local identity is independently Approved; live acceptance remains. CP04/CP05 complete after independent Review Approved; CP06 local tools Approved with real-client and later-tool acceptance pending. V1 is not beta-ready.**

Continue the user's overall deployable daily-use v1 goal after each local stage; checkpoint boundaries are not a request to stop. The current slice intentionally leaves purchase confirmation/History and derived Insights for CP07/08, then full end-to-end/restart/deployment/recovery acceptance. Live identity/client checks remain separate and unexecuted.

Baseline Git commit `a3f8c76` on `main` contains the foundation/identity tree; no remote. Later local changes implement product variants/sourced details, explicit active-list selection, atomic revisioned list batches, soft removal with picked-up evidence, before/after audit and durable idempotency. Migration5 preserves existing records and never guesses an active list. `.gitattributes` enforces LF for source/text without changing global Git settings.

Go1.26.5 serves embedded templates/CSS/minimal JS, a browser-session API and protected official SDK v1.8.0 stateless MCP from one application. Browser and MCP call the same scope/owner services. Ten tools are implemented: context, product search/read/save/archive, list read/save/update, preferences and activity. Purchase/insight tools are unexposed; the UI tabs remain truthful empty views. No seeded production users/data, runtime fixture identity, separate gateway or arbitrary fetch/execute/SQL. Readiness stays503.

Existing CP03 security remains: code+PKCE/state/nonce, one-use encrypted login attempts, external AES-GCM keyring and durable sessions, secure host cookies, exact Origin/CSRF, refresh/logout/revocation, strict access JWT audience/client/scope checks, explicit operator enrollment/disable and active owner resolution. No live infrastructure changes.

## Read in order

1. AGENTS.md and this file.
2. CHECKPOINTS.md for status and stage evidence.
3. REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md.
4. docs/DOMAIN.md for the implemented local contract/reading order and docs/validation/CP04-CP06-2026-10-19.md for executed evidence.
5. docs/AUTH.md, docs/DECISIONS.md, docs/DEPENDENCIES.md and README.md for configuration/remaining choices.

## Next action

Coordinator runs final validation/build on the CP04–CP06 local slice and obtains fresh independent Review. Translation completed after Builder corrected independent price/availability labels and rejected-write refresh failure handling. Do not call Builder or Translation checks an independent review. Keep CP03/CP06 live integration In progress. After local approval continue CP07/08, preserving current files/data and user scope; do not recreate the project.

## Actual validation

- Modern Go Guidelines read for Go1.26 before edits; official SDK primary release/docs checked and exact v1.8.0 dependencies pinned.
- Project-cache `make fmt assets`, `node --check web/static/app.js`, `GOPROXY=off make check build` and race tests for browser/MCP/identity/store/admin passed. Existing snapshot/auth/session/security regressions pass.
- Real loopback OIDC/browser/official SDK integration executed all10 tools, scopes/CSRF/audience/two-user isolation, source validation, exact decimals/variants/null facts, bounded catalog/activity pagination, atomic rollback, idempotent retries, browser/MCP concurrent revision race, explicit selection and archive/restore rules. Reopening SQLite preserved replay outcomes without modifying later state; v4→v5 preserved old shopping rows.
- Coordinator built-in browser checked360/390 widths, tap targets, real Polar fixture photo/details, dialog keyboard/Escape/focus, persistent checks, quantity/search/add/remove,503 unsynced/Retry and final focus restoration. Screenshots/evidence linked in the validation note. These are synthetic local records, not production or retailer verification.
- Fresh Translation independently inspected the corrected listing labels and write/refresh flow, expanded dense JavaScript branches and browser API input literals without changing behavior, then passed Modern Go guidance, `gofmt`, JavaScript parse and uncached `go test ./internal/web -count=1`. The sandbox listener denial was resolved with scoped local escalation. Coordinator separately browser-checked null-price/known-stock, known/zero-price/unknown-stock and409-write+503-refresh recovery before Translation; final full validation/review remain.
- Initial sandbox dependency DNS/listener denial resolved by scoped local escalations. Schema4→5 expectations and SDK audit-output schema inference issue were fixed; final checks passed. No unresolved Builder check failure.
- Final opt-in test preview session94993 remains at127.0.0.1:8092 for coordinator follow-up. It injects a genuine OIDC-created synthetic session through a wrapper compiled only into the test binary before production authentication executes. Production has no fixture switch. Stop this process after QA. Earlier previews were intentionally restarted to load corrections; the current preview predates Translation's readability-only JavaScript edits. Old foundation8090 remains stopped. No production application server runs for this slice.

Use `GOMODCACHE=$PWD/.cache/go-mod GOCACHE=$PWD/.cache/go-build` in the restricted workspace. Loopback tests may require scoped escalation. See the evidence note for commands and actual cases.

## Context and remaining inputs

- User opens a ready Walmart list and checks products; agents manage records. Four compact tabs, no dashboard. Fixed category icons on rows; exact photos belong in details.
- Checkoff records picked-up intent; it never creates a purchase or actual price. Soft removal preserves evidence. Future confirmed purchases alone produce History/Insights metrics.
- Explicit merge increment/keep, expected list/item/selection revisions and account-wide idempotency are shared by browser and MCP.
- User chose https://shopping.vvivid.dev, own-account-only initial enrollment, Codex and ChatGPT. Own-account preferences explicitly selected: America/New_York, USD, Walmart, Monday. These are not global defaults and no live account is enrolled.
- Exact subject, registrations/callbacks, live issuer/resource/proxy compatibility, optional background consent, remote repository and recovery targets remain pending. October18 Keycloak observations are historical, not refreshed runtime evidence.
- Automatic approval review rejected the earlier production SSHMCP connection as outside local authorization. No connection opened or indirect retry/bypass occurred. Local artifacts/tests are authorized; live identity/Traefik/DNS/proxy/enrollment/deployment requires the separately recorded bounded authorization.

## Stage evidence

CP03 Builder/Translation/Review runtime evidence is in CHECKPOINTS.md. Coordinator verified CP04–CP06 Builder `/root/shopping_builder`, host turn_context session01a1527f-e94d-74c3-a912-9ec17a0f5097, as gpt-6.1-sol/max before implementation, and fresh Translation `/root/shopping_translation_final`, host turn_context session01a15292-7794-7b23-ac8d-81a50e4a15eb, as gpt-6.1-sol/xhigh before edits. Review for this slice must supply actual independent runtime evidence and a verdict.

## Latest coordinator result — CP04–CP06

CP04/CP05 are complete after corrections, final Translation, final build/tests/browser checks, and fresh independent Review Approved (no blocking findings). CP06 local10tool slice is Approved; actual-client and purchase/insight tools pending. Runtime model/effort verification IDs for every original/correction stage are in CHECKPOINTS.md. Continue CP07/CP08 locally while live CP03/06 remain pending. Test preview51151 on8092 is synthetic; all failure controls restored. Existing base commit a3f8c76; next snapshot follows this approval. No live infra changes.
