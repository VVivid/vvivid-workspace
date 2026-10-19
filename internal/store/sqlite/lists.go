package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type ListEntry struct {
	ListItem
	Product ProductSummary `json:"product"`
}

type ListView struct {
	List              *List       `json:"list"`
	ActiveListID      *string     `json:"active_list_id"`
	SelectionRevision int64       `json:"selection_revision"`
	Items             []ListEntry `json:"items"`
}

type ItemOperation struct {
	Kind                    string  `json:"kind"`
	ItemID                  string  `json:"item_id,omitempty"`
	ProductID               string  `json:"product_id,omitempty"`
	ExpectedRevision        int64   `json:"expected_revision"`
	Quantity                *string `json:"quantity"`
	PickedUp                *bool   `json:"picked_up"`
	Merge                   string  `json:"merge,omitempty"`
	Retailer                *string `json:"retailer"`
	ExpectedProductRevision *int64  `json:"expected_product_revision,omitempty"`
}

func readList(ctx context.Context, q queryer, owner Owner, id string) (List, error) {
	var l List
	err := q.QueryRowContext(ctx, `SELECT id,title,retailer,status,revision FROM shopping_lists WHERE owner_id=? AND id=?`, owner.id, id).Scan(&l.ID, &l.Title, &l.Retailer, &l.Status, &l.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

func readListView(ctx context.Context, q queryer, owner Owner, id, retailer string, removed bool) (ListView, error) {
	view := ListView{Items: []ListEntry{}}
	err := q.QueryRowContext(ctx, `SELECT list_id,revision FROM active_lists WHERE owner_id=?`, owner.id).Scan(&view.ActiveListID, &view.SelectionRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if id == "" {
		if view.ActiveListID == nil {
			return view, nil
		}
		id = *view.ActiveListID
	}
	l, err := readList(ctx, q, owner, id)
	if err != nil {
		return view, err
	}
	view.List = &l
	rows, err := q.QueryContext(ctx, `SELECT i.id,i.list_id,i.product_id,i.quantity,i.picked_up,i.revision,i.removed_at,i.retailer,`+summaryColumns+` FROM list_items i JOIN products p ON p.owner_id=i.owner_id AND p.id=i.product_id WHERE i.owner_id=? AND i.list_id=? AND (? OR i.removed_at IS NULL) AND (?='' OR i.retailer IS NULL OR lower(i.retailer)=lower(?)) ORDER BY i.created_at,i.id LIMIT 501`, owner.id, id, removed, retailer, retailer)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ListEntry
		p := &e.Product
		if err := rows.Scan(&e.ID, &e.ListID, &e.ProductID, &e.Quantity, &e.PickedUp, &e.Revision, &e.RemovedAt, &e.Retailer, &p.ID, &p.Name, &p.Brand, &p.Flavor, &p.Category, &p.PackageCount, &p.PackageSize, &p.PackageUnit, &p.Archived, &p.Revision); err != nil {
			return view, err
		}
		view.Items = append(view.Items, e)
	}
	if len(view.Items) > 500 {
		return view, fmt.Errorf("%w: list has more than 500 retained items; use a new list", ErrLimit)
	}
	return view, rows.Err()
}

func (s *Store) ListView(ctx context.Context, owner Owner, id, retailer string, removed bool) (ListView, error) {
	if owner.id == "" {
		return ListView{}, ErrOwnerRequired
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ListView{}, err
	}
	defer tx.Rollback()
	view, err := readListView(ctx, tx, owner, id, retailer, removed)
	if err != nil {
		return view, err
	}
	return view, tx.Commit()
}

func (tx *Tx) ListView(id string, removed bool) (ListView, error) {
	return readListView(tx.ctx, tx.sql, tx.owner, id, "", removed)
}

func (tx *Tx) SaveList(l List, expected int64, selectActive bool, selectionRevision int64) (ListView, error) {
	var before any
	if expected == 0 {
		var exists bool
		if err := tx.sql.QueryRowContext(tx.ctx, `SELECT EXISTS(SELECT 1 FROM shopping_lists WHERE owner_id=? AND id=?)`, tx.owner.id, l.ID).Scan(&exists); err != nil {
			return ListView{}, err
		}
		if exists {
			return ListView{}, ErrConflict
		}
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO shopping_lists(owner_id,id,title,retailer,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, tx.owner.id, l.ID, l.Title, l.Retailer, l.Status, tx.now, tx.now); err != nil {
			return ListView{}, err
		}
	} else {
		old, err := readList(tx.ctx, tx.sql, tx.owner, l.ID)
		if err != nil {
			return ListView{}, err
		}
		if old.Revision != expected {
			return ListView{}, ErrConflict
		}
		before = old
		if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE shopping_lists SET title=?,retailer=?,status=?,revision=revision+1,updated_at=? WHERE owner_id=? AND id=?`, l.Title, l.Retailer, l.Status, tx.now, tx.owner.id, l.ID); err != nil {
			return ListView{}, err
		}
	}
	l.Revision = expected + 1
	if selectActive {
		var current int64
		err := tx.sql.QueryRowContext(tx.ctx, `SELECT revision FROM active_lists WHERE owner_id=?`, tx.owner.id).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ListView{}, err
		}
		if current != selectionRevision {
			return ListView{}, ErrConflict
		}
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO active_lists(owner_id,list_id,revision) VALUES (?,?,1) ON CONFLICT(owner_id) DO UPDATE SET list_id=excluded.list_id,revision=active_lists.revision+1`, tx.owner.id, l.ID); err != nil {
			return ListView{}, err
		}
		if err := tx.RecordChange("active_list", l.ID, current, current+1); err != nil {
			return ListView{}, err
		}
	} else if l.Status == "archived" {
		if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE active_lists SET list_id=NULL,revision=revision+1 WHERE owner_id=? AND list_id=?`, tx.owner.id, l.ID); err != nil {
			return ListView{}, err
		}
	}
	if err := tx.RecordChange("list", l.ID, before, l); err != nil {
		return ListView{}, err
	}
	return tx.ListView(l.ID, false)
}

func (tx *Tx) ApplyItems(listID string, expected int64, ops []ItemOperation, newIDs []string) (ListView, error) {
	l, err := readList(tx.ctx, tx.sql, tx.owner, listID)
	if err != nil {
		return ListView{}, err
	}
	if l.Revision != expected {
		return ListView{}, ErrConflict
	}
	if l.Status != "active" {
		return ListView{}, ErrConflict
	}
	for i, op := range ops {
		if op.Kind == "add" {
			if err := tx.addItem(l.ID, op, newIDs[i]); err != nil {
				return ListView{}, err
			}
			continue
		}
		item, err := tx.item(l.ID, op.ItemID)
		if err != nil {
			return ListView{}, err
		}
		if item.Revision != op.ExpectedRevision {
			return ListView{}, ErrConflict
		}
		before := item
		switch op.Kind {
		case "set_quantity":
			if item.RemovedAt != nil {
				return ListView{}, ErrConflict
			}
			item.Quantity = *op.Quantity
		case "set_picked_up":
			if item.RemovedAt != nil {
				return ListView{}, ErrConflict
			}
			item.PickedUp = *op.PickedUp
		case "remove":
			if item.RemovedAt != nil {
				return ListView{}, ErrConflict
			}
			item.RemovedAt = new(tx.now)
		case "restore":
			if item.RemovedAt == nil {
				return ListView{}, ErrConflict
			}
			if err := tx.availableProduct(item.ProductID); err != nil {
				return ListView{}, err
			}
			var live bool
			if err := tx.sql.QueryRowContext(tx.ctx, `SELECT EXISTS(SELECT 1 FROM list_items WHERE owner_id=? AND list_id=? AND product_id=? AND removed_at IS NULL)`, tx.owner.id, l.ID, item.ProductID).Scan(&live); err != nil {
				return ListView{}, err
			}
			if live {
				return ListView{}, ErrConflict
			}
			item.RemovedAt = nil
		default:
			return ListView{}, fmt.Errorf("unknown list operation")
		}
		item.Revision++
		if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE list_items SET quantity=?,picked_up=?,removed_at=?,revision=?,updated_at=?,updated_by=? WHERE owner_id=? AND list_id=? AND id=?`, item.Quantity, item.PickedUp, item.RemovedAt, item.Revision, tx.now, tx.identity.Subject, tx.owner.id, l.ID, item.ID); err != nil {
			return ListView{}, err
		}
		if err := tx.RecordChange("list_item", item.ID, before, item); err != nil {
			return ListView{}, err
		}
	}
	var count int
	if err := tx.sql.QueryRowContext(tx.ctx, `SELECT count(*) FROM list_items WHERE owner_id=? AND list_id=?`, tx.owner.id, l.ID).Scan(&count); err != nil {
		return ListView{}, err
	}
	if count > 500 {
		return ListView{}, fmt.Errorf("%w: list is limited to 500 retained items; create a new list", ErrLimit)
	}
	if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE shopping_lists SET revision=revision+1,updated_at=? WHERE owner_id=? AND id=?`, tx.now, tx.owner.id, l.ID); err != nil {
		return ListView{}, err
	}
	if err := tx.RecordChange("list", l.ID, l.Revision, l.Revision+1); err != nil {
		return ListView{}, err
	}
	return tx.ListView(l.ID, false)
}

func (tx *Tx) item(listID, id string) (ListItem, error) {
	var item ListItem
	err := tx.sql.QueryRowContext(tx.ctx, `SELECT id,list_id,product_id,quantity,picked_up,revision,removed_at,retailer FROM list_items WHERE owner_id=? AND list_id=? AND id=?`, tx.owner.id, listID, id).Scan(&item.ID, &item.ListID, &item.ProductID, &item.Quantity, &item.PickedUp, &item.Revision, &item.RemovedAt, &item.Retailer)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (tx *Tx) availableProduct(id string) error {
	var archived bool
	err := tx.sql.QueryRowContext(tx.ctx, `SELECT archived FROM products WHERE owner_id=? AND id=?`, tx.owner.id, id).Scan(&archived)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if archived {
		return ErrConflict
	}
	return nil
}

func (tx *Tx) addItem(listID string, op ItemOperation, id string) error {
	if err := tx.availableProduct(op.ProductID); err != nil {
		return err
	}
	if op.ExpectedProductRevision != nil {
		var revision int64
		if err := tx.sql.QueryRowContext(tx.ctx, `SELECT revision FROM products WHERE owner_id=? AND id=?`, tx.owner.id, op.ProductID).Scan(&revision); err != nil {
			return err
		}
		if revision != *op.ExpectedProductRevision {
			return ErrConflict
		}
	}
	var existingID string
	err := tx.sql.QueryRowContext(tx.ctx, `SELECT id FROM list_items WHERE owner_id=? AND list_id=? AND product_id=? AND removed_at IS NULL ORDER BY created_at,id LIMIT 1`, tx.owner.id, listID, op.ProductID).Scan(&existingID)
	if err == nil {
		old, err := tx.item(listID, existingID)
		if err != nil {
			return err
		}
		item := old
		if op.Merge == "keep" {
			return nil
		}
		if (item.Retailer == nil) != (op.Retailer == nil) || (item.Retailer != nil && *item.Retailer != *op.Retailer) {
			return ErrConflict
		}
		item.Quantity, err = AddDecimal(item.Quantity, *op.Quantity)
		if err != nil {
			return err
		}
		item.Revision++
		// Increasing requested quantity retains picked-up intent; only an explicit set
		// operation changes it. Audit preserves the prior quantity and checked state.
		if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE list_items SET quantity=?,revision=?,updated_at=?,updated_by=? WHERE owner_id=? AND id=?`, item.Quantity, item.Revision, tx.now, tx.identity.Subject, tx.owner.id, item.ID); err != nil {
			return err
		}
		return tx.RecordChange("list_item", item.ID, old, item)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	item := ListItem{ID: id, ListID: listID, ProductID: op.ProductID, Quantity: *op.Quantity, Retailer: op.Retailer, Revision: 1}
	if err := tx.InsertListItem(item); err != nil {
		return err
	}
	if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE list_items SET retailer=? WHERE owner_id=? AND id=?`, op.Retailer, tx.owner.id, id); err != nil {
		return err
	}
	return tx.RecordChange("list_item", id, nil, item)
}

type Change struct {
	Kind   string          `json:"kind"`
	ID     string          `json:"id"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

func (tx *Tx) RecordChange(kind, id string, before, after any) error {
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	tx.changes = append(tx.changes, Change{Kind: kind, ID: id, Before: b, After: a})
	tx.affected = append(tx.affected, affectedRecord{Kind: kind, ID: id})
	return nil
}

type Activity struct {
	ID          string   `json:"id"`
	ActorClient string   `json:"actor_client"`
	Operation   string   `json:"operation"`
	RequestID   string   `json:"request_id"`
	OccurredAt  int64    `json:"occurred_at"`
	Changes     []Change `json:"changes"`
}

func (s *Store) Activity(ctx context.Context, owner Owner, after int64, afterID string, limit int) ([]Activity, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	rows, err := s.read.QueryContext(ctx, `SELECT id,actor_client,operation,request_id,occurred_at,summary_json FROM audit_events WHERE owner_id=? AND (occurred_at>? OR (occurred_at=? AND id>?)) ORDER BY occurred_at,id LIMIT ?`, owner.id, after, after, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Activity{}
	for rows.Next() {
		var a Activity
		var raw string
		if err := rows.Scan(&a.ID, &a.ActorClient, &a.Operation, &a.RequestID, &a.OccurredAt, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &a.Changes); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
