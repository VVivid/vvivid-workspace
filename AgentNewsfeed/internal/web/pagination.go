package web

import (
	"net/http"
	"net/url"
	"strconv"

	"agentnewsfeed/internal/feed"
)

const pageSizeHeader = "X-Newsfeed-Page-Size"

func parseBrowserPageSize(value string) (int, error) {
	size, err := strconv.Atoi(value)
	if err != nil || size < 1 || size > feed.MaxBrowserPageSize || strconv.Itoa(size) != value {
		return 0, feed.ErrInvalid
	}
	return size, nil
}

func browserPageSize(r *http.Request) (int, error) {
	if r.Header.Get("Accept") == browserMediaType && r.Header.Get(pageSizeHeader) != "" {
		return parseBrowserPageSize(r.Header.Get(pageSizeHeader))
	}
	return feed.DefaultBrowserPageSize, nil
}

func (data *bootstrap) readPagePosition(query url.Values) error {
	if len(query["cursor"]) > 1 || len(query["before"]) > 1 || len(query["q"]) > 1 || len(query["page_size"]) > 1 ||
		(query.Has("cursor") && query.Has("before")) {
		return feed.ErrInvalid
	}
	data.Cursor, data.Before = query.Get("cursor"), query.Get("before")
	if (query.Has("cursor") && data.Cursor == "") || (query.Has("before") && data.Before == "") {
		return feed.ErrInvalid
	}
	var err error
	data.Query, err = feed.NormalizeArticleQuery(query.Get("q"))
	if err != nil {
		return err
	}
	data.PageSize = feed.DefaultBrowserPageSize
	if query.Has("page_size") {
		data.PageSize, err = parseBrowserPageSize(query.Get("page_size"))
	}
	return err
}

func (data bootstrap) pageBoundary() (cursor string, before bool) {
	if data.Before != "" {
		return data.Before, true
	}
	return data.Cursor, false
}
