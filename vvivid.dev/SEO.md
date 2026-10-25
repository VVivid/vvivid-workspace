# Search and answer-engine maintenance

The site provides useful authored HTML, plain Markdown, a crawlable content API,
and an explicit author profile. These improve access and interpretation; they
do not guarantee indexing, rankings, AI citations, or passing Core Web Vitals.

## Index policy and content

Only a build with the exact base URL `https://vvivid.dev/` is eligible for indexing.
The [eligibility partial](layouts/_partials/seo/indexable.html) drives HTML
noindex metadata and the sitemap. Tag/category indexes and uncurated term archives
remain browsable but are noindex and excluded. A term must explicitly opt in with
`index: true`, contain an introduction, and link at least two published regular
pages. The curated Go, benchmarking, JSON-RPC, and technical-deep-dive hubs meet
that policy. The new integration guide connects existing posts without inventing
new experience. Review a hub's usefulness when adding posts; do not opt in a
one-link archive solely to increase the sitemap count.

Preserve original `date`/`publishDate`. Set `lastmod` only after a substantive
article change, never to the build time or a scheduled freshness date. The eight
posts edited for this change have explicit `lastmod: 2026-10-25` because their
openings and structure changed. Hugo renders full ISO 8601 timestamps; a
source date without a time is represented as midnight, not an asserted observed
time of publication. Article schema, visible bylines, and the content API use
the source publication/modification dates. The publisher is Ou David as a Person;
the shared identity points to the visible About ProfilePage.

Keep direct answers near the beginning, follow the page H1 with H2 sections and
H3 subsections, retain code and source links, and link related work where it helps
a reader continue. Do not add synthetic FAQs, unsupported claims, or markup for
content that is not visible. [Preview, www, and cache activation](deploy/seo/README.md)
requires separate operational work; merging the source does not change the
existing preview service.

## IndexNow without an account

[data/indexnow.json](data/indexnow.json) contains the public verification key.
The matching `static/<key>.txt` is published at the root; this key establishes
site ownership for IndexNow and is intentionally public, not an API credential.
The helper checks that the built file contains the same key and validates every
selected URL against the production sitemap and page's canonical/noindex metadata.
Preview builds, alias hosts, thin archives, and arbitrary URLs are rejected.

Build first, then prepare a batch locally:

```sh
HUGO=/absolute/path/to/hugo make -C vvivid.dev build
python3 vvivid.dev/scripts/indexnow.py --public-dir vvivid.dev/public
```

The default prints JSON and makes no network request. To select only changed
pages, pass `--urls-file /path/to/changed-urls.txt`, one canonical URL per line.
The sitemap is the eligibility set, not a change detector. Existing-page updates
can be submitted this way; this helper does not submit removed URLs absent from
the sitemap. A batch is limited to 10,000 URLs.

After the exact production build has been successfully deployed and publicly
verified, an explicitly requested submission can run:

```sh
python3 vvivid.dev/scripts/indexnow.py --public-dir vvivid.dev/public \
  --urls-file /path/to/changed-urls.txt --submit
```

`--submit` first reads the live HTTPS verification file and checks its body and
URL, then sends the reviewed batch to `https://api.indexnow.org/indexnow`. It
accepts HTTP 200/202 as receipt, not proof of indexing. No submission or new
account is part of a build, test, pull request, or deployment workflow. IndexNow
notifies participating engines; Google discovery still uses its normal crawl,
sitemap, and Search Console mechanisms. See the [IndexNow protocol](https://www.indexnow.org/documentation).

## Use the existing search and analytics accounts

In the owner's existing Google Search Console property, verify the production
host/domain using its existing verification method, submit
`https://vvivid.dev/sitemap.xml`, and inspect the homepage, a curated hub, and a
representative article. Check the fetched canonical and indexing status rather
than treating a successful HTTP response as proof of indexing. In the existing
Bing Webmaster Tools property, verify the same production host (or use its Google
import if appropriate), submit the same sitemap, and inspect representative URLs.
Recheck preview noindex and `www` redirect after their separate activation.
No additional account or subscription is needed for this implementation.

Record the deployment date and compare consistent periods, for example 28 days
before and after once enough post-change data exists. Use Search Console and
Bing for impressions, clicks, query/page trends, canonical selection, and excluded
pages; distinguish crawler discovery from actual indexing. In the existing Umami
setup, review organic/referral traffic and the existing project-link, article,
and PDF-export events. Inspect identifiable AI referral sources when present.
Referrals miss zero-click answers and many citations, so they do not measure all
AI visibility. Maintain a small set of relevant prompts and dated manual citation
checks if useful; keep observations separate from ranking or traffic claims.
Use the browser's real-user performance data or a repeatable Lighthouse run for
Core Web Vitals; local byte reductions alone cannot establish LCP, INP, or CLS.

## Measured implementation baseline

Read-only audit on 2026-10-25 found 40 production sitemap URLs returning 200,
eight article schemas, and 26 tag/category pages, many with a single link and a
generic description. The separate public preview was self-canonical and lacked
noindex; `www` returned 404. Those are observations before operational activation,
not claims that this source change has already repaired the public hosts.

The production build now has 19 sitemap URLs: the core pages, eight posts, one
project, four curated archive hubs, and the integration guide. All are
self-canonical and indexable in local generated/HTTP checks. The remaining
archives stay accessible with noindex. Preview builds have an empty sitemap.
The original shared PNG is 1,593,029 bytes; native Hugo encoding produces a
1,200 × 634 JPEG of 77,781 bytes (95.1% smaller), with the same artwork and readable
text on visual inspection. The original `/images/site-cover.png` remains served.
Only the fractional-cents article loads KaTeX; only the JSON-RPC implementation
loads Mermaid. The PDF export button waits for a diagram render before printing.
There is no new measured search traffic, ranking, AI citation, or field
performance result yet.

Run the native project checks, the IndexNow tests, and the local production and
preview Docker smoke checks described in the README before changing these rules.
The fixture tests exercise drafts/future posts, explicit publish/modified dates,
curated and thin archives, author identity, canonicals, and conditional assets.
