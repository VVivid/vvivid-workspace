Vite writes generated app.js and app.css into assets/ before Go compilation.
The Go binary embeds this directory; generated assets are excluded from Git.
