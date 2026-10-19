# Resume AgentShoppingList

Updated October 19, 2026, America/New_York.

## Current state

The owner authorized the shopping-only production rollout and later narrow Keycloak diagnostics/fixes. Beta `0.1.0-beta.2`, application commit `7970b5c`, is deployed at https://shopping.vvivid.dev. The application, browser sign-in/refresh, actual Codex OAuth/tools and live restart persistence are verified. The owner completed ChatGPT connection; Shopping is visibly installed. Actual ChatGPT read/write example creation passed and was independently verified in the UI and readonly audit counts. The owner says the app is good; coordinator recommends V1 beta for daily use. No backups, snapshots, off-server backup copies or Mac recovery keys: this is explicit user policy.

Git main, no remote. Latest committed handoff `0867458`; later live evidence is uncommitted. Preserve all work and production data. CP01/02/04/05/07/08/09 are complete locally; CP03/06/10 remain open for the remaining actual client, phone and receipt-flow acceptance. Do not mark the overall goal complete prematurely.

## Immediate next action

No further feature development is needed before V1 beta use. The owner said the app is good and asked whether this is version1; label the current deployed build V1 beta, retaining actual build0.1.0-beta.2/application7970b5c. Do not rebuild, redeploy, create another plugin or seed more examples merely to relabel it. Let the owner use the app daily and capture feedback. Physical phone/real shopping/receipt confirmation, logout/revocation and remaining live negative-token cases remain unverified beta follow-up; do not turn them into passed checkpoint exits.

Actual ChatGPT conversation https://chatgpt.com/c/6ad6237b-d3b0-83e9-a830-ceb8fbfb0240, Create grocery list, created3sourced variants through Shopping MCP after a provenance-field correction. Active ExampleWalmart contains PolarBlackCherrySeltzer8×12floz, GreatValueWholeVitaminDMilk1gallon, GreatValueLargeWhiteEggs12count, quantity1 each/allunchecked. OriginalWalmart/preferences preserved, no purchases/spending. Root independently verified liveUI and readonly counts/audit actor shopping-chatgpt (save_product3/save_list1/update_list_items1). No SQL/admin data writes. Browser tabs6(app)/10(ChatGPT) marked deliverable.

Current Codex chat does not directly expose the newly installed Shopping plugin; actual ChatGPT browser client was used for this test. Future conversations should select Shopping when calling it. Existing Codex CLI connection also works. Do not claim root had direct Shopping tools.

## Read first

AGENTS.md, CHECKPOINTS.md, REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md, docs/DEPLOYMENT-PLAN.md, docs/CLIENT-SETUP.md. Review docs/validation/CP10-no-backup-2026-10-19.md, CP10-subject-mapper-2026-10-19.md and deploy/oauth-compat/VALIDATION.md for exact source/image/stage evidence. Current files and runtime observations take precedence over historical statements.

## Deployment and identity

Private deployment `/home/admin/agent-shopping-list`, service container `agent-shopping-list-shopping-1`, dedicated volume `agent-shopping-list-data`. Existing Traefik network `proxy`, public entrypoint `immich-public-websecure`, resolver `letsencrypt`; no direct app host port. DNS-only shopping A→144.91.70.238 TTL300. Server is Linuxamd64. Exact tested image index `sha256:9d268276169da14740fa9a05e2e5765f01eb94c1b4e346aa2129cf8c13c596dd`; config digest `efcd5b19be9e3b3ceef3c470bc61c1547769bb9e79278ef74581d6413dbd13f0`. Docker uses the index as image reference, not the config digest. Do not deploy obsolete05ff303 images.

Only the existing human account is enrolled, with America/New_York, USD, Walmart, Monday. Secrets and enrollment subject remain private server files; do not print/export values or regenerate session keys. Shopping clients have separate canonical API/MCP audiences, shopping-only scope assignments, required subject mapper, S256, consent and at most300s access tokens. Password/service/device/CIBA grants disabled. Background/offline consent remains deferred.

The initial browser callback failed because access tokens lacked sub. Added only direct shopping-subject/oidc-sub-mapper to the three shopping clients; evaluated claim assertions and then actual browser/Codex sign-in passed. Fragments/AUTH correction completed verified Builder→Translation→Review Approved; no app validation was weakened.

Gateway binary SHA `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917`; only shopping_origin added to private config. Baseline rebuilt binary `2a22fcc08639e846bf0d49cf90c5ac3ad1a0259c8a4ac97a1e7a50bc442b038a` exactly matched the old installed binary. Independent direct SSH alias personal_prd and sudo work; optional task control socket /tmp/shopping-rollout-ssh may expire. The temporary rollback timer was disarmed after successful enabled-client/gateway/routing/restart checks. No timed rollback is pending. No live binary/config/database backup was made.

## Actual acceptance

Public TLS/readiness/discovery pass; unauthenticated MCP401. Correct shopping authorization target200; missing/cross-API/SSH target400; SSH client→shopping400. Existing SSHMCP connect/run/disconnect passed after the gateway restart, most recently job fa182b02bb0e7ec47fcdf3e789d0b809. This proves the existing connector grant works; a separate fresh interactive SSH login/refresh is not claimed.

Human browser sign-in succeeded. Protected History navigation with a174-second-old durable session passed the mandatory60-second provider refresh boundary. Actual container restart with active Walmart list and browser session preserved integrity/count/totals; browser reload stayed signed in.

Actual Codex OAuth login and get_context/get_list/save_list/get_list/search_products/search_purchases/get_insights passed. Only write created/selected the empty Walmart list; no products or purchases. Codex callback http://127.0.0.1:18444/callback. Local shopping config uses resource discovery once and explicit five shopping scopes; the initial default-scope/duplicate-resource attempt was blocked/canceled. No static token or unrelated permissions.

Owner completed ChatGPT setup privately after copying/pasting its server-held secret. Plugin `plugin_asdk_app_6ad623031440819180772daa4ee4d3f2`, name Shopping, is installed. The actual UI supports User-Defined OAuth Client. Client shopping-chatgpt enabled with observed exact callback https://chatgpt.com/connector_platform_oauth_redirect, client_secret_basic, five shopping default scopes, OIDC onlyopenid (realm-wide SSH/offline list removed from the form). No DCR/CIMD enabled. Do not repeat secret entry or plugin creation. Actual ChatGPT reads/writes and final verification passed.

Phone confirmation, real checkoff and receipt-confirmation/history/insight journey, logout/revocation and remaining live negative-token acceptance are still unverified. Synthetic examples must not become fake personal receipts.

## Application and local validation

One Go1.26.9 app serves compact My list/Products/History/Insights, shared owner/scope-checked services and16 official-SDK MCP tools. SQLiteWAL, checksummed migrations, immutable purchase snapshots, atomic idempotency/revisions and encrypted durable sessions. Checkoff never invents spending. No automatic migration backup; manual recovery utilities remain unused in production.

Both exactbeta2 architectures passed actual process/browser/SDK/crash/retry/container restart checks and offline pinned Trivy scans with0 known findings. Final amd64 archive `.cache/image-audit/beta2-amd64.tar`, SHA c4ddcba2bda6693a496a5000484bf0477637fc9383e07a4c29c486d67ccbd2cc. No new application source build is required for the client setup.

Use cached Go `.cache/go-mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go`, GOTOOLCHAIN=local, project caches and GOPROXY=off. For application changes follow build-translate-review with actual host-runtime verified gpt-6.1-sol/max Builder/Review and xhigh Translation; on Review Denied stop for the human. Keep this handoff current with actual evidence and avoid resetting completed work.
