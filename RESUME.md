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

Next prerequisite: explicit authorization to copy only credential-free deployed Go source (go.mod, go.sum, internal/gateway, cmd/sshmcp-gateway) into a local isolated patch workspace. Automatic approval review rejected that export as sensitive egress beyond read-only discovery. Export was not obtained; lookup was also rejected and not bypassed. Do not retry without the human's specific authorization. Exclude private configs, environment files, keys, credentials, databases and user records. The deployed authproxyhash98d4ed1e2aaf98c4acf34abdafc88abba3bd25e88543eb3b68d22399c6970741 differs from the older local checkout; do not deploy the local checkout blindly.

After authorization, prepare and test the existing login proxy's explicit client-to-resource mapping through Builder → Translation → final validation → independent Review. Preserve SSH grants/refresh behavior, require shopping-only audiences, no separate gateway. Then obtain a concrete rollout approval before live DNS, proxy, Keycloak, enrollment, secret or deployment writes. Read-only discovery authorization does not authorize those writes.

User selections persist: https://shopping.vvivid.dev; own-account-only; Codex/ChatGPT; own preferences America/New_York, USD, Walmart, Monday. Actual shopping callbacks, artifact-transfer channel, optional background consent, remote repository and encrypted off-server recovery target/key still need resolution. Full CP03/06/10 live issuer/proxy/TLS/PKCE/audience/scope/refresh/revocation/crossSSH/client/phone/offserver acceptance remains unfinished. RPO24h/RTO1h/retention are proposed, not measured/scheduled.
