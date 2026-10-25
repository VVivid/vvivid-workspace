{{ .RawContent | strings.TrimSpace }}

## Explore

{{ range .Site.Menus.main }}- [{{ .Name }}]({{ .URL | absURL }})
{{ end }}
## Recent posts

{{ $posts := where .Site.RegularPages "Type" "in" .Site.Params.mainSections }}{{ partial "markdown-list.html" (first 5 $posts.ByDate.Reverse) }}
[All posts]({{ "posts/" | absURL }}) · [Markdown listing]({{ "posts/index.md" | absURL }})

## Reading this site

The pages are public and can be read without an account. [Content index]({{ "llms.txt" | absURL }}), [access information]({{ "auth.md" | absURL }}), [RSS]({{ "index.xml" | absURL }}), and [sitemap]({{ "sitemap.xml" | absURL }}).

Canonical HTML: {{ .Permalink }}
