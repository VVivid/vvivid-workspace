// Package webassets embeds the production assets, independently of the reference demo.
package webassets

import "embed"

//go:embed templates/*.gohtml static/app.css static/app.js static/share.js
var Files embed.FS
