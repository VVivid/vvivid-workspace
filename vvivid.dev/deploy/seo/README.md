# Search indexing operations

These are reviewable operations for the existing public hosts. This repository
change does not apply Cloudflare rules, modify routers, purge caches, submit
URLs, or rebuild the legacy preview service. The GitHub live workflow updates
only `vvivid-live`; its next successful deployment cannot fix the already running
`vvivid-preview` image by itself.

## Keep the existing preview out of indexes

The source has two defenses for future preview images:

- Build with `HUGO_BASEURL=https://preview.vvivid.dev/`. All HTML gets
  `noindex, follow`, self-canonicals use that preview URL, the sitemap is empty,
  and robots does not advertise it. Do not build a preview with the production
  base URL.
- nginx sends `X-Robots-Tag: noindex, follow` for every host except `vvivid.dev`,
  including Markdown, APIs, assets, and errors. This covers a production-built
  image that is reused on preview.

To cover the **currently running** separate preview before a rebuild, add the
narrow Cloudflare Response Header Transform Rule in
[cloudflare-rules.example.json](cloudflare-rules.example.json). In the zone's
Rules overview, create a response-header rule with custom expression
`http.host eq "preview.vvivid.dev"`. Set the static response header
`X-Robots-Tag` to `noindex, follow`. Use **Set**, so an existing value is replaced.
Check the rule order for later overrides. It must not match the apex, `www`,
analytics, or other subdomains.

Keep robots crawlable with `Allow: /`: crawlers need to fetch the response to see
noindex. A robots `Disallow` or a site-wide bot block can prevent that. Keep the
existing `search=yes, ai-input=yes, ai-train=yes` Content Signals. Noindex is an
indexing directive, not access control; public preview remains publicly readable.
If a legacy preview has its own robots disallow, remove only that preview's
restriction during the separately authorized activation.

## Make www redirect reach the canonical site

Read-only inspection on 2026-10-25 found proxied DNS records for the apex, `www`,
and preview. The `www` response was 404 while the observed upstream router
matched only `Host(vvivid.dev)`. Adding DNS is not the identified remedy.

The nginx image now returns `301 https://vvivid.dev$request_uri` for
`Host: www.vvivid.dev`, preserving the path and query. That response is reachable
only if the upstream router forwards `www` to this service and TLS accepts it.
During an authorized origin activation, expand only the existing live router's
host rule, keeping its other labels and networks intact. For example, with the
legacy label name shown in the repository:

```yaml
traefik.http.routers.vvivid.rule: "Host(`vvivid.dev`) || Host(`www.vvivid.dev`)"
```

Use the actual live router name rather than creating a second competing router.
Verify certificate coverage for both hostnames at the browser-facing edge and,
with Full (strict) TLS, between Cloudflare and the origin. Preserve the separate
preview router and its image expression.

Alternatively, the narrow Single Redirect in the JSON example handles `www` at
Cloudflare without waiting for an origin router change. In Rules overview,
create a Single Redirect with custom expression
`http.host eq "www.vvivid.dev"`, dynamic target
`concat("https://vvivid.dev", http.request.uri.path)`, status **301**, and
**Preserve query string enabled**. This needs proxied DNS and edge certificate
coverage for `www`; an edge redirect does not need origin TLS for that request.
Choose one activation path and verify its behavior. The nginx fallback remains
useful when requests reach the origin.

The JSON file contains individual example rules grouped by their Ruleset Engine
phase, not a command or a complete zone ruleset replacement. Merge a reviewed
rule into the existing phase/rule order; never replace the zone's existing rules
with this file. Response-header transforms and Single Redirects are available on
the Free plan, subject to the zone's available quota. Neither example requires
regular expressions, a Worker, a paid plan, or new credentials in this repository.
See Cloudflare's [response-header transforms](https://developers.cloudflare.com/rules/transform/response-header-modification/),
[Transform Rules availability](https://developers.cloudflare.com/rules/transform/#availability),
and [Single Redirect settings](https://developers.cloudflare.com/rules/url-forwarding/single-redirects/settings/).

## Refresh stale robots responses narrowly

The audit observed an old cached `robots.txt`. After the intended origin or rule
change is active, use Cloudflare's custom purge for the exact changed URLs,
starting with `https://vvivid.dev/robots.txt`. Purge the preview robots URL only
if it was changed. Do not use Purge Everything. Check both the response body and
`CF-Cache-Status`/`Age` afterwards. If another cache rule forces an Edge TTL,
correct that narrow override before expecting the origin's `no-store` to work.
A response-header transform runs after cache evaluation and does not repair the
underlying cache policy. The live runbook's HTML/Markdown cache isolation still
applies.

## Verify after an authorized activation

```sh
curl -sSI 'https://www.vvivid.dev/posts/http_in_golang/?source=www&x=1'
curl -sSI https://preview.vvivid.dev/
curl -sSI https://preview.vvivid.dev/about/index.md
curl -sSI https://vvivid.dev/
curl -sS https://vvivid.dev/robots.txt
curl -sS https://vvivid.dev/sitemap.xml
```

Expect one `www` redirect to the apex with the same path/query, preview's noindex
header, no site-wide noindex header at the apex, and the selected production
sitemap URLs. Inspect both HTML and Markdown in both request orders using the
[serving checks](../../README.md#representation-caching-and-deployment-checks).
Verify the actual public edge separately from local image tests. Then use the
search consoles to confirm the crawler sees the changed response. Removal of an
already indexed preview can take time; a temporary console removal alone does
not replace a persistent noindex policy.
