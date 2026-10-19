package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Product struct {
	ID           string        `json:"id"`
	SourceKey    *string       `json:"source_key"`
	Name         string        `json:"name"`
	Brand        string        `json:"brand"`
	Flavor       string        `json:"flavor"`
	Category     string        `json:"category"`
	PackageCount *int64        `json:"package_count"`
	PackageSize  *string       `json:"package_size"`
	PackageUnit  *string       `json:"package_unit"`
	Description  string        `json:"description"`
	Archived     bool          `json:"archived"`
	Revision     int64         `json:"revision"`
	Sources      []FieldSource `json:"sources"`
	Media        []Media       `json:"media"`
	Nutrition    *Nutrition    `json:"nutrition"`
	Listings     []Listing     `json:"listings"`
}

type List struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Retailer *string `json:"retailer"`
	Status   string  `json:"status"`
	Revision int64   `json:"revision"`
}

type ListItem struct {
	ID        string  `json:"id"`
	ListID    string  `json:"list_id"`
	ProductID string  `json:"product_id"`
	Quantity  string  `json:"quantity"`
	PickedUp  bool    `json:"picked_up"`
	Revision  int64   `json:"revision"`
	RemovedAt *int64  `json:"removed_at"`
	Retailer  *string `json:"retailer"`
}

func (tx *Tx) InsertProduct(product Product) error {
	if product.ID == "" || len(product.ID) > 128 || strings.TrimSpace(product.Name) == "" || len(product.Name) > 300 {
		return fmt.Errorf("product requires bounded ID and name")
	}
	switch product.Category {
	case "produce", "meat", "drinks", "pantry", "dairy", "other":
	default:
		return fmt.Errorf("unknown product category")
	}
	_, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO products(owner_id, id, name, category, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, tx.owner.id, product.ID, product.Name, product.Category, tx.now, tx.now)
	if err == nil {
		tx.affected = append(tx.affected, affectedRecord{Kind: "product", ID: product.ID})
	}
	return err
}

func (s *Store) Product(ctx context.Context, owner Owner, id string) (Product, error) {
	if owner.id == "" {
		return Product{}, ErrOwnerRequired
	}
	var product Product
	err := s.read.QueryRowContext(ctx, "SELECT id, name, category, revision FROM products WHERE owner_id = ? AND id = ?", owner.id, id).
		Scan(&product.ID, &product.Name, &product.Category, &product.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	return product, err
}

func (tx *Tx) InsertList(list List) error {
	if list.ID == "" || len(list.ID) > 128 || strings.TrimSpace(list.Title) == "" || len(list.Title) > 200 {
		return fmt.Errorf("list requires bounded ID and title")
	}
	_, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO shopping_lists(owner_id, id, title, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, tx.owner.id, list.ID, list.Title, tx.now, tx.now)
	if err == nil {
		tx.affected = append(tx.affected, affectedRecord{Kind: "list", ID: list.ID})
	}
	return err
}

func (s *Store) List(ctx context.Context, owner Owner, id string) (List, error) {
	if owner.id == "" {
		return List{}, ErrOwnerRequired
	}
	var list List
	err := s.read.QueryRowContext(ctx, "SELECT id, title, revision FROM shopping_lists WHERE owner_id = ? AND id = ?", owner.id, id).
		Scan(&list.ID, &list.Title, &list.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrNotFound
	}
	return list, err
}

var positiveDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.[0-9]{1,6})?$`)

func (tx *Tx) InsertListItem(item ListItem) error {
	if item.ID == "" || len(item.ID) > 128 || !positiveDecimal.MatchString(item.Quantity) || strings.Trim(item.Quantity, "0.") == "" {
		return fmt.Errorf("list item requires a bounded ID and positive decimal quantity")
	}
	// Check both relationships with owner predicates. The composite foreign keys
	// remain a second boundary if a future repository forgets one of these checks.
	var listExists, productExists bool
	if err := tx.sql.QueryRowContext(tx.ctx, `SELECT
		EXISTS (SELECT 1 FROM shopping_lists WHERE owner_id = ? AND id = ?),
		EXISTS (SELECT 1 FROM products WHERE owner_id = ? AND id = ?)`, tx.owner.id, item.ListID, tx.owner.id, item.ProductID).
		Scan(&listExists, &productExists); err != nil {
		return err
	}
	if !listExists || !productExists {
		return ErrNotFound
	}
	_, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO list_items(owner_id, id, list_id, product_id, quantity, picked_up, created_at, updated_at, created_by, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, tx.owner.id, item.ID, item.ListID, item.ProductID, item.Quantity, item.PickedUp, tx.now, tx.now, tx.identity.Subject, tx.identity.Subject)
	if err == nil {
		tx.affected = append(tx.affected, affectedRecord{Kind: "list_item", ID: item.ID})
	}
	return err
}

func (s *Store) ListItem(ctx context.Context, owner Owner, id string) (ListItem, error) {
	if owner.id == "" {
		return ListItem{}, ErrOwnerRequired
	}
	var item ListItem
	err := s.read.QueryRowContext(ctx, `SELECT id, list_id, product_id, quantity, picked_up, revision
		FROM list_items WHERE owner_id = ? AND id = ?`, owner.id, id).
		Scan(&item.ID, &item.ListID, &item.ProductID, &item.Quantity, &item.PickedUp, &item.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ListItem{}, ErrNotFound
	}
	return item, err
}
