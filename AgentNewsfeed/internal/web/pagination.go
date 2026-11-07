package web

import (
	"net/http"
	"net/url"

	"agentnewsfeed/internal/feed"
)

const pageSizeHeader = "X-Newsfeed-Page-Size"

func browserPageSize(r *http.Request) int {
	if r.Header.Get("Accept") == browserMediaType && r.Header.Get(pageSizeHeader) == "5" {
		return 5
	}
	// Native navigation has no viewport information. Start with the smaller page.
	return 3
}

func (data *bootstrap) readPagePosition(query url.Values) error {
	if len(query["cursor"]) > 1 || len(query["before"]) > 1 || (query.Has("cursor") && query.Has("before")) {
		return feed.ErrInvalid
	}
	data.Cursor, data.Before = query.Get("cursor"), query.Get("before")
	return nil
}

func (data bootstrap) pageBoundary() (cursor string, before bool) {
	if data.Before != "" {
		return data.Before, true
	}
	return data.Cursor, false
}
