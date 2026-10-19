// Package webassets embeds the production assets, independently of the reference demo.
package webassets

import "embed"

//go:embed templates/*.gohtml static/app.css static/app.js
var Files embed.FS
