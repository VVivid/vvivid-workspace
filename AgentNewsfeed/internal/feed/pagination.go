package feed

import (
	"encoding/base64"
	"encoding/json"
	"slices"
)

type pageMode uint8

const (
	forwardOnly pageMode = iota
	browseOlder
	browseNewer
)

func browseMode(before bool) pageMode {
	if before {
		return browseNewer
	}
	return browseOlder
}

type listPage[T any] struct {
	rows     []T
	previous string
	next     string
}

// loadPage probes one extra row in the requested direction. Browser lists
// with a boundary also check the opposite direction before offering a link.
func loadPage[Row, Cursor any](limit int, mode pageMode, hasBoundary bool, boundary Cursor,
	readRows func(boundary Cursor, towardNewer bool, limit int) ([]Row, error), cursorFor func(Row) Cursor) (listPage[Row], error) {
	towardNewer := mode == browseNewer
	rows, err := readRows(boundary, towardNewer, limit+1)
	if err != nil {
		return listPage[Row]{}, err
	}
	hasMore := len(rows) > limit
	page := listPage[Row]{rows: rows[:min(len(rows), limit)]}
	if len(page.rows) == 0 {
		return page, nil
	}
	if towardNewer {
		// Reading toward newer rows starts with the closest rows. Display newest first.
		slices.Reverse(page.rows)
		if hasMore {
			page.previous = encodeCursor(cursorFor(page.rows[0]))
		}
		last := cursorFor(page.rows[len(page.rows)-1])
		older, err := readRows(last, false, 1)
		if err != nil {
			return listPage[Row]{}, err
		}
		if len(older) != 0 {
			page.next = encodeCursor(last)
		}
		return page, nil
	}
	if hasMore {
		page.next = encodeCursor(cursorFor(page.rows[len(page.rows)-1]))
	}
	if mode == browseOlder && hasBoundary {
		first := cursorFor(page.rows[0])
		newer, err := readRows(first, true, 1)
		if err != nil {
			return listPage[Row]{}, err
		}
		if len(newer) != 0 {
			page.previous = encodeCursor(first)
		}
	}
	return page, nil
}

func encodeCursor(cursor any) string {
	// The cursor structs contain only primitive JSON fields.
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}
