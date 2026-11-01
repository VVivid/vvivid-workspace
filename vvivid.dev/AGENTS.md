# vvivid.dev agent guidance

Follow the [root instructions](../AGENTS.md) and shared workflow. This project owns the Hugo site, chart and release helpers. The [Fleet/Helm publication contract](deploy/gitops/README.md) owns active CI. The old [SSH live](deploy/live/README.md), [SSH preview](deploy/preview/README.md) and [GitLab](deploy/legacy/README.md) procedures are archived recovery references; do not activate them from this guide.

## Content and author

The portfolio represents Ou David, a systems engineer focused on reliable distributed software, backend engineering, Go, infrastructure, integrations, observability, data safety, and practical product engineering. Preserve the author's direct, experience-driven technical voice and point of view. Use the existing writing as the source for tone, background, and interests. Do not add unsupported credentials, claims, metrics, or experience.

When editing content, preserve URLs and established front matter unless the requested change requires otherwise. Improve clarity with concrete tradeoffs, failure modes, and examples. Content reviews should prioritize accuracy, clarity, credibility, and reader usefulness while retaining the author's personality.

## Build and dependency boundaries

Use Hugo extended at the version in `.hugo-version`. Run `./scripts/build.sh` for the production build and agent-guide exclusion check. Override the binary with `HUGO=/absolute/path/to/hugo`; `HUGO_DESTINATION` selects an output directory and `HUGO_BASEURL` selects a different base URL.

Theme runtime files are vendored under `themes/hugo-blog-awesome/`; [upstream provenance](themes/hugo-blog-awesome/UPSTREAM.md) records the pin and license. Keep site overrides in the project's own `layouts/` and `assets/`. Update the pin deliberately and validate before changing it.

## Preview requests

Use a user-selected canonical version branch `release/vvivid.dev/X.Y.Z` for staging at `https://preview.vvivid.dev/`. Production uses an immutable annotated `vvivid.dev/vX.Y.Z` tag at a verified merged commit. Main checks only; the former `release/vvivid-preview` branch has no workflow.

Read the [publication contract](deploy/gitops/README.md) before preparing a release. Source publication, registry publication, CI-to-GitOps writes and host cutover each require the applicable authorization. Preserve an existing release branch and use normal merge/fast-forward updates; never reset or force-push it. Keep preview noindex and never submit preview URLs to IndexNow.

Reuse the monorepo shared artifact-publication environment and one existing Tailscale GitHub OIDC CI trust. At activation, query the repository's actual `sub_claim_prefix` and update that existing trust's exact environment subject once. Do not create per-project identities or use operator/developer credentials.

After activation, distinguish successful artifact publication, a GitOps commit, Fleet readiness and actual public HTTP acceptance. The homelab runbook owns staged DNS-01 validation and later individual host cutovers. Preparing chart/manifests never migrates a running website.

## Public output and operations

Keep this file and agent-only notes outside `content/`, `static/`, and `assets/`. Verify no case variant of `AGENTS.md` or `AGENT.md` appears in the production output. The nginx configuration also blocks these filenames at every URL depth.

Public examples and links must use public hostnames. Never expose internal VPN addresses, private IPs, `sslip.io`, or registry hostnames in published content. Preserve existing public `gitlab.vvivid.dev` references unless an authorized content change replaces them.

Legacy operational scripts and the project-root Compose configuration are imported references to the former infrastructure. The archived SSH deployment helper retains its fixed server scope in the archived live runbook. Building the site does not authorize running those operations or deploying it.
