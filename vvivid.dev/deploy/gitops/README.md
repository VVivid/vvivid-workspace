# Website release artifacts and private GitOps

The [website workflow](../../../.github/workflows/website.yml) is the only active
project CI. Local implementation does not activate publication, reconciliation
or a website migration. The existing services remain available during preparation.

| Source | Result after activation | Base URL |
| --- | --- | --- |
| Pull request | Read-only native, policy, chart and real serving checks | Both URLs |
| `main` push | Checks only | Both URLs |
| `release/vvivid.dev/X.Y.Z` branch | Zot image alias `release-X.Y.Z`; staging desired state | `https://stage.vvivid.dev/` |
| `vvivid.dev/vX.Y.Z` annotated version tag | Zot image `X.Y.Z`; production desired state | `https://vvivid.dev/` |

Versions use canonical numeric SemVer without prerelease/build suffixes. The old
`release/vvivid-preview` branch has no workflow. Release branch pushes have no
path filter, so branch creation and workflow-only releases are checked too.
Publishing source branches/tags requires the user's explicit push authorization.

Images use `registry.lab.vvivid.dev/products/vvivid-website`. Every publication
also has a source/channel tag: `staging-X.Y.Z-sha-<commit>-run-<id>-<attempt>` or
`production-X.Y.Z-sha-<commit>`. Desired state always uses the exact `sha256`
digest, with source commit/ref and workflow URL. Staging aliases can advance;
source/channel tags are never reused by CI. A repeated staging run gets an
ordered unique chart version `X.Y.Z-staging.<run-id>.<attempt>`. Production chart
versions are `X.Y.Z`. Charts publish as OCI artifacts at
`oci://registry.lab.vvivid.dev/products/charts/vvivid-website`.

The workflow checks both native output and the actual Linux amd64 nginx image
before publishing. Runtime image and actions are pinned by digest/SHA. Staging
uses its own build URL, noindex metadata/headers and empty sitemap; CI never
submits IndexNow URLs. The image smoke check uses the Helm security settings.

## Desired-state transaction

[release.py](release.py) owns the policy; [publish.py](publish.py) publishes the
checked artifacts; [promote.py](promote.py) owns the fixed private Git transaction.
Only these files in `https://github.com/VVivid/homelab-gitops.git` may change:

```text
environments/websites/vvivid-dev/staging/fleet.yaml
environments/websites/vvivid-dev/production/fleet.yaml
```

They are JSON-formatted YAML. CI updates only `helm.version`,
`helm.values.image.digest` and `helm.values.provenance`. Operator-owned exposure,
certificate, namespace and resource settings survive every update. Promotion
fetches current main, checks the source ref still resolves to the expected commit,
compares release order, and creates a one-file commit. A non-fast-forward push
fetches fresh main and rebuilds the change, preserving concurrent unrelated work.
It rejects a newer staging run/version, production version reuse/downgrade,
malformed state and recorded-release digest substitution. It never force-pushes.

Publication checks the registry before each release, rejects existing source/chart
versions and production image tags, and verifies the published image digest against
Docker's pushed digest. Ref movement or registry/TLS/auth failures stop the run.
Partial publication deliberately leaves the already-pushed artifacts intact;
inspect them and use a new production version rather than overwriting/reusing it.
A GitOps update is recorded only after publication succeeds.

The shared Zot push account can technically mutate tags. CI's no-reuse policy and
serialization are the immutability contract for chart/source tags; Zot does not
currently enforce tag immutability against other authorized writers. Images remain
digest-pinned. Fleet fetches the exact chart version; its recorded OCI manifest
digest supports audit, but Fleet's Helm interface does not pin that manifest by
digest. Preserve published chart versions and restrict access to push credentials.
This preparation does not change Zot ACLs or promise protection from an authorized
writer replacing a chart tag outside this pipeline.

## Activation inputs and credential boundaries

Activation is separate authorized work. Keep the repository variable
`WEBSITE_GITOPS_ENABLED` unset/false until the private GitOps main branch and
reviewed configuration are published and all prerequisites are ready. Before
enabling it, configure one protected GitHub environment `artifact-publication`
with the required reviewers/release controls. For staging-only activation, allow
only approved `release/vvivid.dev/*` branches. Add `vvivid.dev/v*` tags only under
later production authorization. Do not allow arbitrary branches into it.

| Environment configuration | Purpose |
| --- | --- |
| Vars `TS_CLIENT_ID`, `TS_AUDIENCE`, `TS_CI_TAG` | The one existing monorepo Tailscale GitHub OIDC CI trust |
| Var `ZOT_PRODUCTS_PUSH_USERNAME`, secret `ZOT_PRODUCTS_PUSH_PASSWORD` | Existing products-only publication account |
| Var `GITOPS_APP_CLIENT_ID`, secret `GITOPS_APP_PRIVATE_KEY` | Dedicated GitHub App installed only on private `VVivid/homelab-gitops` |

The App needs repository Contents write; the action mints an installation token
restricted to `homelab-gitops` and revokes it at job completion. It is separate
from the workflow's read-only `GITHUB_TOKEN`, which only verifies the source ref.
Do not reuse developer SSH keys, connector tokens or operator credentials.
Git uses temporary owner-only askpass scripts and tokens in the environment;
registry passwords go over stdin into temporary owner-only credential files,
which are removed after the publishing step. No token is placed in Git URLs or
command arguments.

Reuse **one existing** Tailscale CI trust for all future monorepo service jobs.
Choose its existing approved CI tag and set `TS_CI_TAG` to that value; do not add
a per-project/repository client or broaden tailnet ACLs. At activation, query the
source repository's actual GitHub `sub_claim_prefix` (it may use immutable IDs),
then update the chosen existing trust once to the exact subject for the shared
`artifact-publication` environment. Keep its existing audience. Verify authorized
CI can reach only the required registry endpoint before retiring obsolete trust
configuration. Nothing in this local change performs that identity update.

GitHub environment secrets/vars belong to the caller repository. If a future
service invokes a reusable workflow from another repository, that workflow does
not inherit configuration from its defining repository. Supply approved inputs
and explicit secrets from the caller, and account for the caller's actual OIDC
subject. Reuse the current monorepo identity here; adding callers outside it is a
separate trust decision. A shared environment is not an authorization bypass.

GitOps branch protection must permit the scoped App's reviewed automation writes
to main (or a separately designed PR flow). A protected-branch rejection stops the
helper. The user must explicitly authorize **future automated GitOps commits and
pushes** at activation; local plan approval does not grant that authorization.

The private homelab repository owns `clusters/personal-prod-2/gitrepo.yaml`,
namespace/certificate preparation, Fleet read credentials and staged host cutover.
Its bootstrap stays paused, desired versions/digests stay `UNCONFIGURED`, and no
existing manual services are adopted during preparation. Staging activation selects
only `foundation/staging` and `staging`, serving `https://stage.vvivid.dev/` in
`website-staging`. The production foundation/bundle remain unenrolled until a
separate production request. Downstream image-pull, Fleet Helm-read and
Git-read credentials are separate from the CI publishing/App credentials.

## Local checks

From the source root, with pinned Hugo Extended and Helm on PATH:

```sh
make -C vvivid.dev check chart-test
docker build --platform linux/amd64 -t vvivid-website:production ./vvivid.dev
./vvivid.dev/scripts/smoke-image.sh vvivid-website:production https://vvivid.dev/
docker build --platform linux/amd64 -t vvivid-website:staging \
  --build-arg HUGO_BASEURL=https://stage.vvivid.dev/ ./vvivid.dev
./vvivid.dev/scripts/smoke-image.sh vvivid-website:staging https://stage.vvivid.dev/
```

Promotion tests use temporary local bare repositories, including concurrent
unrelated writes, competing newer deployments, changed refs and protected-branch
failures. Chart checks use explicit fixture-only digests, lint/render both URLs,
reject the unconfigured defaults and reject public ingress before its certificate
gate. They do not contact an external Git remote, registry or deployment server.
Use `make -C vvivid.dev legacy-deployment-test` only to test the retained archived
SSH helper with fake commands. Its former workflow assertions were removed.

For rollback, pause reconciliation/publication, inspect the last known-good
GitOps revision and exact recorded artifacts, then make a separately reviewed
operator change to desired state. Never move a source version tag, overwrite an
existing production/chart version or ask the CI helper to downgrade a release.
Public DNS rollback and retained old hosting belong to the homelab runbook.

## Primary contracts

- [Fleet OCI/Helm bundle configuration](https://fleet.rancher.io/reference/ref-fleet-yaml).
- [Fleet private GitRepo and Helm credentials](https://fleet.rancher.io/how-tos-for-users/gitrepo-add).
- [Helm OCI registries](https://helm.sh/docs/topics/registries/).
- [Tailscale GitHub Actions OIDC](https://tailscale.com/kb/1276/tailscale-github-action).
- [GitHub App installation token action](https://github.com/actions/create-github-app-token).
