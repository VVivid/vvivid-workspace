package sqlite

// pageRange uses the view's timestamp and ID together so tied timestamps
// remain in the same order in either direction. A zero timestamp starts a list.
func pageRange(timestampColumn, idColumn string, timestamp int64, id string, before bool, limit int) (string, []any) {
	comparison, direction := "<", " DESC"
	if before {
		comparison, direction = ">", " ASC"
	}
	order := " ORDER BY " + timestampColumn + direction + ", " + idColumn + direction + " LIMIT ?"
	if timestamp == 0 {
		return order, []any{limit}
	}
	return " AND (" + timestampColumn + ", " + idColumn + ") " + comparison + " (?, ?)" + order, []any{timestamp, id, limit}
}
