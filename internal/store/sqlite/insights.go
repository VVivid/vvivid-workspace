package sqlite

import (
	"context"
	"fmt"
)

// InsightLine is a projection of the same immutable current-confirmed snapshots
// returned by History. No request can supply totals or a calculation expression.
type InsightLine struct {
	PurchasedAt int64
	Currency    string
	Category    string
	Quantity    string
	Unit        string
	PackCount   *int64
	PackSize    *string
	PackUnit    *string
	Merchandise *int64
	Tax         *int64
}

func (s *Store) InsightLines(ctx context.Context, owner Owner, from, to int64) ([]InsightLine, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	rows, err := s.read.QueryContext(ctx, `SELECT p.purchased_at,p.currency,l.category_snapshot,l.quantity,l.quantity_unit,
		l.package_count_snapshot,l.package_size_snapshot,l.package_unit_snapshot,l.line_total_minor,l.tax_minor
		FROM purchases p JOIN purchase_lines l ON l.owner_id=p.owner_id AND l.purchase_id=p.id
		WHERE p.owner_id=? AND p.status='confirmed' AND p.purchased_at>=? AND p.purchased_at<?
		ORDER BY p.purchased_at,p.id,l.line_number,l.id LIMIT 25001`, owner.id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []InsightLine{}
	for rows.Next() {
		var line InsightLine
		if err := rows.Scan(&line.PurchasedAt, &line.Currency, &line.Category, &line.Quantity, &line.Unit, &line.PackCount, &line.PackSize, &line.PackUnit, &line.Merchandise, &line.Tax); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	if len(lines) > 25000 {
		return nil, fmt.Errorf("%w: narrow the insights date range", ErrLimit)
	}
	return lines, rows.Err()
}
