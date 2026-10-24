# {{ .Title }}

{{ with .Description }}{{ . }}

{{ end }}Canonical HTML: {{ .Permalink }}

{{ with .RawContent }}{{ . }}
{{ end }}
{{ partial "markdown-list.html" .Pages.ByDate.Reverse }}
