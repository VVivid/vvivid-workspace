# Resume AgentShoppingList

Updated October 19, 2026, America/New_York.

## Current state

Project: `/Users/oudavid/works/opensource/AgentShoppingList`, Git `main`, no remote. Approved code commit `05ff303f5c543e5a932e1f8b36ffc31c2e44c210` adds release/recovery; `746ef30` adds purchases/history/insights. Preserve all files and data.

CP01/02/04/05/07/08/09 are complete locally. CP03/06 live identity/client acceptance and CP10 live deployment/recovery acceptance remain in progress. Local release artifacts completed Builder → Translation → final validation → fresh independent Review **Approved**, with no actionable findings. V1 is not deployed or daily-beta accepted. No live account or production data was seeded.

One Go1.26.9 application serves four compact templates, narrow browser API and16 protected official-SDK MCP tools using shared owner/scope-checked services. Purchase writes preserve durable exact replay outcomes; full immutable source evidence remains on reads. History and insights use current confirmed snapshots. Checkoff never invents spending.

## Read in order

1. AGENTS.md, this file, CHECKPOINTS.md.
2. REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md.
3. docs/RUNBOOK.md, docs/AUTH.md, docs/DOMAIN.md, docs/DEPENDENCIES.md.
4. docs/validation/CP09-CP10-2026-10-19.md and CP07-result-correction evidence.

## Verified local release

- Real readiness checks configured identity and live database/current complete migrations; unconfigured identity returns503. Liveness stays independent of temporary issuer/database failure while HTTP serves.
- Pinned multi-stage Docker images run nonroot65532 with embedded assets/migrations, CA/timezone support and no Node/test identity in the runtime. Prepared deploy/compose.yaml requires actual existing Traefik inputs, one persistent named volume and private file secrets; no host port, separate gateway or new identity service.
- Operator backup/verify/restore/inspect uses the maintained SQLite online backup API, verified private atomic archives, schema/checksum/integrity/FK/hash/count/exact confirmed-total checks and isolated no-overwrite restoration without migration. Existing-schema upgrades require a verified recovery point before applying SQL.
- Final formatter/CSS/vet/all ordinary tests/both binaries, JS/shell/diff checks and actual production-process acceptance passed after Translation. Builder relevant race suites passed. Real process tests cover two owners, PKCE browser login, SDK Polar/list, browser check with no spend, sourced confirmation, History/Insights, revision races, durable sessions/preferences, graceful restart, lost committed response plus SIGKILL/concurrent replay, blocked precommit interruption/retry, online backup and isolated restore.
- Exact approved-commit beta images passed the same journey inside read-only/nonroot/network-none containers on Linuxarm64 and Linuxamd64 under local Docker emulation, followed by actual container stop/start and identical durable counts/totals. No task-owned test containers/volumes/listeners remain.
- Go govulncheck1.9.0 on patchedGo1.26.9 source and npm audit returned0 findings. Both final images passed local offline Trivy0.76.0 scans:0 known Debian/Go vulnerabilities, DB updated October19. Candidate scans had no network, Docker socket or host credentials. Public scanner/database download occurred separately without candidate access. Rejected Docker Scout was never retried; the safer method eliminated its metadata-egress risk and reviewer independently verified it.

Final local tags: `agentshoppinglist:beta-arm64`, `agentshoppinglist:beta-amd64`, version `0.1.0-beta.1`, full code commit05ff303f5c543e5a932e1f8b36ffc31c2e44c210, built2026-10-19T06:25:16Z. arm64 index0ebf9187e8202ae1d6cbe58e7d8c23db4771ec9b33fa7fa180b2ac420ccd3938; amd64 indexf5ba662e527b769409ef04a3de75a4e72d29f1a2016efd2ebad195a7b57b3d94. These are local images, not registry publications. Final logs/reports are ignored under .cache/acceptance/beta-*.log and .cache/image-audit/beta-*-report.json. Detailed metadata/hashes and stage verification are in the validation note.

## Next action and authorization boundary

The human authorized read-only discovery on personal_prd. Discovery completed and both SSHMCP connections are closed. Server is amd64, Docker29.1.3, Traefik3.7.5/networkproxy/publicHTTPSentrypointimmich-public-websecure/resolverletsencrypt, Keycloak26.8.1. Existing discovery works. Server resolver cannot resolve shopping.vvivid.dev; no shopping router/clients/resources exist. Live shopping-resource probe returns400invalid_target. No live writes occurred.

Read docs/DEPLOYMENT-PLAN.md for the concrete observed inputs, prepared amd64 artifacts, proposed shopping-only change set and evidence. deploy/personal-prd.environment.prepared contains nonsecret observed/proposed values and has not been applied. Application code/images remain05ff303, unchanged by discovery.

The human explicitly authorized copying only the credential-free deployed Go source and subsequently the required `internal/runner` package for local patch preparation. Both scoped exports completed and connections closed; earlier discovery-only export rejections are historical and these transfer prerequisites are cleared. No private config/environment/key/credential/database/user record was copied. Original source archive SHA256 `5ac441596d754fc711cec2c309b8bdd22958293122f8f492590287d2a354e177`; runner archive `bfcf3ae6d21ae9abebc46f2cece58c488093c86318ce7a669e59c0a5419d016b`. Exact original authproxy hash remains `98d4ed1e2aaf98c4acf34abdafc88abba3bd25e88543eb3b68d22399c6970741`. Baseline is ignored `.cache/proxy-compat/deployed`; isolated edits are under `.cache/proxy-compat/work`. The older local SSHMCP checkout is unchanged and not used.

Local compatibility bundle `deploy/oauth-compat/` contains the standard three-file patch, additional tests, 23-file baseline/patched hashes, scratch-only offline checker and concrete future apply/rollback instructions. Optional canonical HTTPS `shopping_origin` maps the three fixed shopping IDs to separate API/MCP targets, preserving SSH bindings, original refresh omission and unrelated realm-client behavior. Basic client-name normalization follows the exact Keycloak26.8.1 parser while preserving upstream credentials. Initial Translation clarified binding/resource names and equivalent failure conditions, regenerated patch/hashes, passed the uncached offline gateway suite and proved exact scratch reversal, then returned a root-module packaging failure to Builder. The correction stores the unchanged gateway tests as `oauth_compat_test.go.txt` and has the checker copy them to the original Go test path in scratch. Builder and initial Translation evidence is in deploy/oauth-compat/VALIDATION.md. Fresh Translation, coordinator final checker/build and fresh independent Review remain; original Builder binary identities predate Translation. CP03/06/10 stay In progress. No patch has been applied to production, no gateway binary executed, and application code/images remain05ff303.

Packaging correction Builder passed root `make fmt assets check build`, JS/shell/Python/diff checks and the complete offline scratch checker, including uncached gateway/runner tests, race tests, real Linux/amd64 build and exact23-file reversal. Current evidence is `.cache/proxy-compat/packaging-builder-checked/report.json`; binary SHA256 `0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917`. Patch SHA256 remains `91dbd6261754f5ad1c5344ac8eb17b1680ff8bcc4a4bbaa454acee8e8a7d10c4`; all baseline/work source and manifests are unchanged. Tests used synthetic local Keycloak/SSH peers and workers, with no production connection or binary execution. Fresh Translation completed without further code/checker edits: root package inventory excludes the inert test artifact, all23 baseline/four patched hashes and test equality pass, checker syntax and Builder binary hash pass, and focused uncached binding/Basic/config tests pass0.612s. Detailed traces and stage evidence are in VALIDATION.md. Coordinator final root/checker checks and fresh independent Review remain; no commit.

Next: complete that sequential local workflow, then obtain concrete rollout approval before any live binary/config/restart, DNS, Keycloak, enrollment, secret, artifact transfer or deployment write. The verified service is `sshmcp-gateway.service`, `/usr/local/bin/sshmcp-gateway --config /etc/sshmcp/gateway.json`; it also carries the SSHMCP administration path. Rollout requires an independent recovery channel and approved rollback watchdog before restart. Private config has not been read/exported; a future merge adds only `shopping_origin`, preserving every existing field. See docs/DEPLOYMENT-PLAN.md and deploy/oauth-compat/README.md.

User selections persist: https://shopping.vvivid.dev; own-account-only; Codex/ChatGPT; own preferences America/New_York, USD, Walmart, Monday. Actual shopping callbacks, artifact-transfer channel, optional background consent, remote repository and encrypted off-server recovery target/key still need resolution. Full CP03/06/10 live issuer/proxy/TLS/PKCE/audience/scope/refresh/revocation/crossSSH/client/phone/offserver acceptance remains unfinished. RPO24h/RTO1h/retention are proposed, not measured/scheduled.

## Latest steering — no production backups

User rejected the recovery proposal: keep everything on the server; do not back up anything. No production backup/snapshot/off-server copy or Mac recovery key should be created or scheduled. Previous related CP10 backup/restore exits are superseded; historical synthetic test evidence stays historical. Current appcode05ff303 still auto-backs up before migrations; remove that side effect via a new local workflow after the current proxy Review. Deploymentplan/proxyrollbackinstructions now avoid live-data/config backup copies and require a separately validated rebuilt-baseline/config-field-removal rollback. No live changes authorized.

## Latest local proxy verdict

The compatibility bundle completed correctionBuilder, freshTranslation, fullcoordinatorroot/checker validation and independentReviewApproved/noactionablefindings. See deploy/oauth-compat/VALIDATION.md for all stage runtimeproof and exacthashes. No gatewayexecuted orlivepatchapplied. Next localcodework: remove automaticapplicationpre-migrationbackup whilekeepingmigration-historyguards, updatecanonicalcurrentrequirements/runbooktonobackupserver-onlypolicy, validateandreviewthenrebuildapplicationimages. Liveprovisioning/deployment remainsunauthorized.
