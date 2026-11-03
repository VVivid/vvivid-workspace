# Shopping production publication

The project owns [its chart](../../chart/Chart.yaml),
[CI](../../../.github/workflows/shopping.yml) and production artifacts. The
homelab repository owns operator values, identity, retained data and cutover at
`environments/rd/agent-shopping-list/`. Local preparation does not publish or
activate any service. No app, dependency, database or domain upgrade is included.

## Checks and release identity

PR/main checks install the committed npm lockfile, build CSS, run browser tests,
Go vet/tests/race/build, chart/schema/release/publication tests and the real Linux
amd64 production container journey. The existing container harness uses only
synthetic test data on a disposable Docker volume and no external network.

```sh
npm ci --ignore-scripts
make assets check race build
make chart-test PYTHON=/absolute/path/to/check-venv/bin/python
# requirements are pinned in deploy/gitops/requirements.txt; Helm 4.3.1 required.
docker build --platform linux/amd64 --tag agentshoppinglist:local .
make container-test
```

Only canonical `AgentShoppingList/vX.Y.Z` tags publish. The first candidate is
`AgentShoppingList/v0.1.0`; verify the tag and both registry repositories are
unused before an authorized release. Project versions are independent. Main
pushes and PRs only check. Tag creation/push, source merging, publication and
live migration retain their separate authorization requirements.

The publication job also requires `SHOPPING_GITOPS_ENABLED=true`, the existing
`artifact-publication` environment and `monorepo-artifact-publication` lock.
It uses the existing Tailscale GitHub OIDC identity, `TS_CLIENT_ID`, `TS_AUDIENCE`,
`TS_CI_TAG`, products push identity and GitOps-only GitHub App, without adding
trust subjects or a broad source write token. The App token is restricted to
`VVivid/homelab-gitops` contents writes. Website and Newsfeed publication use
the same lock and keep their own release identities.

## Artifact and desired-state transaction

1. Parse a canonical tag and valid commit/run identity; require its source commit
   to be merged into current `origin/main`. Build Linux amd64 with exact source
   ref/SHA/origin labels and pass the existing container acceptance harness.
2. Revalidate the remote source tag before each publication boundary. Refuse any
   existing `products/agent-shopping-list:production-X.Y.Z-sha-SHA`, image version
   or `products/charts/agent-shopping-list:X.Y.Z` tag. Partial publications remain
   audit evidence and require a new version; retries cannot overwrite them.
3. Publish the checked image, verify its manifest digest against Docker, package
   the app chart at the same project version, and verify the reported OCI chart
   digest against Zot. Publish the version image alias only after those checks.
4. Revalidate source, fetch fresh GitOps main and change only
   `environments/rd/agent-shopping-list/production/fleet.yaml`: Helm version,
   image digest and complete provenance. Preserve maintenance, claim, resources,
   ingress/TLS gates and every other operator value. A repeated identical release
   is a no-op. Reused/downgraded versions, substituted digests, moved tags and
   malformed target/provenance fail closed.
5. Push a normal one-file commit. On a competing main update, start over against
   fresh main, preserve operator work and revalidate source/version ordering.
   Authentication/protection/transport failures stop; no force push or rollback.

Credentials pass through protected temporary Docker/Helm configs, stdin and Git
askpass, and never enter URLs or argv. Registry credential redirects are rejected.
The CLI has no target remote/path override. Offline tests cover real local Git
races, source movement, digest substitution, immutable tags and exact field scope.

## Runtime ownership and acceptance

The chart owns one Recreate Shopping Deployment, ClusterIP Service, ingress-only
NetworkPolicy, retained 5Gi `rd-local-retain` PVC, new app certificates and routes.
It references the existing `rd-workload`, `rd-registry-pull`, pinned public gateway
JWKS and protected Shopping identity/session Secret. A nonroot init copies
projected secrets to private 0600 memory files and creates the 0700 SQLite child;
the application enforces 0600 database mode. Maintenance sets zero replicas while
retaining the claim. The chart does not own the namespace, Keycloak, gateway,
tunnel, shared signing keys, service account or issuers.

The public browser allowlist preserves `/`, Products, History, Insights,
login/callback/logout, `/static/` (including `share.js`), `/api/v1/` and `/s/`
(including share reads and item PATCH requests). Public MCP, health and protected
resource discovery paths have no route. The fixed private MCP route uses only
`platform-tls` and the gateway; upstream Host remains `shopping.vvivid.dev`.
Public browser ingress requires both the production certificate gate and observed
trusted TLS. Rendering cannot establish certificate readiness.

Shopping production imports the existing data and opaque owner. Never use the
Newsfeed fresh-empty acceptance/enrollment procedure for this migration. Preserve
all tables and deliberate identity remapping in the homelab runbook. A separate
Shopping tunnel remains paused until a unique tunnel ID and actual ChatGPT
connection are registered; preserve Newsfeed's existing tunnel/main channel.
