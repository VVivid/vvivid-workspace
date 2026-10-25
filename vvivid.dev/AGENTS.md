# vvivid.dev agent guidance

Follow the [root instructions](../AGENTS.md) and shared workflow. This project owns the Hugo site, content, layout overrides, static assets, and its vendored theme. The original archive's GitLab deployment is retained only as [legacy reference](deploy/legacy/gitlab-ci.yml); it is not an active repository workflow. GitHub live deployment setup and recovery are documented in the [live runbook](deploy/live/README.md); the separate [preview runbook](deploy/preview/README.md) covers only
`release/vvivid-preview` and its shared-lock migration. `main` remains live.

## Content and author

The portfolio represents Ou David, a systems engineer focused on reliable distributed software, backend engineering, Go, infrastructure, integrations, observability, data safety, and practical product engineering. Preserve the author's direct, experience-driven technical voice and point of view. Use the existing writing as the source for tone, background, and interests. Do not add unsupported credentials, claims, metrics, or experience.

When editing content, preserve URLs and established front matter unless the requested change requires otherwise. Improve clarity with concrete tradeoffs, failure modes, and examples. Content reviews should prioritize accuracy, clarity, credibility, and reader usefulness while retaining the author's personality.

## Build and dependency boundaries

Use Hugo extended at the version in `.hugo-version`. Run `./scripts/build.sh` for the production build and agent-guide exclusion check. Override the binary with `HUGO=/absolute/path/to/hugo`; `HUGO_DESTINATION` selects an output directory and `HUGO_BASEURL` selects a different base URL.

Theme runtime files are vendored under `themes/hugo-blog-awesome/`; [upstream provenance](themes/hugo-blog-awesome/UPSTREAM.md) records the pin and license. Keep site overrides in the project's own `layouts/` and `assets/`. Update the pin deliberately and validate before changing it.

## Preview requests

When the user asks for a website preview, use the exact deployment branch `release/vvivid-preview` and URL `https://preview.vvivid.dev/`. Other `release/vvivid-*` branches and a branch named `preview` do not deploy. `main` continues to deploy live at `https://vvivid.dev/`; a preview request does not promote changes to live.

Read the [preview runbook](deploy/preview/README.md) before publishing a preview. Before the first release-branch push, verify that the workflow is included in the selected source and the one-time GitHub environment, Tailscale, restricted server helper, shared-lock migration, routing, and legacy GitLab cutover are ready. Creating a branch alone does not install these prerequisites.

Prepare and validate requested changes in the task's dedicated worktree through the shared development workflow. Identify the source commit the user wants to preview. Inspect any existing `release/vvivid-preview` branch before updating it; preserve its work and use a normal merge or fast-forward rather than resetting or force-pushing. Remote creation or updates require explicit push permission covering `origin` and `release/vvivid-preview`, unless that authorization was already supplied. Publishing a task branch or merging a PR into `main` does not update the preview branch automatically.

Each push to `release/vvivid-preview` runs the [preview workflow](../.github/workflows/website-preview.yml), builds with `HUGO_BASEURL=https://preview.vvivid.dev/`, and deploys a checked immutable digest to only `vvivid-preview`. Preview remains publicly readable with `noindex`; do not submit preview URLs to IndexNow. After an authorized push, inspect the workflow result and verify the exact running image, local/public preview health, indexing exclusion, and unchanged live service as described in the runbook. Report the preview URL and actual deployment result; a successful branch push alone is not deployment success.

## Public output and operations

Keep this file and agent-only notes outside `content/`, `static/`, and `assets/`. Verify no case variant of `AGENTS.md` or `AGENT.md` appears in the production output. The nginx configuration also blocks these filenames at every URL depth.

Public examples and links must use public hostnames. Never expose internal VPN addresses, private IPs, `sslip.io`, or registry hostnames in published content. Preserve existing public `gitlab.vvivid.dev` references unless an authorized content change replaces them.

Legacy operational scripts and the project-root Compose configuration are imported references to the former infrastructure. The live deployment helper has its own fixed server scope in the live runbook. Building the site does not authorize running those operations or deploying it.
