# GitHub preview deployment

The [preview workflow](../../../.github/workflows/website-preview.yml) maps only
`release/vvivid-preview` to `https://preview.vvivid.dev/`. The
[live workflow](../../../.github/workflows/website.yml) continues to map `main`
to `https://vvivid.dev/`. Pull requests check both complete serving images with
read-only repository permissions; they cannot publish or deploy.

Every push to the exact preview branch builds and checks a Linux amd64 image with
`HUGO_BASEURL=https://preview.vvivid.dev/`, publishes
`ghcr.io/vvivid/vvivid-website:preview-sha-<commit>`, and deploys its immutable
`sha256` digest. Live uses `sha-<commit>` so images built from the same commit
with different URLs cannot overwrite each other's tag. Preview pushes have no
path filter: the first branch push and workflow-only updates must run even if
the website files match `main`. Unrelated monorepo pushes to this branch also
build. PR checks watch the site and both website workflow files.

These files prepare activation; they do not establish installed access or a
successful deployment. Publication, creation/update of `release/vvivid-preview`,
GitHub/Tailscale/server changes, and activation require their corresponding
operational authorization. Keep private addresses, keys, and endpoints in runtime
configuration.

## Environment and Tailscale identity

Create the `vvivid-preview` GitHub environment in `VVivid/vvivid-workspace`.
Allow **only** the branch `release/vvivid-preview`, with no tags or branch glob.
Keep a required reviewer for the first deployment until server installation and
unauthenticated package pull are verified. Removing that review rule after
activation enables automatic deployment on subsequent preview pushes. Protect
and review changes to the release branch and workflows accordingly.

Use separate preview values on this environment; do not reuse live credentials:

| Kind | Name | Value |
| --- | --- | --- |
| Variable | `TS_CLIENT_ID` | Preview-only Tailscale federated credential client ID |
| Variable | `TS_AUDIENCE` | Exact audience generated for that credential |
| Variable | `VVIVID_DEPLOY_HOST` | Server MagicDNS hostname or Tailscale address |
| Variable | `VVIVID_DEPLOY_PORT` | Existing SSH port |
| Secret | `VVIVID_DEPLOY_SSH_KEY` | Dedicated preview Ed25519 private key without passphrase |
| Secret | `VVIVID_DEPLOY_KNOWN_HOSTS` | Independently verified `[hostname]:port` SSH host-key line |

Follow the [live package and host-key procedure](../live/README.md#github-environment-and-package):
the package must be public and the server must be able to pull the exact digest
without registry-write credentials. Verify the host key through the trusted
administrator connection; the workflow never learns it with `ssh-keyscan`.

Create a **separate** Tailscale OIDC trust credential with these constraints:

| Field | Value |
| --- | --- |
| Issuer | GitHub / `https://token.actions.githubusercontent.com` |
| Subject | `repo:VVivid/vvivid-workspace:environment:vvivid-preview` |
| Custom claim `repository` | `VVivid/vvivid-workspace` |
| Custom claim `ref` | `refs/heads/release/vvivid-preview` |
| Custom claim `workflow_ref` | `VVivid/vvivid-workspace/.github/workflows/website-preview.yml@refs/heads/release/vvivid-preview` |
| Scope | Writable `auth_keys` only |
| Tag | `tag:vvivid-preview-ci` |

Save its generated client ID and audience in the preview environment. No
reusable Tailscale secret or auth key is needed. Merge a narrow grant for
`tag:vvivid-preview-ci` to only this server's existing SSH TCP port, with the
appropriate administrator tag owner. Follow the
[live policy example](../live/README.md#tailscale-federation-and-policy), replacing
only the new identity's values. Inspect additive grants so preview does not
inherit unrelated access. Preserve live and administrator grants. This is
ordinary OpenSSH over Tailscale, not Tailscale SSH.

## Server installation and shared-lock migration

The server prerequisites and fixed infrastructure path are the same as
[live](../live/README.md#restricted-server-identity): Python 3, Docker Compose,
curl, OpenSSH, sudo, and a local filesystem supporting `flock`. Both helpers use
`/home/admin/infrastructure/compose.yml`, its sibling `.env`, and Compose project
`private-edge`. Preview fixes service `vvivid-preview`, key
`VVIVID_PREVIEW_IMAGE`, and health host `preview.vvivid.dev`. It cannot select live
or another repository from SSH arguments.

Before replacing helpers, hold **both** GitHub deployment environments and wait
for all active/queued server deployments to finish. Keep the old GitLab live job
disabled. Disable and drain the old GitLab preview deployment job before preview
cutover; it does not participate in the new lock. In that external project's
configuration, set only `deploy_preview.rules` to `[{when: never}]`, use `[skip ci]`
for a configuration-only cutover commit, and cancel only preview deployments.
Do not restart the old runner or modify its unrelated workloads. Publication to
that old repository needs explicit permission for its ref.

**Update the installed live helper before enabling preview.** Earlier live
versions used `/run/lock/vvivid-live.lock`. Both current installations must use
`/run/lock/vvivid-deployment.lock`, held from baseline capture through final health
or rollback. Different per-service locks allow two read/replace operations on
the shared `.env` to erase each other's updates. An in-flight old helper keeps
its old lock even after its file is replaced, which is why deployments must be
drained first. Manual writers and legacy scripts must also be held or coordinated;
the lock cannot serialize programs that ignore it.

Create the separate restricted account:

```sh
sudo useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /bin/sh vvivid-preview-deploy
sudo passwd --lock vvivid-preview-deploy
```

Give it no writable login home and no `docker`, `sudo`, or administrator group
membership. From this directory in a trusted reviewed checkout, install:

```sh
sudo install -d -o root -g root -m 755 /usr/local/libexec /etc/vvivid-preview /etc/ssh/authorized_keys
sudo install -o root -g root -m 755 ../live/deploy.py /usr/local/sbin/vvivid-live-deploy
sudo install -o root -g root -m 755 ../live/deploy.py /usr/local/sbin/vvivid-preview-deploy
sudo install -o root -g root -m 755 ssh-entrypoint.py /usr/local/libexec/vvivid-preview-ssh
```

Both deployments share the reviewed [helper source](../live/deploy.py). The
root-owned executable basename fixes its scope; install it under exactly these
names. Keep separate files at root-owned paths, not symlinks into writable
checkouts. The live SSH entrypoint and account remain unchanged.

Create root-owned mode `0644`
`/etc/ssh/authorized_keys/vvivid-preview-deploy` with only the preview public key:

```text
restrict,command="/usr/local/libexec/vvivid-preview-ssh" ssh-ed25519 <preview-public-key> vvivid-preview-github
```

Install [sshd-vvivid-preview.conf.example](sshd-vvivid-preview.conf.example) as
`/etc/ssh/sshd_config.d/90-vvivid-preview.conf` if the main SSH config includes
that directory. Validate with `sudo sshd -t`, inspect effective
`sshd -T -C user=vvivid-preview-deploy,host=localhost,addr=<runner-tailnet-address>`,
then reload SSH while retaining the trusted admin session. Validate
[sudoers-vvivid-preview.example](sudoers-vvivid-preview.example) with
`sudo visudo -cf <staged-file>` and install as `/etc/sudoers.d/vvivid-preview`,
root-owned mode `0440`. The account must have no additional sudo rules.

Create `/etc/vvivid-preview/health-url`, root-owned mode `0600`, containing the
actual server-local Traefik HTTP origin. Keep the live health file unchanged.
The preview helper sends `Host: preview.vvivid.dev` locally and also requires
HTTP 200 from public `https://preview.vvivid.dev/`; redirects and unavailable
public routing fail deployment.

## Existing Compose service and first deployment

Record both services' current image references/IDs and dedicated settings. Add
only the preview image override to the **existing** `vvivid-preview` service.
[compose-preview-image.example.yml](compose-preview-image.example.yml) uses:

```yaml
image: ${VVIVID_PREVIEW_IMAGE:-${VVIVID_IMAGE}:preview}
```

The fallback matches the archived pipeline's `:preview` producer; the current
server Compose file is not in this repository. Preserve the actual old preview
image expression as the fallback if it differs. Keep labels, networks, options,
container name, `VVIVID_IMAGE`, live image expression, and live settings intact.
Leave `VVIVID_PREVIEW_IMAGE` absent initially. Compare normalized
`docker compose --project-name private-edge --env-file
/home/admin/infrastructure/.env --file /home/admin/infrastructure/compose.yml
config --format json` before and after: without the key, all configuration must
match; with a temporary `VVIVID_PREVIEW_IMAGE` digest override, only
`services.vvivid-preview.image` may differ. Configuration validation does not
recreate a service.

After the workflow is present on the exact release branch, its first authorized
push runs even when the site tree is unchanged. First publish the checked image,
confirm package access, installed helper versions, SSH rejection cases, branch
restriction, and both baseline health checks; then release preview's environment
approval. Keep the previous preview image available for rollback. Do not run
legacy cleanup or registry cleanup during cutover.

Activation evidence is the preview container's exact digest and actual Docker
image ID, local preview Host HTTP 200, public preview HTTPS 200, preview noindex
headers/metadata and empty sitemap, and unchanged live image/container state.
The [search runbook](../seo/README.md) covers separate edge cache/header changes;
image checks cannot establish CDN behavior. Preview remains publicly readable.
No workflow submits IndexNow; the production-only helper requires its own
explicit invocation after verified production activation.

## Deployment and recovery

GitHub serializes preview jobs without cancelling an active deployment. The gate
compares the candidate website tree and preview workflow with the current exact
release branch. Newer site/workflow changes skip an older queued candidate;
unrelated monorepo commits do not invalidate it. API errors fail closed. Live
has its own GitHub concurrency group; both meet at the shared server lock.

Under that lock, preview captures its prior assignment and running image/ID,
refuses an out-of-sync baseline or missing Compose override, and pulls the exact
GHCR digest before changing any setting. It atomically replaces only
`VVIVID_PREVIEW_IMAGE`, preserving ownership, mode, and live settings. It runs
`compose up --detach --no-deps --pull never vvivid-preview`. Health verifies both
the configured image reference and actual Docker image ID, then local/public
HTTP 200. A cached public 200 cannot mask a wrong running image.

Recreation or health failure and handled interruptions restore the previous
preview assignment (including an absent key) and recreate only preview with its
previous image. Rollback also verifies image/ID and local/public health while
holding the shared lock. The workflow fails even after successful rollback.
`ROLLBACK FAILED` requires administrator intervention. An abrupt host failure or
uncatchable kill can interrupt recovery: inspect persisted key, actual image and
health before retrying. Never restore a whole old `.env` over newer live values.

For an intentional rollback, run **vvivid.dev preview** from branch
`release/vvivid-preview`, supplying a previously checked **preview-built** GHCR
`sha256:<digest>` as `image_digest`. Record the source base URL with each digest;
the server restricts the repository, not build provenance. A digest input skips
build/publish and uses the same identity, lock and health checks. Empty input
rebuilds the selected release branch. Dispatches on other branches/tags cannot
publish or deploy. This does not promote preview to live; promotion remains a
reviewed change to `main`, which builds with the production base URL.

To withdraw access, block only the preview environment, revoke its Tailscale
credential, and remove its SSH key/sudo rule/account configuration. That leaves
the current container running. Reverting the preview image/Compose expression
or re-enabling an old pipeline is a separate coordinated administrator action.
Retain the shared-lock live helper while either deployment can use the shared
`.env`.

## Local checks

```sh
make -C vvivid.dev deployment-test
HUGO=/absolute/path/to/pinned/hugo make website-check website-test website-build
docker build --platform linux/amd64 --tag vvivid-website:preview-check \
  --build-arg HUGO_BASEURL=https://preview.vvivid.dev/ ./vvivid.dev
./vvivid.dev/scripts/smoke-image.sh vvivid-website:preview-check https://preview.vvivid.dev/
```

The standard-library deployment suite exercises both installation scopes with
fake Docker/HTTP/GitHub commands, plus real interprocess locking and temporary
`.env` replacements, including preview rollback alongside a live update. Native
checks build both production and preview outputs. Image smoke checks verify the
expected preview URL, noindex metadata/headers, crawlable robots without a sitemap
advertisement, empty sitemap, representation contracts and guide exclusion.
They start and remove only temporary local containers and never contact the server.
