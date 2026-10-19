# Resume AgentShoppingList

Updated October 19, 2026, America/New_York.

## Current state

Project: `/Users/oudavid/works/opensource/AgentShoppingList`, Git `main`, baseline `7d671f6`, no remote. Preserve all files and data. Prior application code `05ff303f5c543e5a932e1f8b36ffc31c2e44c210` completed local independent Review; the current server-only no-backup correction is uncommitted and requires its own sequential workflow.

CP01/02/04/05/07/08/09 are complete locally. CP03/06 live identity/client acceptance and CP10 deployment/persistence acceptance remain in progress. The OAuth compatibility bundle is independently Approved locally and unapplied. V1 is not deployed or daily-beta accepted. No live account or production data was seeded.

One Go1.26.9 application serves four compact templates, a narrow browser API and 16 protected official-SDK MCP tools using shared owner/scope-checked services. Purchase writes preserve durable exact replay outcomes; full immutable source evidence remains on reads. History and Insights use current confirmed snapshots. Checkoff never invents spending.

## Read in order

1. AGENTS.md, this file, CHECKPOINTS.md.
2. REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md.
3. docs/RUNBOOK.md, docs/AUTH.md, docs/DOMAIN.md, docs/DEPENDENCIES.md.
4. docs/DEPLOYMENT-PLAN.md and deploy/oauth-compat/VALIDATION.md.
5. Historical application evidence in docs/validation/CP09-CP10-2026-10-19.md and CP07 result-correction evidence.

## Current user policy and local correction

The user explicitly rejected backups: keep everything on the server; do not back up anything. Shopping data and runtime secrets stay on the server. Do not create or schedule production backups, snapshots, off-server copies, restore drills, retention jobs or Mac recovery keys. This supersedes previous related CP10 exits and proposed RPO/RTO/retention targets. Historical synthetic backup tests remain historical evidence, not active operational instructions. Existing manual operator backup tools require a future explicit request and are not invoked by startup, enrollment, browser or MCP paths.

The frozen Builder correction removes Store.Open's automatic pre-migration archive, MigrationBackup field/log/error claim and the private backupBeforeMigration helper. The no-history/nonempty-schema guard now lives in migration initialization; checksum/name/consecutive/newer history checks, WAL, short per-migration transactions, owner checks and idempotency are preserved. No migration SQL, domain, UI, auth or MCP change. New regressions cover fresh/existing/upgrade readiness without extra archive/scratch files, preserved checked state/preferences/session ciphertext/purchases/audit/exact replay, refusal of unrelated/application schemas without history and rollback of failed migration SQL/data/history. Uncached SQLite passed0.822s; root make fmt/assets/check/build passed; uncached SQLite/admin races passed5.432s/1.744s; actual local production-process acceptance passed2.430s; JS/shell/diff checks passed. The process tests' manual backup/restore calls use synthetic fixtures only. No final images were rebuilt. See docs/validation/CP10-no-backup-2026-10-19.md.

Current workflow: Builder runtime `/root/no_backup_builder`, host session `01a15449-c4f9-7c00-bb85-5cdc3efa47d2`, verified by coordinator from host turn_context as gpt-6.1-sol/max. Translation `/root/no_backup_translation`, host session `01a15451-7383-7783-a1bf-25e95cbad84d`, was verified from host turn_context as gpt-6.1-sol/xhigh and completed without code edits or a correctness/readability blocker. Seven focused uncached SQLite migration/readiness tests passed0.400s; Modern Go1.26 guidance and diff check passed. Normal/failure traces are in the correction validation note. Coordinator final validation, fresh independent Review and replacement image builds/tests/scans remain. No new independent approval is claimed.

## Previously verified local release evidence

At application commit05ff303, readiness checked identity plus live database/current complete migrations; liveness was independent of temporary issuer/database failure. Formatter/CSS/vet/all ordinary tests/both binaries, JS/shell/diff checks, relevant races and actual production-process acceptance passed. Real process/container tests covered two owners, browser PKCE, official SDK Polar/list, check without inferred spending, sourced confirmation, History/Insights, revision races, durable sessions/preferences, graceful restart, lost committed response plus SIGKILL/concurrent replay and blocked precommit interruption/retry. Existing manual recovery tools were tested only on synthetic fixtures.

Pinned multi-stage images run nonroot65532 with embedded assets/migrations, CA/timezone support and no Node/test identity. Prepared deploy/compose.yaml uses existing Traefik, one persistent named volume and private file secrets. Exact05ff303 images passed the journey and container stop/start on Linuxarm64 and emulated Linuxamd64. Go govulncheck1.9.0, npm audit and offline pinnedTrivy0.76.0 scans reported zero known findings on that candidate. Docker Scout was rejected/unexecuted; candidate scans had no network, socket or host credentials.

Historical tags `agentshoppinglist:beta-arm64` and `agentshoppinglist:beta-amd64`, version `0.1.0-beta.1`, built `2026-10-19T06:25:16Z`, still contain05ff303 and are superseded as deployment candidates by the correction. arm64 index `0ebf9187e8202ae1d6cbe58e7d8c23db4771ec9b33fa7fa180b2ac420ccd3938`; amd64 index `f5ba662e527b769409ef04a3de75a4e72d29f1a2016efd2ebad195a7b57b3d94`. They are local images, not registry publications. Detailed hashes and ignored .cache evidence are in the historical validation note. Rebuild/test/scan replacement images only after the new code workflow; do not transfer the old candidate.

## Live discovery and approved local proxy bundle

Authorized read-only personal_prd discovery completed and all SSHMCP connections closed. Observed amd64, Docker29.1.3, Traefik3.7.5/networkproxy/publicHTTPSentrypointimmich-public-websecure/resolverletsencrypt and Keycloak26.8.1. Existing HTTPS discovery works. The server resolver could not resolve shopping.vvivid.dev; no shopping router/clients/resources were found. A shopping-resource probe returned400invalid_target. These are discovery observations, not live shopping acceptance. No live write occurred.

The user explicitly authorized credential-free deployed Go source and required internal/runner exports for local compatibility preparation. Original archive SHA256 `5ac441596d754fc711cec2c309b8bdd22958293122f8f492590287d2a354e177`; runner archive `bfcf3ae6d21ae9abebc46f2cece58c488093c86318ce7a669e59c0a5419d016b`; original authproxy SHA256 `98d4ed1e2aaf98c4acf34abdafc88abba3bd25e88543eb3b68d22399c6970741`. No private config/environment/key/credential/database/user record was copied. The baseline/work exports remain ignored under .cache/proxy-compat; the unrelated SSHMCP checkout is unchanged.

The committed deploy/oauth-compat bundle completed Builder corrections, fresh Translation, coordinator full checker/root validation and independent Review Approved with no actionable findings. Exact stage identities, hashes and validation are in deploy/oauth-compat/VALIDATION.md. It contains the three-file patch, inert test artifact, 23-file baseline/patched hashes and scratch-only checker. Optional canonical shopping_origin binds fixed shopping web→API and Codex/ChatGPT→MCP while preserving SSH bindings/refresh omission and unrelated realm-client behavior. Basic normalization matches Keycloak26.8.1 and preserves upstream credentials. No gateway binary was executed and no patch was applied to production.

The observed service is sshmcp-gateway.service, `/usr/local/bin/sshmcp-gateway --config /etc/sshmcp/gateway.json`, and it also carries SSHMCP administration. Any future restart requires an independent administration path and an approved rollback watchdog. The rollback plan rebuilds the already-authorized baseline and removes only shopping_origin, without live config/binary/database backup copies. Private config has not been read/exported. See docs/DEPLOYMENT-PLAN.md and deploy/oauth-compat/README.md.

The human confirmed a working direct SSH administration path independent of SSHMCP on October 19. This is user-reported availability; this local workflow did not open or verify that connection. No gateway restart or other live write was authorized by the confirmation.

## Next action and authorization boundary

Translation is complete and the no-backup implementation is frozen for coordinator final checks and fresh independent Review. After approval, commit and rebuild/test/scan exact replacement application images; version0.1.0-beta.1 may remain the candidate version with new commit metadata. Keep CP03/06/10 open until live acceptance actually passes.

User selections persist: https://shopping.vvivid.dev; own-account-only; Codex/ChatGPT; America/New_York, USD, Walmart, Monday for their own account. Actual shopping callbacks, artifact-transfer channel, optional background consent and remote repository remain inputs. No recovery target/key is required under the selected policy. Before any binary/config/restart, DNS, Keycloak, enrollment, secret, artifact transfer or deployment write, prepare the concrete change set and obtain live rollout authorization. Current local work grants no live writes.
