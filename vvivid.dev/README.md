# vvivid.dev

Ou David's personal website, built with Hugo Extended. This project owns its content, theme, build context, and release version. Commands below run from the repository root unless stated otherwise.

## Native build and preview

Install Make, a POSIX shell, Python 3, Bash for deployment workflow tests, and **Hugo Extended 0.162.1**, the version recorded in [.hugo-version](.hugo-version). No Node or Go installation is needed. Download the matching Extended archive for your platform from the [official release](https://github.com/gohugoio/hugo/releases/tag/v0.162.1) and verify its published checksum.

For Linux amd64, a temporary installation can be used without changing system tools:

```sh
site_hugo_dir=$(mktemp -d)
curl -fL --output "$site_hugo_dir/hugo.tar.gz" \
  https://github.com/gohugoio/hugo/releases/download/v0.162.1/hugo_extended_0.162.1_linux-amd64.tar.gz
printf '%s  %s\n' \
  e34160095b6a6406af857fe212f50e4451f67ed1276b9bb0de13d08754980118 \
  "$site_hugo_dir/hugo.tar.gz" | sha256sum --check
tar -xzf "$site_hugo_dir/hugo.tar.gz" -C "$site_hugo_dir"
HUGO="$site_hugo_dir/hugo" make -C vvivid.dev check
```

Use `make website-check`, `make website-test`, or `make website-build` at the repository root, or `make check`, `make test`, or `make build` inside this directory. All three project targets render the production site through [scripts/build.sh](scripts/build.sh). Check and test also run the [deployment tests](deploy/live/README.md#local-checks) with fake Docker/HTTP/GitHub commands; no remote service is contacted. Build remains a render-only target. The build script requires the pinned Extended version, minifies output, and fails if any case variant of `AGENTS.md` or `AGENT.md` reaches the output. Generated `public/` and `resources/` directories are ignored by Git.

Set `HUGO=/absolute/path/to/hugo` when the binary is outside `PATH`. `HUGO_DESTINATION` changes the output directory; relative paths are resolved from `vvivid.dev/`. `HUGO_BASEURL` overrides the production default of `https://vvivid.dev/`.

For a development server using the installation above:

```sh
cd vvivid.dev
"$site_hugo_dir/hugo" server --bind 127.0.0.1 --baseURL http://localhost:1313/
```

Open [localhost:1313](http://localhost:1313/). The development server is for preview; use a production Make target to validate output before delivery.

## Docker build and local serving

Build using this project as the complete Docker context:

```sh
docker build --build-arg HUGO_BASEURL=http://localhost:8080/ \
  -t vvivid-website:local ./vvivid.dev
docker run --rm --publish 127.0.0.1:8080:80 vvivid-website:local
```

Open [localhost:8080](http://localhost:8080/). The [Dockerfile](Dockerfile) builds with a Hugo image pinned by version and digest, runs the same production build script, and serves the generated site with nginx. Omit `--build-arg HUGO_BASEURL=...` to build with the production URL. This local command uses Docker's default network and requires no external Traefik network. The nginx configuration blocks agent-guide filenames at every URL depth.

The [live GitHub workflow](../.github/workflows/website.yml) builds and smoke-checks the complete Docker image on site or workflow changes. Pull requests have read-only repository permissions. Matching pushes to `main` publish an immutable image to GHCR and deploy only live through restricted SSH over Tailscale after the [one-time setup](deploy/live/README.md). The deployment job uses the `vvivid-live` GitHub environment, serializes updates, verifies the exact running image and local/public HTTP health, and restores the previous live image on failure. The separate
[preview workflow](../.github/workflows/website-preview.yml) checks the preview
image on pull requests and deploys only exact `release/vvivid-preview` pushes to
`preview.vvivid.dev`, using its own URL, `preview-sha-<commit>` image tags,
`vvivid-preview` environment, and restricted identity. Both helpers serialize
shared `.env` writes; see the [preview runbook](deploy/preview/README.md) for
installation and migration of the existing service.

The official `nginx:alpine` image includes the njs module used by
[nginx/representations.js](nginx/representations.js) to parse HTTP `Accept`
preferences. The Dockerfile loads that bundled module; a custom nginx installation
must also load `ngx_http_js_module` and install this script before using
[nginx.conf](nginx.conf). No application server or external conversion service is
required. `nginx -t` is part of the image smoke check and catches a missing module.

After building the image, run the real HTTP checks locally:

```sh
./vvivid.dev/scripts/smoke-image.sh vvivid-website:local
```

This needs Docker, curl, and Python 3. It starts a temporary container on an
automatically assigned loopback port, checks the nginx configuration, runs
[scripts/test-serving.py](scripts/test-serving.py), and removes the container.
Both website workflows call this same entry point. An optional second argument
asserts the build base URL; each workflow passes its own public URL. It checks
HTML/Markdown,
Accept quality values and exclusions, HEAD, conditional requests, redirects,
errors, API/catalog/skill contracts and digests, discovery resources, structured
data, Content Signals, static asset caching, and blocked agent-guide URLs. It does
not contact the live website. Check and test also build temporary content fixtures
to verify automatic publication and exclusion of drafts and future pages.

## Source and dependencies

- [content/](content/) contains the pages and posts; preserve existing front matter and URLs when editing.
- [layouts/](layouts/), [assets/](assets/), and [static/](static/) contain site overrides and public assets.
- [hugo.toml](hugo.toml) configures the site.
- [themes/hugo-blog-awesome/](themes/hugo-blog-awesome/) vendors the theme's runtime files and MIT license at `v2.0.1`, commit `46d1ab1a9cc8ad24a3d482af618771d708d91c2f`. [UPSTREAM.md](themes/hugo-blog-awesome/UPSTREAM.md) records provenance, checksum, and update instructions. No submodule checkout is needed.
- [AGENTS.md](AGENTS.md) contains contributor guidance and stays outside public assets and generated output.

The site was imported from an attached source snapshot without original Git history. The archive's theme directory was empty and its theme revision could not be recovered. The pinned published theme makes the build reproducible, but exact visual parity with the original site cannot be verified. Content and asset files were preserved; template changes adapt the archive to the pinned Hugo version.

Update `.hugo-version` and the Docker builder image version/digest together when deliberately upgrading Hugo, then validate the site and overrides. This project's independent release tags use `vvivid.dev/vX.Y.Z`; see the [workspace development guide](../DEVELOPMENT.md#independent-versions).

## Public Markdown and discovery

Hugo generates `index.md` alongside the HTML home, about, project, post, section,
and taxonomy pages. Single-page Markdown uses the authored body without front
matter, retaining code fences and links; home and listing templates add useful
navigation, descriptions, and published entries. Both formats share the same
published-content selection. Drafts and future content stay excluded by the
production build. These templates currently require no shortcode conversion.

On an existing page such as `/about/`, nginx serves Markdown for
`Accept: text/markdown`. It compares valid quality values, honors `q=0` and
more-specific media ranges, and serves HTML when both representations are equally
preferred. Missing or wildcard-only preferences therefore keep ordinary browser
behavior. If neither available format is acceptable, it returns `406`; missing
pages remain `404`. Direct `/about/index.md` links always return Markdown and
`/about/index.html` also supports negotiation. A URL missing its trailing slash
retains the normal redirect. Hugo's development server exposes the generated
files but does not perform nginx's HTTP negotiation.

`/llms.txt` is generated from published pages on each build, with descriptions and
HTML/Markdown links. `/auth.md` explains anonymous public reading and the absence
of registration or credentials. HTML `<link>` elements and HTTP `Link` headers
point to the actual alternate representation, content index, access document,
RSS, and sitemap. The homepage also advertises the API catalog, API description,
human API documentation, and public skill index. Its JSON-LD describes the
existing website and author using configured public profile links.

## Read-only content API and public skill

`GET /api/content.json` returns a complete build-time JSON snapshot of published
regular pages: titles, descriptions, canonical and Markdown URLs, sections,
authors, tags, categories, dates when known, reading estimates, and optional
project links. Filter locally; there is no query search, pagination, or dynamic
application server. Drafts and future content follow Hugo's production selection
and stay excluded. [API documentation](content/api/_index.md) is published at
`/api/`; `/api/openapi.json` provides the OpenAPI 3.1 contract. Anonymous HEAD is
also supported. Unknown paths and unsupported methods use nginx's HTML 404/405
responses. The API and discovery resources retain `no-store` cache isolation.

`/.well-known/api-catalog` serves an [RFC 9727](https://www.rfc-editor.org/rfc/rfc9727.html)
linkset as `application/linkset+json` with the API's actual endpoint as its
`anchor`, plus `service-desc` and `service-doc` links to the contract and docs.
Hugo's extensionless output uses the matching `layouts/home.apicatalog` template.

`/.well-known/agent-skills/index.json` implements the
[Agent Skills Discovery draft v0.2.0](https://github.com/cloudflare/agent-skills-discovery-rfc).
This draft is a discovery convention, not an established IETF well-known URI
registration. The single `skill-md` artifact teaches finding, reading, and citing
the site's articles. Metadata lives in [data/agent-skills.yaml](data/agent-skills.yaml);
[the public skill template](assets/agent-skills/read-vvivid-articles.md) is rendered
and published as `/.well-known/agent-skills/read-vvivid-articles/SKILL.md`. The index
computes `sha256:` from that resource's exact raw bytes during the same build,
rather than maintaining a separate checksum. Internal `AGENT.md` and `AGENTS.md`
remain excluded and blocked.

Generated `robots.txt` declares `Content-Signal: search=yes, ai-input=yes,
ai-train=yes`: the author's chosen policy allows search, AI answers, and training.
These [Content Signals](https://contentsignals.org/) express usage preferences;
they are not authentication or technical access controls.

## Search and answer-engine maintenance

[SEO.md](SEO.md) covers the curated archive policy, author and article metadata,
conditional math/diagram assets, the measured source baseline, and the account-free
IndexNow helper. The helper defaults to a local JSON dry run and never submits
URLs during a build or deployment. Native check/test also exercise its safeguards.
Build previews with their own `HUGO_BASEURL`; they receive noindex metadata and an
empty sitemap. nginx adds a noindex header on every nonproduction host.

The [search operations runbook](deploy/seo/README.md) provides narrow, reviewable
Cloudflare rules for the already running preview and `www`, plus router/TLS and
robots-cache checks. These require separate activation. The live GitHub workflow
changes only live; preview images are rebuilt by the separate exact-branch
workflow after its own activation.

The site hosts no MCP server, account system, write endpoint, OAuth authorization
server, token endpoint, or client registration. Articles and project links can
discuss separately hosted APIs; those services have their own access policies.

## WebMCP evaluation

The [current WebMCP draft](https://webmachinelearning.github.io/webmcp/) exposes
`document.modelContext.registerTool` with an input schema, an asynchronous execute
callback, and annotations such as `readOnlyHint`. It is experimental. A potential
`search_articles` or `list_projects` browser tool could filter the generated
content API, but native compatibility with this draft has not been verified in
the available environment. No browser tool or Cloudflare WebMCP bridge is enabled.
The supported interface remains the anonymous HTTP API. Re-evaluate against a
native browser implementation before enabling browser tools, including feature
detection, input validation, cancellation, and read-only execution tests. This
evaluation does not change live Cloudflare settings or require a paid plan.

## Representation caching and deployment checks

Page and discovery responses send `Cache-Control: no-store` and `Vary: Accept`,
including their error responses. This deliberately avoids sharing HTML and
Markdown at the same URL in a browser or CDN cache. Page/Markdown locations
disable nginx ETags and `If-Modified-Since` handling: outputs can have identical
build timestamps, so an HTML timestamp must not produce a Markdown `304` (or the
reverse). `Last-Modified` may still be present as informational metadata;
conditional date requests receive a full representation. Static image, CSS,
and JavaScript assets retain their separate seven-day caching policy.

Cloudflare does not include arbitrary `Vary` fields in its default cache key.
The origin's `no-store` policy is the safe default for this feature without
changing the Cloudflare plan or adding an account. Before an authorized
deployment, confirm that no Cache Rule, Page Rule, Worker, or forced Edge TTL
overrides origin cache control on pages, `.md`, `/llms.txt`, `/api/`,
`/.well-known/`, or error responses.
Purge previously cached page responses when activating this configuration.
`Vary: Accept` alone cannot repair an already cached response. A future explicit
Free-plan Vary Cache Rule is a separate configuration choice; do not remove
`no-store` merely because the header is present. See Cloudflare's
[cache-control behavior](https://developers.cloudflare.com/cache/concepts/cache-control/)
and [Vary support](https://developers.cloudflare.com/cache/concepts/vary/).

After authorized deployment, repeat HTML/Markdown requests in both orders at the
public hostname and inspect `Content-Type`, `Vary`, `Cache-Control`, `CF-Cache-Status`,
and conditional responses. Local image checks establish origin behavior only;
they do not establish the live CDN configuration or activate these resources.

## Deployment

The [live deployment runbook](deploy/live/README.md) owns GitHub/Tailscale setup, restricted server installation, the live-only Compose change, cutover, and rollback. Deployment activation is a separate operational step; local builds do not change external services. The [preview deployment runbook](deploy/preview/README.md) owns separate
`release/vvivid-preview` activation and shared-lock migration; `main` remains
live. Preview branch creation/publication and service cutover are separate
operational actions.

The archived GitLab pipeline is retained under [deploy/legacy/](deploy/legacy/README.md), outside the active CI filename. Its registry, external `/infra/compose.yml`, preview/live services, Traefik configuration, and credentials were not migrated.

The root [docker-compose.yml](docker-compose.yml), [cleanup script](scripts/cleanup-vvivid-images.sh), and [live-tag script](scripts/persist-vvivid-live-tag.sh) remain operational references to that infrastructure. Invoking them can change external services, images, or environment files. The Make targets and GitHub workflow do not run these legacy operations. Source import and local builds do not establish a live deployment.
