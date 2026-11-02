package thumbnail

import (
	"net/url"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// MarkdownImages maps parsed image destinations to public HTTPS URLs resolved
// against the saved source. It never fetches a page, parses HTML or discovers
// images outside Markdown image nodes. The fetcher's policy and DNS checks still
// apply before any connection is made.
func MarkdownImages(sourceURL, markdown string) map[string]string {
	images := make(map[string]string)
	base, err := url.Parse(sourceURL)
	if err != nil || (Policy{}).Validate(sourceURL) != nil || markdown == "" {
		return images
	}
	source := []byte(markdown)
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		image, ok := node.(*ast.Image)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		// Match the URI escaping used by CommonMark renderers, including escaped
		// punctuation and character references. The browser matches this exact
		// destination to a local route rather than relying on parser node order.
		destination := string(util.URLEscape(image.Destination, true))
		reference, err := url.Parse(destination)
		if err == nil && destination != "" {
			resolved := base.ResolveReference(reference).String()
			if (Policy{}).Validate(resolved) == nil {
				images[destination] = resolved
			}
		}
		// Images nested in alt text do not produce browser image requests.
		return ast.WalkSkipChildren, nil
	})
	return images
}
