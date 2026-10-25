# auth.md — Public access to {{ .Site.Title }}

This is a public, static portfolio and technical blog at {{ .Site.BaseURL }}.

## Reading

- Published pages, Markdown, RSS, the sitemap, the content API, and public discovery resources allow anonymous GET and HEAD requests.
- No account, registration, API key, bearer token, session, or login is required to read this site.
- Request `Accept: text/markdown` on a page URL to prefer Markdown. Direct `index.md` links also work. HTML is the default when both formats have the same preference.
- [Content index]({{ "llms.txt" | absURL }}), [RSS]({{ "index.xml" | absURL }}), and [sitemap]({{ "sitemap.xml" | absURL }}) list public content.
- [Content API]({{ "api/content.json" | absURL }}): Published page metadata in JSON. [Documentation]({{ "api/" | absURL }}) and [OpenAPI description]({{ "api/openapi.json" | absURL }}).
- [API catalog]({{ ".well-known/api-catalog" | absURL }}) and [public skill index]({{ ".well-known/agent-skills/index.json" | absURL }}) describe the read-only API and article-reading instructions.

## APIs and authentication discovery

This site exposes a build-time, read-only content JSON API. It does not accept writes or host an MCP server. Articles and project links can describe APIs hosted elsewhere; those are separate services.

OAuth authorization server metadata, OpenID Connect discovery, and client registration are not applicable and are not advertised. There are no authorization or token endpoints on this site. This document explains public access; it is not an authentication protocol or an invitation to submit credentials.

The optional browser analytics script and third-party assets do not require visitor registration. Services on other hostnames have their own access policies.
