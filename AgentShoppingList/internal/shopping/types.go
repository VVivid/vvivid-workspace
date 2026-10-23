package shopping

import "agentshoppinglist/internal/store/sqlite"

var categories = []string{"produce", "meat", "drinks", "pantry", "dairy", "other"}

type WriteMeta struct {
	RequestID      string `json:"request_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type SaveProductInput struct {
	WriteMeta
	Product          sqlite.Product `json:"product"`
	ExpectedRevision int64          `json:"expected_revision"`
}

type ArchiveProductInput struct {
	WriteMeta
	ProductID        string `json:"product_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Archived         bool   `json:"archived"`
}

type SaveListInput struct {
	WriteMeta
	ListID                    string  `json:"list_id,omitempty"`
	Title                     string  `json:"title"`
	Retailer                  *string `json:"retailer"`
	Archived                  bool    `json:"archived"`
	ExpectedRevision          int64   `json:"expected_revision"`
	MakeActive                bool    `json:"make_active"`
	ExpectedSelectionRevision int64   `json:"expected_selection_revision"`
}

type UpdateItemsInput struct {
	WriteMeta
	ListID           string                 `json:"list_id"`
	ExpectedRevision int64                  `json:"expected_revision"`
	Operations       []sqlite.ItemOperation `json:"operations"`
}

type PreferencesInput struct {
	WriteMeta
	Preferences      sqlite.Preferences `json:"preferences"`
	ExpectedRevision int64              `json:"expected_revision"`
}

type SearchInput struct {
	Query           string `json:"query,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit,omitzero"`
	IncludeArchived bool   `json:"include_archived"`
}

type ProductPage struct {
	Products   []sqlite.ProductSummary `json:"products"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

type ListInput struct {
	ListID         string `json:"list_id,omitempty"`
	Retailer       string `json:"retailer,omitempty"`
	IncludeRemoved bool   `json:"include_removed"`
}

type Context struct {
	Preferences       sqlite.Preferences `json:"preferences"`
	Categories        []string           `json:"categories"`
	Capabilities      []string           `json:"capabilities"`
	ActiveListID      *string            `json:"active_list_id"`
	SelectionRevision int64              `json:"selection_revision"`
}

type ActivityInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitzero"`
}

type ActivityPage struct {
	Events     []sqlite.Activity `json:"events"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type WriteResult[T any] struct {
	Record   T    `json:"record"`
	Replayed bool `json:"replayed"`
}
