# VocabBank production release contract

The [dedicated workflow](../../../.github/workflows/vocabbank.yml) checks matching
PRs and main pushes. Canonical `VocabBank/vX.Y.Z` tags also run checks. Main does
not publish. Each app owns its dependencies, runtime, chart and release; this
code does not import sibling application packages or adopt shared resources.

The check job installs the Go version from `go.mod`, Node 24, the locked
`web/package-lock.json`, Chromium dependencies, checksum-pinned Helm and isolated
check-only Python dependencies. It runs format/type/vet checks, Go tests/race,
production static export/build, all four browser regressions through the real Go
handler, post-build checks, chart/release/publication tests, and Linux amd64
Docker plus the actual nonroot executable's CLI checks. The image contains no
test issuer, login bypass, owner or credentials. Candidate CLI checks establish
private/exclusive key generation and real SQLite enrollment/repeat-start
integrity, not authenticated container HTTP or real-provider acceptance.

## One-time activation

After reviewed source is merged and publication is authorized:

1. Add only `VocabBank/v*` to the existing `artifact-publication` environment's
   permitted tag rules. Preserve every existing branch/tag rule and protection.
2. Set the **repository-level** Actions variable `VOCAB_GITOPS_ENABLED=true`.
   The job `if` is evaluated before environment variables are available.
3. Reuse the existing environment's `TS_CLIENT_ID`, `TS_AUDIENCE`, `TS_CI_TAG`,
   `ZOT_PRODUCTS_PUSH_USERNAME` and `GITOPS_APP_CLIENT_ID`, plus protected
   `ZOT_PRODUCTS_PUSH_PASSWORD` and `GITOPS_APP_PRIVATE_KEY`. The source read
   token is the job's normal read-only `GITHUB_TOKEN`. Do not create a new
   Tailscale client/trust relationship or widen the GitOps-only App's access.
4. Prepare the fixed private desired-state file before its first release.
   Its internally consistent `UNCONFIGURED` version/image/provenance is the only
   accepted initial state. Keep that app path unenrolled until actual artifacts,
   namespace credentials and operator prerequisites are ready.

The shared `monorepo-artifact-publication` concurrency lock serializes artifact
publication across apps. Registry publication uses products-push credentials;
Fleet Helm reads and namespace image pulls use separate products-read
credentials. Credentials are sent via password-stdin/protected temporary config
or askpass, never a URL or command-line value. There are no remote/path overrides
in the promotion CLI. Identity, TLS, public DNS, gateway and Fleet enrollment
remain separate operator actions in the private VocabBank runbook.

## Immutable release and promotion

Publish an unused annotated `VocabBank/vX.Y.Z` tag at a verified merged main
commit, after the repository's explicit push/release authorization. Numeric
versions have no leading zeros, prerelease suffix or build metadata. The plan
parser rejects every other ref; the workflow requires merged-main ancestry.
The source tag is revalidated before each upload and before each GitOps write.

`publish.py` inspects the already-tested Linux amd64 candidate's commit/ref/base
URL labels. It refuses existing source-image tags, release-image versions and
chart versions. It publishes only:

- `registry.lab.vvivid.dev/products/vocabbank:production-X.Y.Z-sha-COMMIT`;
- `registry.lab.vvivid.dev/products/vocabbank:X.Y.Z`;
- `oci://registry.lab.vvivid.dev/products/charts/vocabbank:X.Y.Z`.

It verifies registry image digests against local pushed RepoDigests, the OCI
chart digest against Helm's returned digest, and re-reads both image tags and
chart before recording metadata. Redirects of credential-bearing registry
requests fail closed. The image deploys by digest. The chart version is unique;
its manifest digest is recorded provenance, not a claim of server-side registry
tag immutability. Authorized registry writers must preserve published versions.

`promote.py` changes exactly Helm version, image digest and full provenance in
`environments/rd/vocabbank/production/fleet.yaml` in private
`VVivid/homelab-gitops`. Host, claim, maintenance, resources, identity and TLS/route
settings remain operator-owned. Initial configuration, exact idempotent retries,
newer version ordering, digest substitution, stale refs and target/retention
drift have explicit tests. A competing main update triggers a fresh fetch and
one-file commit, preserving concurrent operator work; a newer release blocks an
older retry. Normal push only; authentication/protection failures stop.

Partial publication is retained for audit and fails version-reuse checks; do not
rewrite tags or erase artifacts to hide it. Inspect the published digests and
use the fixed promoter with verified release metadata for a promotion-only
recovery, or prepare a new authorized version. No arbitrary target/path is
accepted. Rollback is an explicit operator action using prior known artifacts
and data-aware recovery, rather than disabling monotonic release policy.

## Local checks

```sh
python3 -m venv .cache/release-checks
.cache/release-checks/bin/pip install -r deploy/gitops/requirements.txt
make chart-test PYTHON="$PWD/.cache/release-checks/bin/python"
make docker-build container-test
```

Helm must be on PATH (or set `HELM` for the tests). Root entry points
`make vocab-chart-test vocab-container-test` delegate without running other apps.
The chart fails closed on release placeholders, drifted provenance/host/trust,
unknown settings, unsafe storage/resources, extra writers and public-route gates.
It owns only the new app ServiceAccount, Deployment, ClusterIP Service, retained
PVC, ingress NetworkPolicy and optional narrow browser Ingress in `vocabbank`.
Issuers/Certificates and the cross-namespace private gateway ingress belong to
the private setup. No Namespace, shared identity, gateway, registry, DNS,
Traefik, tunnel or shared R&D resources are adopted by Helm.
