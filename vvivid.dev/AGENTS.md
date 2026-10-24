# vvivid.dev agent guidance

Follow the [root instructions](../AGENTS.md) and shared workflow. This project owns the Hugo site, content, layout overrides, static assets, and its vendored theme. The original archive's GitLab deployment is retained only as [legacy reference](deploy/legacy/gitlab-ci.yml); it is not an active workflow.

## Content and author

The portfolio represents Ou David, a systems engineer focused on reliable distributed software, backend engineering, Go, infrastructure, integrations, observability, data safety, and practical product engineering. Preserve the author's direct, experience-driven technical voice and point of view. Use the existing writing as the source for tone, background, and interests. Do not add unsupported credentials, claims, metrics, or experience.

When editing content, preserve URLs and established front matter unless the requested change requires otherwise. Improve clarity with concrete tradeoffs, failure modes, and examples. Content reviews should prioritize accuracy, clarity, credibility, and reader usefulness while retaining the author's personality.

## Build and dependency boundaries

Use Hugo extended at the version in `.hugo-version`. Run `./scripts/build.sh` for the production build and agent-guide exclusion check. Override the binary with `HUGO=/absolute/path/to/hugo`; `HUGO_DESTINATION` selects an output directory and `HUGO_BASEURL` selects a different base URL.

Theme runtime files are vendored under `themes/hugo-blog-awesome/`; [upstream provenance](themes/hugo-blog-awesome/UPSTREAM.md) records the pin and license. Keep site overrides in the project's own `layouts/` and `assets/`. Update the pin deliberately and validate before changing it.

## Public output and operations

Keep this file and agent-only notes outside `content/`, `static/`, and `assets/`. Verify no case variant of `AGENTS.md` or `AGENT.md` appears in the production output. The nginx configuration also blocks these filenames at every URL depth.

Public examples and links must use public hostnames. Never expose internal VPN addresses, private IPs, `sslip.io`, or registry hostnames in published content. Preserve existing public `gitlab.vvivid.dev` references unless an authorized content change replaces them.

Operational scripts and Compose configuration are imported references to the former infrastructure. Building the site does not authorize running those operations or deploying it.
