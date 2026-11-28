package web_test

import (
	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/store/sqlite"
)

// narrated gives a test article the plain narration every agent save needs.
func narrated(c feed.Content) feed.Content {
	c.Narration = feed.PlainNarration(c.Title, c.ContentMarkdown)
	return c
}

// narrationFor is the plain narration of an article with title and markdown,
// for a patch that changes either.
func narrationFor(title, markdown string) *[]sqlite.NarrationPart {
	return new(feed.PlainNarration(title, markdown))
}
