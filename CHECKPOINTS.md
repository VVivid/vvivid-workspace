# Development checkpoints

Updated October 19, 2026. **CP00–CP02 complete. CP03 local workflow Approved with live acceptance pending. CP04/CP05 complete; CP06 local tools Approved with actual-client and later-tool acceptance pending.**

## Progress ledger

| Checkpoint | Status | Evidence |
| --- | --- | --- |
| CP00 Requirements and handoff | Complete | This document set, updated spec Page, preserved reference prototype; see RESUME.md |
| CP01 Go foundation | Complete | Embedded shell; Translation/final checks/independent Review Approved |
| CP02 Tenant-safe SQLite persistence | Complete | Final checks, isolation/idempotency/race tests; independent Review Approved |
| CP03 Identity and authorization | In progress | Local workflow independently Approved; live Keycloak/phone/client acceptance pending |
| CP04 Product and list services | Complete | Shared services, source/variant/revision/isolation/replay tests; independent Review Approved |
| CP05 Mobile templates and Web API | Complete | Built-in browser 360/390px interactions/failures, final checks, independent Review Approved |
| CP06 Built-in MCP | In progress | Local official SDK 10-tool slice Approved; purchase/insight tools and actual clients pending |
| CP07 Purchase reconciliation and history | Not started | Demo fixtures only |
| CP08 Derived insights | Not started | Demo calculations only |
| CP09 End-to-end and restart verification | Not started | None |
| CP10 Deployment and recovery acceptance | Not started | No shopping infrastructure changes |

## How to use checkpoints

Work from the first incomplete checkpoint. A dependency may require a small preparatory change, but do not mark dependent behavior complete before validating it. Continue within the development chat's authorization; checkpoint boundaries are not automatic approval gates.

Before pausing, update the ledger and RESUME.md. Capture:
- Status: Not started / In progress / Complete / Blocked.
- Files and behavior changed, and Git commit/branch if present.
- Actual checks run, dates, outcomes and concise evidence locations.
- Failures and skipped validation, with reasons.
- Exact next action and any required input.
- Active process ownership/ports, migrations, environment, and infrastructure changes if any.

Do not store credentials or personal receipt contents in evidence. Do not replay external writes on resume; inspect outcome and idempotency records first. A build, a saved config, deployment and runtime acceptance are separate claims.

## CP00 Requirements and handoff

Deliverables: README, AGENTS, REQUIREMENTS, SPEC, ARCHITECTURE, CHECKPOINTS, RESUME, identity context, decisions, source prototype and screenshots.

Exit: local package exists in AgentShoppingList; links are internally valid; Page incorporates selected architecture; checkpoint states are honest. No backend implementation required.

## CP01 Go foundation

Deliverables:
- Inspect applicable instructions and current files/Git state. Initialize repository only if appropriate and absent; preserve reference assets.
- Pin Go version, module name, official MCP SDK, SQLite driver, Tailwind tooling and migration approach. Module path/repository destination remain to be chosen.
- One application entry point, routing, configuration validation, template rendering, static assets and health endpoints.
- Document actual build/dev/check commands and required configuration without secrets.

Exit: clean build and a rendered mobile shell served by Go; no accidental React production app. Config errors are explicit. References remain isolated. Run appropriate formatting/static checks established by project config. Record commands instead of guessing them in this plan.

## CP02 Tenant-safe SQLite persistence

Deliverables: migrations for identities/preferences, product variants/listings, lists/items, purchase records/snapshots, audit, sessions and idempotency as needed; repositories require owner context.

Exit:
- Fresh schema creation and upgrade preserve data.
- Foreign keys work on all connection paths.
- Cross-owner reads, writes and relationships are rejected.
- SQLite WAL/busy behavior and a controlled write path are configured.
- Rollback and repeat-idempotency behavior are covered by meaningful tests.
- Test authentication may exist in test fixtures only; no public runtime identity bypass.

## CP03 Identity and authorization

Deliverables:
- Confirm final origin, API/MCP audiences, callbacks, enrollment and initial clients.
- Re-check existing Keycloak and OAuth proxy context.
- Implement browser OIDC sessions, CSRF, scope checks and MCP bearer validation.
- Prepare scoped shopping client/resource changes; apply live changes only within authorized scope.

Exit:
- Two test users map by verified issuer+subject and remain isolated.
- Wrong issuer/audience/signature/expiry/scope and missing CSRF fail.
- Browser grants cannot edit products or purchases.
- Session persistence/logout and signing-key rotation strategy are validated.
- If actual Keycloak integration cannot yet run, record local tests separately and leave integration acceptance incomplete; do not claim production readiness.

## CP04 Product and list services

Deliverables: source-backed product variants, predefined category icons, listing metadata, active-list selection, explicit merge semantics, set checked/quantity operations, soft removal, revisions and audit.

Exit:
- Polar appears as a drink with its real photo in details and pack metadata.
- Product variants do not merge by name alone.
- Atomic batches roll back fully on error.
- Duplicate mutation requests do not double-add.
- Conflicting browser/agent revisions cannot overwrite each other.
- Unknown price/availability/nutrition is not fabricated.

## CP05 Mobile templates and Web API

Deliverables: the narrow API in SPEC.md, My list/Products templates, reusable controls, detail dialog, minimal JS and focus/visibility revalidation. History/Insights tabs may be empty pending CP07/08.

Exit:
- Check on 360px and a second common mobile width for overflow, tap targets and clear states.
- Tap product opens accessible details; checkbox changes persist.
- Keyboard/focus behavior is verified for the dialog.
- Add/search/quantity/remove use authorized services and expected revisions.
- Network failure is visible and unsaved state is never presented as synced.
- No sidebar, dashboard, stock form, admin UI or unnecessary copy returns.

## CP06 Built-in MCP

Deliverables: official Go SDK HTTP handler mounted at /mcp, protected-resource metadata, typed tools and scope mapping from SPEC.md.

Exit:
- Actual selected MCP client can discover/authenticate and read/update its user's list.
- All planned tools have bounded schemas and structured results; incomplete purchase/insight tools stay unexposed until implemented.
- Every tool enforces ownership and required scopes.
- Wrong resource and foreign user IDs fail; sessions cannot cross users.
- Retry and concurrent edits behave identically through web and MCP.
- Go app serves MCP directly through Traefik; no separate MCP gateway is introduced.
- Agent research happens outside database transactions and is not an arbitrary server fetch/execute capability.

## CP07 Purchase reconciliation and history

Deliverables: draft/confirm/void and replacement corrections, immutable snapshots, source references, partial-purchase reconciliation, history UI and Buy again.

Exit:
- Checkoff alone does not manufacture confirmed spending.
- Confirm atomically records the purchase and reconciles expected list revisions.
- Duplicate receipt/request/confirmation creates one effective purchase.
- Partial purchase leaves remaining quantity; conflict causes no partial commit.
- Corrections/voids preserve audit and do not double-count.
- Product edits never rewrite historical name/pack/category snapshots.
- Unknown prices remain explicit; discounts/tax basis is defined.
- History displays actual records; Buy again uses idempotent list operations.

## CP08 Derived insights

Deliverables: fixed SQL/service calculations and simple mobile charts for weekly spend, categories and compatible purchase quantities.

Exit:
- All charts derive from the same confirmed records as History.
- Category totals reconcile with the displayed merchandise basis.
- Test null price vs zero, pack conversions, currencies, void/replacement, incomplete data coverage, and timezone/week boundaries.
- Partial-week comparisons are fair or explicitly labelled.
- Charts say bought/purchased, not consumed.
- MCP can read computed values but cannot set totals or submit executable formulas.

## CP09 End-to-end and restart verification

Deliverables: complete local environment, two-user browser+MCP acceptance and restart/concurrency evidence.

Exit:
- Demonstrate agent adds Polar → user views photo/checks item → agent confirms sourced purchase → History/Insights update exactly once.
- Cross-user isolation holds on web, MCP, media, caches and jobs.
- Restart process/container and retry an interrupted request: data and idempotency survive, no double purchase.
- Exercise SQLite contention and avoid long-held locks.
- Run required build/checks and meaningful integration tests; record actual results.
- Obtain applicable independent review and resolve findings per the current workflow.
- README/runbook contain exact reproducible commands and environment requirements.

## CP10 Deployment and recovery acceptance

Deliverables: immutable container build, internal network/Traefik routes, persistent volume, runtime secret handling, migration/backup/restore runbook, final Keycloak integration.

Exit:
- Shopping origin works on the user's phone and intended remote MCP clients.
- TLS, discovery, callbacks, consent, PKCE, audience and scope enforcement work through the deployed proxy.
- Shopping grant cannot authorize SSH, and SSH grant cannot authorize shopping.
- Existing SSHMCP authentication remains working after any OAuth proxy change.
- Graceful shutdown/restart and readiness checks work with persistence.
- Consistent database/asset backup is copied off server and restored in isolation; records and totals verified.
- Migration rollback/restore limits, retention, recovery objectives and release version are recorded.
- Distinguish published container/config from verified runtime outcome. Do not mark complete on deployment alone.

## Checkpoint evidence entry template

### CP00 update 2026-10-19

- Status: Complete.
- Changed: 10 planning/handoff documents, accepted prototype source, and 4 screenshots saved under AgentShoppingList; 28 files total before this ledger entry.
- Checks executed: required documents are present and nonempty; staged and project copies match byte-for-byte; selected architecture and ownership/scope terms exist; CP01–CP10 all remain Not started; no root Go module, database, or copied node_modules exists.
- Page: updated existing spec, inspected all operation receipts, and read back the stack/infrastructure, SQLite isolation, no-gateway, and resume sections successfully.
- Results: documentation and reference preservation checks passed. No Go application or authentication/runtime tests apply to this docs-only handoff. Page content was read back; a fresh rendered Page layout inspection was not performed.
- Side effects: created documentation/reference files only; updated the existing private spec Page. No Git repository initialization, backend development, shopping OAuth registration or deployment changes performed.
- Next action: in a new development chat, read AGENTS.md and RESUME.md and begin CP01.

### CP01 update 2026-10-19 — Builder handoff

- Status: In progress. Implementation and Builder validation passed; required Translation/final validation/independent Review are pending.
- Changed: initialized local Git on `main`; added Go 1.26.5 module `agentshoppinglist`, `cmd/shopping`, validated environment configuration, method-aware routes, embedded Go mobile templates/CSS, liveness/readiness, request IDs/safe structured logs, graceful shutdown, Make/npm commands, configuration example, dependency/migration decisions, focused config/render/route tests. Preserved reference assets; no React production runtime.
- Dependency selection: Tailwind CSS/CLI 4.3.0 with locked `@parcel/watcher` 2.6.0; official MCP Go SDK v1.8.0 and `modernc.org/sqlite` v1.60.1 recorded as exact pins for their implementing checkpoints. CP01 has no artificial unused SDK/driver imports. Numbered embedded transactional SQL/checksum migration approach selected for CP02.
- Commit: no commits yet; all project files remain untracked in the new local repository; no remote configured.
- Checks actually executed: `go version` (go1.26.5 darwin/arm64), Modern Go Guidelines `list --go-version 1.26`, `npm install --ignore-scripts`, compatible `npm audit fix --ignore-scripts`, `npm ci --ignore-scripts --offline` using the project cache, `npm run build:css`, `GOCACHE=$PWD/.cache/go-build make check build` (gofmt check, `go vet ./...`, `go test ./...`, binary build).
- Runtime checks: started the built binary on 127.0.0.1:8090; real HTTP requests returned 200 for all four pages, CSS and liveness, 503 for readiness, and 404 for absent `/api/v1/me` and `/mcp`. Responses had `no-store` and generated request IDs. Invalid `SHOPPING_HTTP_ADDR=bad` exited 1 with a configuration error before binding. SIGINT produced the shutdown log and exit 0; restarted successfully on the same port.
- Browser evidence from coordinator: built-in browser screenshot at 360 × 800 was clean; DOM scroll width and viewport width were both 360; all four navigation targets were 44px tall; clicking Products displayed its page. This verifies the signed-out shell only.
- Failures resolved: sandbox initially denied Git initialization, npm DNS and loopback binding; scoped escalations succeeded. First Go check ran before CSS generation finished and failed for the absent embedded CSS; all checks passed after sequential asset generation. Evaluated Tailwind 4.3.3 produced four high-severity build-tool findings; 4.3.0 plus compatible transitive lockfile update reduced npm audit to zero findings. No force update used.
- Unchecked: Translation, independent Review, SQLite/MCP/authentication and production/container behavior. Shell checks are not user isolation proof. Readiness remains intentionally 503 because persistence and identity are absent.
- Active process: Builder-owned exec session `37805`, `./bin/shopping`, loopback 127.0.0.1:8090, left running for coordinator browser inspection. Earlier validation process exited cleanly. No database, migrations, secrets, OAuth registration, live infrastructure or deployment changes.
- Evidence: [docs/validation/CP01-2026-10-19.md](docs/validation/CP01-2026-10-19.md).
- Next action: Translation agent inspects/readability edits within CP01; coordinator reruns final checks and obtains fresh independent Review before completing CP01 and starting CP02.

### CPxx update YYYY-MM-DD

- Status:
- Changed files / behavior:
- Commit or snapshot:
- Checks actually executed:
- Results and evidence:
- Unchecked or failed:
- Infrastructure or data side effects:
- Next action:

### CP01 update 2026-10-19 — Final validation and review

- Status: Complete. Independent reviewer reported no blocking findings and `Approved` for CP01 scope only.
- Translation expanded template navigation markup for readability; behavior and Go code unchanged.
- Coordinator executed `GOCACHE=$PWD/.cache/go-build make fmt assets check build` successfully after Translation. Built-in browser checked 360px and 390px widths (no horizontal overflow), 44px navigation targets, and all four tabs.
- Runtime stage verification: host session `turn_context` records confirmed Builder `/root/builder` (`01a1525d-f2b5-7280-8803-a69cc7f94b0d`) gpt-6.1-sol/max; Translation `/root/translation` (`01a15263-532f-71d3-bb84-4f388b978b4c`) gpt-6.1-sol/xhigh; Review `/root/review` (`01a15264-48e4-78d2-b48a-88b52b885c96`) gpt-6.1-sol/max. Requested arguments or agent self-report were not used as verification.
- Translation's default Go cache permission failure was resolved with a writable temporary cache; checks passed. No unresolved CP01 failures.
- No commit yet; no remote or live infrastructure changes. Builder-owned loopback shell session 37805 remains running.
- Next action: CP02 SQLite migrations, owner-qualified persistence, idempotency and isolation tests. Full v1 is not yet beta-ready.

### CP02 update 2026-10-19 — Builder handoff

- Status: In progress. Builder implementation and validation passed; Translation, final validation and fresh independent Review remain.
- Changed: actual `modernc.org/sqlite` v1.60.1 dependency/checksums with upstream-required `modernc.org/libc` v1.77.1; three embedded ordered/checksummed transactional migrations; owner-composite schema for identities/preferences, products/media/nutrition/listings, lists/items, purchases/immutable snapshots, hashed sessions, immutable audit and idempotency. Startup opens/migrates SQLite before HTTP and closes it after graceful shutdown.
- Ownership: explicit issuer+subject resolves an already enrolled opaque owner. Separate provisioning requires explicit valid timezone, currency, retailer and week start. No HTTP/MCP identity/enrollment bypass. Initial owner-qualified insert/read primitives and preference revision updates exist for later services; full domain workflows remain at their checkpoints.
- SQLite: private file, WAL/FULL synchronous mode, foreign keys and bounded busy timeout on every connection; one immediate writer and four read-only connections. Network research stays outside transactions.
- Checks executed: Modern Go Guidelines list for Go1.26.5; dependency resolution/tidy; `GOMODCACHE=$PWD/.cache/go-mod GOCACHE=$PWD/.cache/go-build make fmt check build`; `go test -race ./internal/store/sqlite` with the same caches. All passed.
- Tests executed: fresh schema; v1→v3 preserving preferences; repeat migrations; DDL/data/history rollback; drift/newer/gapped history rejection; explicit identity/preferences; two-owner reads/writes/relationships; ten composite FK cases; mutation rollback; changed-request idempotency rejection; eight concurrent duplicate requests mutate once; close/reopen checked-state/outcome persistence; per-connection settings; actual bounded writer contention with WAL reads; immutable confirmed/voided snapshots and audit; null vs zero prices.
- Real binary smoke: temporary database/loopback8091 started and shut down twice, with schema3/WAL/0600, shell200, readiness503, API/MCP404, no-store and SIGTERM exit0. Invalid memory-only DB config exited1 before HTTP. Real process smoke had zero accounts; store close/reopen tests preserve records. Full personal-data process acceptance remains CP09.
- Resolved failures: sandbox dependency DNS and loopback denial; scoped escalations succeeded. No unresolved Builder validation failures. Evidence: [docs/validation/CP02-2026-10-19.md](docs/validation/CP02-2026-10-19.md).
- Stage: coordinator verified Builder session `01a15265-6add-74b0-80e5-19fc809c30d3` from host turn_context as gpt-6.1-sol/max. Translation/review pending.
- Git/processes: no commits/remote; existing untracked tree preserved. Temporary smoke databases removed and both8091 processes exited. Earlier CP01 session37805 on8090 remains untouched, running its old binary image. No server access, live infrastructure, OAuth registration or deployment changes.
- Next: Translation inspect CP02, then coordinator final checks/build and fresh read-only Review. Continue the overall v1 goal afterward.

### CP02 update 2026-10-19 — Final validation and review

- Status: Complete. Fresh independent reviewer reported no blocking findings and `Approved`. One P3 remains: `.gitignore` should include `*.sqlite-wal` and `*.sqlite-shm` for custom paths outside default ignored data/. Include this housekeeping fix in the next Builder pass.
- Translation renamed the local transaction wrapper to mutationTx and explained the atomic domain/audit/idempotency commit; behavior preserved.
- Coordinator executed `GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod GOPROXY=off make fmt assets check build` successfully after Translation. Reviewer independently passed uncached full tests, SQLite race tests, vet and formatting.
- Host runtime turn_context verified Builder `/root/persistence_builder` (`01a15265-6add-74b0-80e5-19fc809c30d3`) gpt-6.1-sol/max; Translation `/root/persistence_translation` (`01a1526d-0f1a-74d3-a9af-0cdcc9e5cdfb`) gpt-6.1-sol/xhigh; Review `/root/persistence_review` (`01a1526d-f27c-70e0-ba41-c1dd43221ebb`) gpt-6.1-sol/max.
- Translation's initial default module-cache permission failure was resolved using the project caches.
- Next: CP03 local identity implementation and integration preparation. Live SSHMCP connect attempt for later read-only verification was rejected by automatic approval review as outside authorized local work; no connection opened, no live change. Do not retry indirectly; obtain explicit authorization for bounded live identity verification/setup after local preparation.
- No commits/remote. Existing old CP01 shell on8090 remains as recorded; all CP02 smoke processes stopped.

### CP03 update 2026-10-19 — Builder handoff

- Status: In progress. Local Builder checks pass; Translation/final validation/independent Review and live Keycloak/client acceptance remain. No beta verdict.
- Confirmed: https://shopping.vvivid.dev, own-account-only initial enrollment, Codex and ChatGPT. Exact registration/callbacks, subject and explicit preferences remain inputs.
- Changed: code+PKCE/state/nonce and one-use encrypted login attempts; durable AES-GCM sessions/external keyring, secure host cookies, exact Origin/CSRF, refresh/logout/revocation. Strict RS256 access JWTs enforce exact issuer, sole API/MCP audience, type/time/client/shopping scopes; browser grants cannot manage products/purchases/preferences. Shared verified Principal resolves active enrolled owners. No ID-token bearer auth or runtime fixture identity.
- Persistence: migration4 adds attempts and account disable; session repositories hold encrypted OAuth state and hashes. Network calls stay outside transactions. Operator shopping-admin enroll/disable requires explicit inputs; disable revokes sessions and blocks prior-owner writes. Fixed CP02 P3 *.sqlite-wal/*.sqlite-shm ignores.
- Executed: full modern Go guidance before edits; make fmt/assets/check/build passed; identity/store/admin race checks passed. Real fake-issuer cases cover discovery/code/PKCE/state/nonce, two-user isolation, invalid token claims/signature/scopes, CSRF/origin, reopen, refresh races/target/subject/revocation, logout and signing/session-key rotation. See docs/validation/CP03-2026-10-19.md.
- Built smoke: twice127.0.0.1:8091, same temporary empty DB, schema4/WAL/0600/zero accounts, shell/live200, ready503, unconfigured auth/API/MCP404, SIGTERM0 both. Partial identity config exit1 before listening. Processes/files cleaned.
- Resolved: sandbox DNS/loopback required scoped local escalations; schema assertions3→4; signing fixture corrected to retain previous public key during overlap. Final checks pass. No actual browser/Keycloak/Codex/ChatGPT sign-in executed.
- Git/live state: main/untracked, no commits/remote. AUTH.md/JSON fragments prepared only; no live connection/secret/enrollment/client/proxy/DNS/Traefik/deployment change. Coordinator separately verified8090 preview stopped; session37805 expired, final exit code unavailable. No Builder processes remain.
- Stage: coordinator verified identity Builder host turn_context session01a15270-47c7-7ac2-942c-3a7daebbf320 as gpt-6.1-sol/max. Translation/reviewer pending.
- Next: independent Translation of local CP03, final checks/build, fresh Review. Keep live acceptance In progress; bounded infrastructure authorization remains required. Continue permitted later preparation afterward.

### CP03 update 2026-10-19 — Local workflow approved, live acceptance pending

- Status: In progress. Local implementation completed Builder, Translation, final validation and independent Review `Approved`, with no blocking findings. Actual Keycloak/proxy/phone/Codex/ChatGPT acceptance remains unexecuted; not beta-ready.
- Translation clarified environment lookups, OAuth/session initialization and metadata field names, plus AUTH.md spacing. No security or side-effect semantics changed.
- Coordinator ran `GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod GOPROXY=off make fmt assets check build` after Translation. Sandbox httptest loopback bind failed; scoped escalation rerun passed all checks. Reviewer independently passed uncached full tests, vet and formatting.
- Host turn_context verified Builder `/root/identity_builder` (`01a15270-47c7-7ac2-942c-3a7daebbf320`) gpt-6.1-sol/max; Translation `/root/identity_translation` (`01a1527b-7f1c-74d1-a6c0-396494fa6382`) gpt-6.1-sol/xhigh; Review `/root/identity_review` (`01a1527d-3b07-7350-88f7-f772f457cd4d`) gpt-6.1-sol/max.
- User confirmed https://shopping.vvivid.dev, only their account initially, Codex and ChatGPT first clients. Preparation reflects these choices, without secrets or applied infrastructure changes.
- No application listeners remain. CP01 session37805 expired; original Builder verified no listener8090 and connection refused. All CP03 temporary processes/databases cleaned.
- Next: preparatory CP04–CP06 local product/list services, mobile adapters and built-in MCP over verified principals. Keep live CP03 integration open. No remote connection retry until explicit bounded authorization; earlier automatic rejection remains recorded.

### CP04–CP06 update 2026-10-19 — Local Builder handoff

- Status: In progress. Local Builder implementation/checks pass; Translation, final coordinator validation and fresh independent Review remain. CP03/CP06 live integration stays open. No beta verdict.
- Git/stage: baseline commit a3f8c76 on main, no remote. Coordinator verified Builder /root/shopping_builder host turn_context session01a1527f-e94d-74c3-a912-9ec17a0f5097 as gpt-6.1-sol/max before editing. Added .gitattributes LF rules without changing global settings.
- Changed: migration5 for field sources, item retailer restrictions, before/after audit and explicit active-list selection; shared scope/owner services; source-backed product variants/media/nutrition/listings; atomic bounded list batches, exact decimals, revisions, idempotency, soft remove/restore; mobile My list/Products templates/API, fixed SVG category icons, accessible details, server-authoritative JS/network errors/focus refresh; official SDK v1.8.0 stateless protected MCP with ten typed tools/private cache hints. History/Insights remain empty, purchase/insight tools unexposed, readiness503.
- Executed: Modern Go Guidelines; pinned dependency resolution/tidy; project-cache make fmt/assets, node --check, GOPROXY=off make check/build, and race tests for web/identity/store/admin. Full tests/vet/both binaries pass. Source URL, unknown facts, variants, all ten SDK tools, two-user scopes/ownership, CSRF/audience, atomic rollback/retry, cursor pages, browser/MCP revision races, selection/archive/restore and SQLite reopen replay/v4 upgrade cases pass.
- Coordinator built-in browser:360×800/390×844 no overflow,44px targets; Polar photo/details/null facts; dialog Tab/Escape/focus; persistent check, quantity2→3, remove/search/add;503 showed unsynced state and disabled competing writes; Retry saved authoritative state; final save preserved checkbox focus. Screenshots and exact case evidence: [docs/validation/CP04-CP06-2026-10-19.md](docs/validation/CP04-CP06-2026-10-19.md).
- Resolved failures: sandbox DNS/loopback required scoped local escalation; old migration-count assertions4→5; all-tool test found audit JSON bytes incorrectly inferred as array, fixed with known snapshot schema union. Final checks pass. Actual selected-client OAuth/phone/deployed proxy, purchases/metrics, whole-app process restart and recovery remain unexecuted.
- User confirmed own-account preferences America/New_York, USD, Walmart, Monday; no global default/live enrollment. No live infrastructure, SSHMCP, identity registration/proxy/Traefik/DNS/deployment changes.
- Process: final synthetic visual test preview session13624 remains on127.0.0.1:8092 for coordinator QA; test-only wrapper obtains/injects a genuine local OIDC session before production auth. No production bypass or fixture switch. Old preview44712 intentionally terminated for final embedded JS restart. Stop13624 after QA.
- Next: Translation, coordinator final validation and fresh Review; after local approval continue CP07/08 under the ongoing daily-use v1 goal. [docs/DOMAIN.md](docs/DOMAIN.md) records current contracts and future audit-schema extension point.

### CP04–CP06 update 2026-10-19 — Translation after Builder corrections

- Status: In progress. Translation completed; coordinator final validation/build and fresh independent Review remain. No CP03/CP06 live or beta acceptance claim.
- Runtime: coordinator verified fresh Translation `/root/shopping_translation_final`, host turn_context session01a15292-7794-7b23-ac8d-81a50e4a15eb, as gpt-6.1-sol/xhigh before edits. Baseline remains a3f8c76 on main; no new commit.
- Builder corrections inspected: product details render observed price and local availability independently, including null and zero prices; rejected writes report a refresh only when the reload succeeds, and failed manual refresh retains its Refresh action. Coordinator executed browser checks for these corrections before this pass, with fixture controls restored.
- Translation edits: expanded JavaScript async/error/event branches and documented the pending-write/reload constraints; expanded browser POST/PATCH/DELETE service input literals so list/item revisions and operation fields are visible. Preserved expressions, validation, authorization, retry key/body, transaction boundaries and side-effect order. No new correctness issue or unresolved readability blocker found.
- Checks actually run: Modern Go Guidelines for internal/web/api.go, gofmt on that file, `node --check web/static/app.js`, and uncached `GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod GOPROXY=off go test ./internal/web -count=1`. Sandbox denied the local fixture listener; scoped escalation rerun passed in0.502s. Full build/test/race and fresh rendered-browser checks after this readability-only pass remain coordinator work.
- Normal trace inspected: checkbox → PATCH with explicit picked_up/list+item revisions → shared authorization/validation → atomic item/list revision, audit and replay commit → authoritative page refresh → Saved/focus restoration. Failure trace inspected: stale revision → rollback/409 → page reload503 → explicit refresh-failed message and Refresh action; manual success reloads authoritative state. These traces are code inspection, not additional browser execution.
- Process: original Builder owns current synthetic preview session94993 on127.0.0.1:8092; it predates Translation's readability-only JavaScript edits. No production listener or live infrastructure change was made.
- Next: coordinator final formatting/assets/build/tests and fresh independent Review; continue CP07/08 after local approval.

### CP04–CP06 update 2026-10-19 — Local workflow Approved

- CP04 and CP05: Complete. CP06: In progress (local implemented tools Approved; actual clients and later purchase/insight tools pending). Fresh independent reviewer found no blocking issues and returned Approved for the local slice. Full v1 is not beta-ready; CP03 live acceptance remains open.
- Translation found two correctness issues before editing; each returned through separate Builder correction and fresh Translation: independent price/availability labels; false refreshed claim after409+page503. Coordinator executed all four listing cases and conflict/failure/recovery in the built-in browser. Final Translation expanded JS control flow and API inputs without semantic changes.
- Coordinator final `GOCACHE=$PWD/.cache/go-build GOMODCACHE=$PWD/.cache/go-mod GOPROXY=off make fmt assets check build` and `node --check web/static/app.js` passed. On exact post-Translation preview, labels, details/Escape, save/focus,409+page503 warning and restored manualRefresh passed. Reviewer independently passed uncached SQLite tests, vet, formatting, JS parse and diff checks.
- Host session turn_context verified each stage below; model always gpt-6.1-sol.

| Stage | Agent | Runtime session | Effort |
| --- | --- | --- | --- |
| Builder | /root/shopping_builder | 01a1527f-e94d-74c3-a912-9ec17a0f5097 | max |
| Translation (returned correctness issue) | /root/shopping_translation | 01a1528c-f501-7cd2-8c7f-a5f022d2ad5f | xhigh |
| Correction Builder | /root/shopping_correction_builder | 01a1528d-a874-71e1-8e5e-a71c1b1b6aeb | max |
| Translation (returned correctness issue) | /root/shopping_translation_retry | 01a1528f-731e-7dc3-85a4-d38176d0f092 | xhigh |
| Correction Builder | /root/refresh_correction_builder | 01a15290-5694-7c71-9e20-bebdf551c759 | max |
| Final Translation | /root/shopping_translation_final | 01a15292-7794-7b23-ac8d-81a50e4a15eb | xhigh |
| Independent Review | /root/shopping_review | 01a15294-d75d-7b33-9c23-ad46665a0d42 | max |

- Process: test-only preview session51151 on127.0.0.1:8092, original Builder-owned. All test controls restored; this is synthetic data, no production bypass. Prior fixture processes terminated intentionally for updated embedded assets.
- Next: CP07 purchase reconciliation/history and CP08 derived insights over shared services, then remaining local MCP tools, end-to-end/recovery/deployment artifacts. Do not retry live infrastructure until explicit bounded authorization; prior auto-review rejection still applies.
