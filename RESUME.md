# Resume AgentShoppingList

Updated October19, 2026, America/New_York.

## Current state

Project: `/Users/oudavid/works/opensource/AgentShoppingList`. Git `main`, last approved commit `746ef30` (sourced purchases, immutable history and derived insights), earlier `21d4f35`/`a3f8c76`; no remote. Current CP09/10 release changes are uncommitted. Preserve them and all data. No deployment has occurred; v1 is not beta-ready.

CP01/02/04/05/07/08 have local independent approval. CP03 live Keycloak and CP06 actual Codex/ChatGPT/proxy acceptance remain pending. CP09 is complete locally. CP10 local release artifacts completed Builder, Translation, coordinator final validation and fresh independent Review Approved; live acceptance remains. Continue the full deployable daily-use goal after each checkpoint, within authorization.

Go1.26.9 serves four compact Go template views, a narrow browser API and16 official SDKv1.8.0 MCP tools over shared owner/scope-checked services. Source-backed purchase/history/insight behavior at746ef30 remains preserved. No production fixtures, public identity bypass or enrolled live account is seeded.

## Read in order

1. AGENTS.md, this file and CHECKPOINTS.md.
2. REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md.
3. docs/DOMAIN.md, docs/AUTH.md, docs/RUNBOOK.md and docs/DEPENDENCIES.md.
4. docs/validation/CP09-CP10-2026-10-19.md and the prior CP07 result-correction evidence.

## Current release slice

- Operational readiness checks configured identity plus available database/current complete migrations; an unconfigured shell returns503. Liveness remains200 through transient issuer outages and persistence failure while HTTP serves.
- Build metadata identifies version/commit/time/Go/OS/architecture. Go pins moved1.26.5→1.26.9 following current advisory findings. Multi-stage Docker pins Go1.26.9, Node24.22.0/Tailwind4.3.0 and distroless static-debian13 nonroot by immutable registry digest. Production assets/migrations are embedded; no Node or test fixture is in production commands.
- Credential-free deploy/compose.yaml requires exact existing network/entrypoint/resolver/client/issuer/image/host-secret values, uses one persistent named volume,65532, a read-only root, read-only secret mounts and no published app port. It has not been applied.
- shopping-admin backup/verify/restore/inspect use the maintained SQLite online backup API, private atomic no-overwrite tar artifacts, migration/checksum/integrity/FK/hash/count/confirmed-total verification and isolated restoration without automatic migration. Existing-schema startup first creates/verifies a pre-migration archive; it refuses to migrate if backup fails. All known old prefixes are supported unchanged for recovery; newer/drifted schemas fail.
- Runbook records rollback limits, session/recovery key requirements, proposed7daily/4weekly/6monthly encrypted off-server retention and RPO24h/RTO1h targets. Those targets/off-server storage/scheduling/drill remain unverified and unconfigured.

## Actual local validation

- Full Go1.26 Modern Go Guidelines list read before edits. make fmt assets check build and JS syntax passed on Go1.26.9; ordinary tests intentionally skip the explicit whole-process test unless opted in.
- Recovery tests passed active WAL with concurrent atomic writers, retained sessions/idempotency/audit and exact null/zero/known totals, corrupt/truncated/path/hash/count/FK/newer/overwrite failures, old-schema restore without migration and pre-migration snapshot.
- Actual host production-binary journey passed with synthetic OIDC+PKCE browser login, two owners, official SDK Polar/check/no spend/confirmation/History/Insights, graceful restart, committed response loss+SIGKILL/concurrent replay, writer-blocked precommit interruption/retry, durable sessions/preferences and CLI backup/isolated restore.
- Actual local Linuxarm64 image passed that same journey in a read-only/nonroot --network none container, then isolated restore and real Docker stop/start with identical durable counts/totals. Final matching Linuxarm64 and Linuxamd64 candidates were rebuilt with source746ef30-dirty, version0.1.0-local and built_at2026-10-19T06:14:33Z. Both passed final process/recovery/container stop-start acceptance; amd64 ran under Docker Desktop emulation. Exact image IDs are in the evidence note. No test resources/listeners remain.
- Relevant race suite passed web/shopping/SQLite/identity/admin. Coordinator current-release source govulncheckv1.9.0 returned zero findings with patched Go1.26.9; npm audit returned zero. These are not a final image/base-package advisory scan.

Use `GOTOOLCHAIN=go1.26.9 GOMODCACHE=$PWD/.cache/go-mod GOCACHE=$PWD/.cache/go-build`. Loopback/Docker tests need scoped local execution permission. Tests own only temporary synthetic listeners, processes and isolated Docker containers/volumes, and clean them up.

## Next action

Commit the independently approved local release code, then rebuild/test/scan images labeled with the exact final commit. Request bounded read-only production inventory authorization to resolve actual server architecture, existing Traefik names, live Keycloak client/callback/resource compatibility and private secret provisioning. Prepare a concrete shopping-only deployment change set from that inventory before requesting approval to apply it. Live CP03/06/10, actual phone/Codex/ChatGPT/SSH isolation, encrypted off-server backup and timed restore remain incomplete.

User chose https://shopping.vvivid.dev, own-account-only initial enrollment, Codex/ChatGPT and own preferences America/New_York, USD, Walmart, Monday; these are account selections, not global defaults. Exact live subject/registrations/callbacks/refresh resource/proxy behavior, optional background consent, remote repository/server architecture and recovery targets still need input/acceptance. Historical October18 Keycloak observations are not current runtime proof.

Automatic approval review previously rejected production SSHMCP access as outside local authorization. It was not retried or bypassed. For this slice, automatic approval review rejected Docker Scout because it may send private image/package metadata to Docker without specifically authorized destination/payload. That scan was not run or bypassed; image/base-package advisory validation remains pending. Local image/build/test preparation continues. No live Keycloak, Traefik, DNS, enrollment, secret, server or deployment action occurred.

## Latest authoritative local approval

Fresh Review /root/release_review returned Approved with no actionable findings. Post-Translation coordinator full build/check/process acceptance and rebuilt arm64/amd64container suites passed. Safe offline pinnedTrivy0.76.0 reports0 known OS/Go vulnerabilities bothimages using2026-10-19publicDB; candidate containers had no network/socket/credentials. RejectedScout was never retried. SourcegovulncheckGo1.26.9/npm0. See latest CHECKPOINTS and validationnote for effective stageIDs/config and image/DBhashes. Next commit approvedcode then rebuild/test/scan finalcommitimages. Live production inventory/configuration/deployment/offserver work still requires bounded authorization; no live changes have occurred.
