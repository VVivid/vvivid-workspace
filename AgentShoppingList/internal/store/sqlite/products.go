package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

type FieldSource struct {
	Fields       []string `json:"fields"`
	URL          string   `json:"url"`
	ObservedAt   int64    `json:"observed_at"`
	Verification string   `json:"verification"`
}

type Media struct {
	ImageURL   string `json:"image_url"`
	SourceURL  string `json:"source_url"`
	ObservedAt int64  `json:"observed_at"`
}

type NutritionFact struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
	Unit  string  `json:"unit"`
}

type Nutrition struct {
	ServingSize  string          `json:"serving_size"`
	ServingUnit  string          `json:"serving_unit"`
	Facts        []NutritionFact `json:"facts"`
	SourceURL    string          `json:"source_url"`
	ObservedAt   int64           `json:"observed_at"`
	Verification string          `json:"verification"`
}

type Listing struct {
	Retailer      string  `json:"retailer"`
	ItemID        *string `json:"item_id"`
	ProductURL    string  `json:"product_url"`
	StoreLocation *string `json:"store_location"`
	PriceMinor    *int64  `json:"price_minor"`
	Currency      *string `json:"currency"`
	ObservedAt    *int64  `json:"observed_at"`
	Availability  string  `json:"availability"`
}

// ProductSummary deliberately excludes imported descriptions, media and sources.
// List/catalog reads remain bounded even when detail records contain nutrition.
type ProductSummary struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Brand        string  `json:"brand"`
	Flavor       string  `json:"flavor"`
	Category     string  `json:"category"`
	PackageCount *int64  `json:"package_count"`
	PackageSize  *string `json:"package_size"`
	PackageUnit  *string `json:"package_unit"`
	Archived     bool    `json:"archived"`
	Revision     int64   `json:"revision"`
}

const productColumns = `id, source_key, name, brand, flavor, category, package_count, package_size, package_unit, description, archived, revision, sources_json`
const summaryColumns = `p.id, p.name, p.brand, p.flavor, p.category, p.package_count, p.package_size, p.package_unit, p.archived, p.revision`

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readProduct(ctx context.Context, q queryer, owner Owner, id string) (Product, error) {
	p := Product{Media: []Media{}, Listings: []Listing{}}
	var sources string
	err := q.QueryRowContext(ctx, `SELECT `+productColumns+` FROM products WHERE owner_id = ? AND id = ?`, owner.id, id).Scan(
		&p.ID, &p.SourceKey, &p.Name, &p.Brand, &p.Flavor, &p.Category, &p.PackageCount, &p.PackageSize, &p.PackageUnit, &p.Description, &p.Archived, &p.Revision, &sources)
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(sources), &p.Sources); err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT image_url, source_url, observed_at FROM product_media WHERE owner_id = ? AND product_id = ? ORDER BY id`, owner.id, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var m Media
		if err := rows.Scan(&m.ImageURL, &m.SourceURL, &m.ObservedAt); err != nil {
			rows.Close()
			return p, err
		}
		p.Media = append(p.Media, m)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return p, err
	}
	var n Nutrition
	var facts string
	err = q.QueryRowContext(ctx, `SELECT serving_size, serving_unit, facts_json, source_url, observed_at, verification FROM product_nutrition WHERE owner_id = ? AND product_id = ?`, owner.id, id).Scan(&n.ServingSize, &n.ServingUnit, &facts, &n.SourceURL, &n.ObservedAt, &n.Verification)
	if err == nil {
		if err = json.Unmarshal([]byte(facts), &n.Facts); err != nil {
			return p, err
		}
		p.Nutrition = &n
	} else if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	rows, err = q.QueryContext(ctx, `SELECT retailer, retailer_item_id, product_url, store_location, price_minor, currency, observed_at, availability FROM retailer_listings WHERE owner_id = ? AND product_id = ? ORDER BY id`, owner.id, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var l Listing
		if err := rows.Scan(&l.Retailer, &l.ItemID, &l.ProductURL, &l.StoreLocation, &l.PriceMinor, &l.Currency, &l.ObservedAt, &l.Availability); err != nil {
			rows.Close()
			return p, err
		}
		p.Listings = append(p.Listings, l)
	}
	return p, errors.Join(rows.Err(), rows.Close())
}

func (s *Store) FullProduct(ctx context.Context, owner Owner, id string) (Product, error) {
	if owner.id == "" {
		return Product{}, ErrOwnerRequired
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Product{}, err
	}
	defer tx.Rollback()
	p, err := readProduct(ctx, tx, owner, id)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (tx *Tx) FullProduct(id string) (Product, error) {
	return readProduct(tx.ctx, tx.sql, tx.owner, id)
}

func (tx *Tx) SaveProduct(p Product, expected int64) (Product, error) {
	var before any
	if expected == 0 {
		var found bool
		if err := tx.sql.QueryRowContext(tx.ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE owner_id = ? AND (id = ? OR (? IS NOT NULL AND source_key = ?)))`, tx.owner.id, p.ID, p.SourceKey, p.SourceKey).Scan(&found); err != nil {
			return p, err
		}
		if found {
			return p, ErrConflict
		}
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO products(owner_id,id,name,category,created_at,updated_at) VALUES (?,?,?,?,?,?)`, tx.owner.id, p.ID, p.Name, p.Category, tx.now, tx.now); err != nil {
			return p, err
		}
	} else {
		old, err := tx.FullProduct(p.ID)
		if err != nil {
			return p, err
		}
		if old.Revision != expected {
			return p, ErrConflict
		}
		before = old
	}
	sources, err := json.Marshal(p.Sources)
	if err != nil {
		return p, err
	}
	// A stable source key identifies one exact variant. It never merges by name.
	var collision bool
	if err := tx.sql.QueryRowContext(tx.ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE owner_id = ? AND source_key = ? AND id <> ?)`, tx.owner.id, p.SourceKey, p.ID).Scan(&collision); err != nil {
		return p, err
	}
	if collision {
		return p, ErrConflict
	}
	revision := expected + 1
	if _, err = tx.sql.ExecContext(tx.ctx, `UPDATE products SET source_key=?, name=?, brand=?, flavor=?, category=?, package_count=?, package_size=?, package_unit=?, description=?, archived=?, revision=?, updated_at=?, sources_json=? WHERE owner_id=? AND id=?`, p.SourceKey, p.Name, p.Brand, p.Flavor, p.Category, p.PackageCount, p.PackageSize, p.PackageUnit, p.Description, p.Archived, revision, tx.now, string(sources), tx.owner.id, p.ID); err != nil {
		return p, err
	}
	for _, table := range []string{"product_media", "product_nutrition", "retailer_listings"} {
		if _, err := tx.sql.ExecContext(tx.ctx, `DELETE FROM `+table+` WHERE owner_id = ? AND product_id = ?`, tx.owner.id, p.ID); err != nil {
			return p, err
		}
	}
	for i, m := range p.Media {
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO product_media(owner_id,id,product_id,image_url,source_url,observed_at) VALUES (?,?,?,?,?,?)`, tx.owner.id, p.ID+"-media-"+strconv.Itoa(i), p.ID, m.ImageURL, m.SourceURL, m.ObservedAt); err != nil {
			return p, err
		}
	}
	if n := p.Nutrition; n != nil {
		facts, err := json.Marshal(n.Facts)
		if err != nil {
			return p, err
		}
		if _, err = tx.sql.ExecContext(tx.ctx, `INSERT INTO product_nutrition(owner_id,product_id,serving_size,serving_unit,facts_json,source_url,observed_at,verification) VALUES (?,?,?,?,?,?,?,?)`, tx.owner.id, p.ID, n.ServingSize, n.ServingUnit, string(facts), n.SourceURL, n.ObservedAt, n.Verification); err != nil {
			return p, err
		}
	}
	for i, l := range p.Listings {
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO retailer_listings(owner_id,id,product_id,retailer,retailer_item_id,product_url,store_location,price_minor,currency,observed_at,availability) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, tx.owner.id, p.ID+"-listing-"+strconv.Itoa(i), p.ID, l.Retailer, l.ItemID, l.ProductURL, l.StoreLocation, l.PriceMinor, l.Currency, l.ObservedAt, l.Availability); err != nil {
			return p, err
		}
	}
	p.Revision = revision
	if err := tx.RecordChange("product", p.ID, before, p); err != nil {
		return p, err
	}
	return p, nil
}

func (tx *Tx) ArchiveProduct(id string, expected int64, archived bool) (Product, error) {
	p, err := tx.FullProduct(id)
	if err != nil {
		return p, err
	}
	if p.Revision != expected {
		return p, ErrConflict
	}
	before := p
	p.Archived = archived
	p.Revision++
	if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE products SET archived=?,revision=?,updated_at=? WHERE owner_id=? AND id=?`, p.Archived, p.Revision, tx.now, tx.owner.id, id); err != nil {
		return p, err
	}
	return p, tx.RecordChange("product", id, before, p)
}

func (s *Store) SearchProducts(ctx context.Context, owner Owner, q, after string, limit int, archived bool) ([]ProductSummary, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	rows, err := s.read.QueryContext(ctx, `SELECT `+summaryColumns+` FROM products p WHERE p.owner_id=? AND p.id>? AND (? OR p.archived=0) AND instr(lower(p.name || ' ' || p.brand || ' ' || p.flavor),lower(?))>0 ORDER BY p.id LIMIT ?`, owner.id, after, archived, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	products := []ProductSummary{}
	for rows.Next() {
		var p ProductSummary
		if err := rows.Scan(&p.ID, &p.Name, &p.Brand, &p.Flavor, &p.Category, &p.PackageCount, &p.PackageSize, &p.PackageUnit, &p.Archived, &p.Revision); err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, rows.Err()
}
