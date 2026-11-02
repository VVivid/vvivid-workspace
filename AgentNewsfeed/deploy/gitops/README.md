# AgentNewsfeed production artifacts and Fleet contract

Prepared source only: no publication, rollout, identity/tunnel change or DNS cutover
is established by these files. Production browser origin is
`https://newsfeed.vvivid.dev`; the existing R&D realm provides owner login.
The [homelab runbook](https://github.com/VVivid/homelab-gitops/tree/main/environments/rd/agent-newsfeed)
owns private setup, readiness, enrollment, acceptance, fresh-storage transition,
tunnel reuse, cutover and recovery. Production starts empty; old content is not migrated.

## Release identity

Only a canonical numeric SemVer tag **`AgentNewsfeed/vX.Y.Z`** publishes.
`main`, PRs, release branches, prereleases, build metadata, leading-zero versions
and other project tags cannot publish. `0.1.0` is the intended initial version;
recheck source tags, image versions and chart versions before using it. Tags
identify a verified merged commit and require separately authorized publication.
The [dedicated workflow](../../../.github/workflows/newsfeed.yml) checks PR/main/
tag pushes, including Go/browser/race/build, synthetic Compose, real Linux amd64
container, chart schema/routing/security, and release/promotion policy tests.

Artifacts are independent of website releases:

| Artifact | Immutable identity |
| --- | --- |
| Image | `registry.lab.vvivid.dev/products/agent-newsfeed@sha256:...` |
| Image tags | `X.Y.Z`, `production-X.Y.Z-sha-COMMIT` (both must be unused) |
| OCI chart | `oci://registry.lab.vvivid.dev/products/charts/agent-newsfeed`, version `X.Y.Z` |
| Desired state | `VVivid/homelab-gitops:environments/rd/agent-newsfeed/production/fleet.yaml` |

The workflow builds Linux **amd64** from only this project, embeds frontend
assets, checks the real candidate before upload and records source ref/commit,
workflow run/attempt, image digest, chart version and OCI chart manifest digest.
The image remains nonroot UID65532 with a read-only root; no frontend runtime or
sibling Go/filesystem dependency is introduced. Dependency pins are unchanged.
The existing MCP adapter version starts at 0.1.0; production binary version is
set from the actual tag.

`publish.py` rejects existing image/version/chart tags, checks remote source
identity before uploads and after publication, compares registry image digest
to Docker's recorded repository digest, compares Helm's uploaded chart digest
to registry HEAD, and verifies both final image tags reference that exact image.
Registry HTTP redirects are rejected so credentials are not forwarded elsewhere.
A partially published release remains for audit; use a new version after
inspection. Never mutate/reuse its tags to repair a failed run. The registry's
server-side immutability is not claimed; authorized writers must preserve chart
versions. Fleet consumes an exact chart **version** and digest-pinned image;
chart manifest digest is audit provenance.

`promote.py` has no CLI target/repository/path override. It rechecks the exact
source ref, fetches fresh private `main`, changes only the fixed JSON-formatted
YAML file and commits only these release fields:

- `helm.version`
- `helm.values.image.digest`
- `helm.values.provenance`

Hostname, namespace, release/chart/image identity and retention policy drift
fail closed. Existing release/provenance consistency is checked. Exact repeated
promotion is a no-op; changed digests for the same run, version reuse, source
substitution and downgrades are rejected. Normal non-fast-forward races retry
from fresh main, rechecking source/order each time and preserving operator TLS,
resources, maintenance, retained claim selection and unrelated files. Authentication, protection
and transport failures stop. Tests perform actual transactions against temporary
local bare Git remotes; no hosted credentials or services are used.

## Shared CI activation

Leave `NEWSFEED_GITOPS_ENABLED` false/unset until approved. Reuse protected
`artifact-publication`, existing `TS_CLIENT_ID`, `TS_AUDIENCE`, `TS_CI_TAG`, Zot
push credentials, GitOps-only App and `monorepo-artifact-publication` lock.
Later add `AgentNewsfeed/v*` to environment deployment rules **alongside** existing
website release branches. Do not modify Tailscale OIDC trust or use retained
operator credentials as CI inputs. PRs and main have no publication access.
The scoped App writes only the private GitOps repository; Fleet/pods independently
use existing read credentials. Normal releases do not need new DNS, secrets,
OIDC subjects, tunnel registration or enrollment changes.

The existing Fleet GitRepo remains on its two active website staging paths.
Only the separate paused newsfeed enrollment patch adds the exact new app path
and chart regex. Never select the R&D root or adopt its existing namespace,
foundation, identity, gateway, demo workloads or tunnel into Fleet. Enrollment
stays paused until real artifacts, credentials, resources and namespace readiness
are verified. This source change does not activate that patch.

## Runtime and initial exposure

[chart/](../../chart) creates only new app resources: single `Recreate`
Deployment, ClusterIP Service, retained 5Gi `rd-local-retain` claim, explicit
Traefik/gateway NetworkPolicy, private exact MCP/discovery ingress, certificates
and gated browser ingress. It references the existing namespace ServiceAccount,
registry pull Secret, shared gateway public JWKS ConfigMap and R&D ACME Issuers.
It never creates shared Namespace/Secret/Issuer/quota/gateway/tunnel resources.
Requests are 100m CPU/128Mi; limits are 500m/512Mi. Identity Secret projection is visible
only to init; copies in memory are UID65532/exact 0600 and read-only to the app.
Only `/data` is writable by the main container. Keep session keys stable. Set operator-owned `maintenance=true` to quiesce the single writer through desired state for offline enrollment or backup; restore false afterward.

The chart requests staging DNS-01 first with public ingress absent; enable
production issuance only after observed staging readiness, then enable browser
ingress only after observed trusted production TLS. Values cannot establish
actual certificate readiness. Public ingress permits only `/`, archive, exact
login/callback/logout, article API/reader, static assets and thumbnails. It
excludes `/mcp`, all MCP discovery and health. Canonical Host is preserved.
Private exact `/agent-newsfeed/mcp` and its protected-resource metadata go to
`rd-gateway` on `platform-tls` with existing private TLS.

The owning homelab gateway route forwards to ClusterIP `/mcp` with Host
`newsfeed.vvivid.dev`, backend audience `agent-newsfeed-mcp`, allowed client
`newsfeed-chatgpt`, required read and forwarded read/write permissions, and
external lifetime <=300 seconds. The app verifies the independent signed gateway
identity contract; browser/session auth stays direct OIDC. See the
[authentication configuration](../../README.md#mcp-gateway-authentication-mode).

Use a disposable operator-selected `agent-newsfeed-acceptance` claim for writes,
refresh, actual ChatGPT tools, isolation, idempotency and restart persistence.
After acceptance, select a new `agent-newsfeed-data` claim, preserve the old claim/PV
and session Secret, enroll only the verified existing R&D owner offline, and
verify final active/archive feeds are empty. CI preserves the storage selection.
No seeding or source-data migration is part of production preparation.

The user chose the **existing tunnel**, not another registration/workload. After
backend acceptance, change only its owning manual main target to private Newsfeed,
keep its Personal org/workspace/runtime/key/Secret, retire the old demo ChatGPT
connection and create a fresh static OAuth Newsfeed connection. Demo gateway
backends/routes remain. Client callback must be verified from the actual form;
currently observed `https://chatgpt.com/connector_platform_oauth_redirect`.
Select User-Defined OAuth Client with S256 and `client_secret_post`, disable OIDC
email/domain claiming and DCR. Browser uses `client_secret_basic`. Use external
resource indicators exactly `https://newsfeed.vvivid.dev/api/v1` and
`https://mcp.rd.lab.vvivid.dev/agent-newsfeed/mcp`; actual five-minute grants and
refresh must be verified. Never import/replace the existing realm/users/database.

Trusted `curl --resolve` and browser hostname override to `13.140.235.116` precede
any DNS change. After actual-client/browser/storage and private Rancher checks,
only `newsfeed.vvivid.dev` DNS-only A may change from `144.91.70.238` to
`13.140.235.116`, TTL300, with separately authorized cutover. Keep the old app/data
seven days. No old-server operations are part of this source preparation.

## Local validation and reading order

```sh
make -C AgentNewsfeed check race build compose-check container-build container-test
python3 -m venv /tmp/newsfeed-checks
/tmp/newsfeed-checks/bin/pip install -r AgentNewsfeed/deploy/gitops/requirements.txt
make -C AgentNewsfeed chart-test PYTHON=/tmp/newsfeed-checks/bin/python
# Set HELM=/absolute/path/to/helm if Helm is not on PATH.
actionlint .github/workflows/newsfeed.yml
```

Read [release.py](release.py), [publish.py](publish.py), [promote.py](promote.py),
[chart schema](../../chart/values.schema.json),
[app gateway verifier](../../internal/identity/gateway.go) and the owning homelab
runbook. Tests use nondeployable synthetic digests/keys, temporary Git repositories,
throwaway SQLite/volumes and loopback provider fixtures. They do not establish
real provider enrollment, hosted CI publication, cluster isolation, TLS, ChatGPT,
DNS or backup/restore acceptance. Operator rollback remains an explicit reviewed
GitOps/data-aware change; normal CI deliberately rejects downgrades.
