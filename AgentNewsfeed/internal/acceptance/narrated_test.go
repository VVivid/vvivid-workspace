package acceptance

import "agentnewsfeed/internal/feed"

// narrated gives a test article the plain narration every agent save needs.
func narrated(c feed.Content) feed.Content {
	c.Narration = feed.PlainNarration(c.Title, c.ContentMarkdown)
	return c
}
