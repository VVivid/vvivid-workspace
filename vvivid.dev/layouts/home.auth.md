# auth.md — Public access to {{ .Site.Title }}

This is a public, static portfolio and technical blog at {{ .Site.BaseURL }}.

## Reading

- Published pages, Markdown, RSS, and the sitemap allow anonymous GET and HEAD requests.
- No account, registration, API key, bearer token, session, or login is required to read this site.
- Request `Accept: text/markdown` on a page URL to prefer Markdown. Direct `index.md` links also work. HTML is the default when both formats have the same preference.
- [Content index]({{ "llms.txt" | absURL }}), [RSS]({{ "index.xml" | absURL }}), and [sitemap]({{ "sitemap.xml" | absURL }}) list public content.

## APIs and authentication discovery

This site exposes no public application API, MCP endpoint, or write operations. Articles and project links can describe APIs hosted elsewhere; those are not services provided by this website.

An API catalog, OpenAPI document, OAuth authorization server metadata, OpenID Connect discovery, and client registration are not applicable and are not advertised. There are no authorization or token endpoints on this site. This document explains public access; it is not an authentication protocol or an invitation to submit credentials.

The optional browser analytics script and third-party assets do not require visitor registration. Services on other hostnames have their own access policies.
