// Package shopping is the shared authorization and mutation boundary for browser
// and MCP adapters. It accepts verified principals, never owner/client arguments.
package shopping

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
)

type Service struct{ store *sqlite.Store }

// Ready checks the shared durable store used by both authenticated adapters.
func (s *Service) Ready(ctx context.Context) error { return s.store.Ready(ctx) }

func New(store *sqlite.Store) *Service { return &Service{store: store} }

func (s *Service) require(ctx context.Context, p identity.Principal, scope string) error {
	if err := p.Require(scope); err != nil {
		return err
	}
	// A cached Principal cannot keep reading after operator disable.
	owner, err := s.store.OwnerForIdentity(ctx, p.Identity())
	if err != nil || owner != p.Owner() {
		return identity.ErrUnauthenticated
	}
	return nil
}

func (s *Service) GetContext(ctx context.Context, p identity.Principal) (Context, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return Context{}, err
	}
	prefs, err := s.store.Preferences(ctx, p.Owner())
	if err != nil {
		return Context{}, err
	}
	list, err := s.store.ListView(ctx, p.Owner(), "", "", false)
	if err != nil {
		return Context{}, err
	}
	caps := []string{"products:read", "lists:read", "purchases:read", "insights:read", "activity:read"}
	for scope, name := range map[string]string{identity.ProductsWrite: "products:write", identity.ListsWrite: "lists:write", identity.PurchasesWrite: "purchases:write", identity.PreferencesWrite: "preferences:write"} {
		if p.Allows(scope) {
			caps = append(caps, name)
		}
	}
	slices.Sort(caps)
	return Context{Preferences: prefs, Categories: slices.Clone(categories), Capabilities: caps, ActiveListID: list.ActiveListID, SelectionRevision: list.SelectionRevision}, nil
}

func (s *Service) GetProduct(ctx context.Context, p identity.Principal, id string) (sqlite.Product, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return sqlite.Product{}, err
	}
	if !validID(id) {
		return sqlite.Product{}, invalid("product_id is required")
	}
	return s.store.FullProduct(ctx, p.Owner(), id)
}

func (s *Service) SearchProducts(ctx context.Context, p identity.Principal, input SearchInput) (ProductPage, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return ProductPage{}, err
	}
	if len(input.Query) > 200 || hasControl(input.Query) {
		return ProductPage{}, invalid("query must contain at most 200 plain-text bytes")
	}
	limit, err := pageLimit(input.Limit, 50)
	if err != nil {
		return ProductPage{}, err
	}
	var after string
	if input.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil || len(raw) > 128 || !validID(string(raw)) {
			return ProductPage{}, invalid("invalid product cursor")
		}
		after = string(raw)
	}
	products, err := s.store.SearchProducts(ctx, p.Owner(), strings.TrimSpace(input.Query), after, limit+1, input.IncludeArchived)
	if err != nil {
		return ProductPage{}, err
	}
	page := ProductPage{Products: products}
	if len(products) > limit {
		page.Products = products[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(products[limit-1].ID))
	}
	return page, nil
}

func (s *Service) GetList(ctx context.Context, p identity.Principal, input ListInput) (sqlite.ListView, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return sqlite.ListView{}, err
	}
	if input.ListID != "" && !validID(input.ListID) {
		return sqlite.ListView{}, invalid("invalid list_id")
	}
	if input.Retailer != "" && !plain(input.Retailer, 128) {
		return sqlite.ListView{}, invalid("invalid retailer")
	}
	return s.store.ListView(ctx, p.Owner(), input.ListID, input.Retailer, input.IncludeRemoved)
}

func (s *Service) SaveProduct(ctx context.Context, p identity.Principal, input SaveProductInput) (WriteResult[sqlite.Product], error) {
	if err := s.require(ctx, p, identity.ProductsWrite); err != nil {
		return WriteResult[sqlite.Product]{}, err
	}
	if err := validateProduct(input.Product); err != nil {
		return WriteResult[sqlite.Product]{}, err
	}
	if input.ExpectedRevision < 0 || (input.ExpectedRevision == 0 && input.Product.ID != "") || (input.ExpectedRevision > 0 && !validID(input.Product.ID)) {
		return WriteResult[sqlite.Product]{}, invalid("create with an empty product.id and revision 0, or revise an existing id and positive revision")
	}
	return write(s, ctx, p, input.WriteMeta, "save_product", input, func(tx *sqlite.Tx) (sqlite.Product, error) {
		product := input.Product
		if product.ID == "" {
			product.ID = rand.Text()
		}
		return tx.SaveProduct(product, input.ExpectedRevision)
	})
}

func (s *Service) ArchiveProduct(ctx context.Context, p identity.Principal, input ArchiveProductInput) (WriteResult[sqlite.Product], error) {
	if err := s.require(ctx, p, identity.ProductsWrite); err != nil {
		return WriteResult[sqlite.Product]{}, err
	}
	if !validID(input.ProductID) || input.ExpectedRevision < 1 {
		return WriteResult[sqlite.Product]{}, invalid("product id and positive expected_revision are required")
	}
	return write(s, ctx, p, input.WriteMeta, "archive_product", input, func(tx *sqlite.Tx) (sqlite.Product, error) {
		return tx.ArchiveProduct(input.ProductID, input.ExpectedRevision, input.Archived)
	})
}

func (s *Service) SaveList(ctx context.Context, p identity.Principal, input SaveListInput) (WriteResult[sqlite.ListView], error) {
	if err := s.require(ctx, p, identity.ListsWrite); err != nil {
		return WriteResult[sqlite.ListView]{}, err
	}
	if err := validateListInput(input); err != nil {
		return WriteResult[sqlite.ListView]{}, err
	}
	return write(s, ctx, p, input.WriteMeta, "save_list", input, func(tx *sqlite.Tx) (sqlite.ListView, error) {
		id := input.ListID
		if id == "" {
			id = rand.Text()
		}
		status := "active"
		if input.Archived {
			status = "archived"
		}
		return tx.SaveList(sqlite.List{ID: id, Title: input.Title, Retailer: input.Retailer, Status: status}, input.ExpectedRevision, input.MakeActive, input.ExpectedSelectionRevision)
	})
}

func (s *Service) UpdateItems(ctx context.Context, p identity.Principal, input UpdateItemsInput) (WriteResult[sqlite.ListView], error) {
	if err := s.require(ctx, p, identity.ListsWrite); err != nil {
		return WriteResult[sqlite.ListView]{}, err
	}
	if err := validateOperations(input); err != nil {
		return WriteResult[sqlite.ListView]{}, err
	}
	return write(s, ctx, p, input.WriteMeta, "update_list_items", input, func(tx *sqlite.Tx) (sqlite.ListView, error) {
		ids := make([]string, len(input.Operations))
		for i := range ids {
			ids[i] = rand.Text()
		}
		return tx.ApplyItems(input.ListID, input.ExpectedRevision, input.Operations, ids)
	})
}

func (s *Service) UpdatePreferences(ctx context.Context, p identity.Principal, input PreferencesInput) (WriteResult[sqlite.Preferences], error) {
	if err := s.require(ctx, p, identity.PreferencesWrite); err != nil {
		return WriteResult[sqlite.Preferences]{}, err
	}
	if err := input.Preferences.Validate(); err != nil {
		return WriteResult[sqlite.Preferences]{}, invalid("%s", err)
	}
	if input.ExpectedRevision < 1 {
		return WriteResult[sqlite.Preferences]{}, invalid("positive expected_revision required")
	}
	return write(s, ctx, p, input.WriteMeta, "update_preferences", input, func(tx *sqlite.Tx) (sqlite.Preferences, error) {
		old, err := s.store.Preferences(ctx, p.Owner())
		if err != nil {
			return sqlite.Preferences{}, err
		}
		if err := tx.SetPreferences(input.Preferences, input.ExpectedRevision); err != nil {
			return sqlite.Preferences{}, err
		}
		prefs := input.Preferences
		prefs.Revision = input.ExpectedRevision + 1
		return prefs, tx.RecordChange("preferences", p.Owner().ID(), old, prefs)
	})
}

func (s *Service) GetActivity(ctx context.Context, p identity.Principal, input ActivityInput) (ActivityPage, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return ActivityPage{}, err
	}
	limit, err := pageLimit(input.Limit, 10)
	if err != nil {
		return ActivityPage{}, err
	}
	var after int64
	var afterID string
	if input.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil || len(raw) > 160 {
			return ActivityPage{}, invalid("invalid activity cursor")
		}
		stamp, id, ok := strings.Cut(string(raw), ":")
		after, err = strconv.ParseInt(stamp, 10, 64)
		if !ok || err != nil || after < 0 || !validID(id) {
			return ActivityPage{}, invalid("invalid activity cursor")
		}
		afterID = id
	}
	events, err := s.store.Activity(ctx, p.Owner(), after, afterID, limit+1)
	if err != nil {
		return ActivityPage{}, err
	}
	page := ActivityPage{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		a := events[limit-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d:%s", a.OccurredAt, a.ID))
	}
	return page, nil
}
