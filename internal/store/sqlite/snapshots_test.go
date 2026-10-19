package sqlite

import (
	"database/sql"
	"testing"
)

func TestConfirmedSnapshotsAndAuditRemainImmutable(t *testing.T) {
	store, _ := newTestStore(t)
	owner := enrollTestOwner(t, store, "alice")
	if _, err := store.Write(t.Context(), owner, testMutation("product"), func(tx *Tx) ([]byte, error) {
		return []byte(`{}`), tx.InsertProduct(Product{ID: "polar", Name: "Original package name", Category: "drinks"})
	}); err != nil {
		t.Fatal(err)
	}
	// Raw SQL fixtures exercise persistence invariants before CP07 introduces
	// purchase services. No runtime purchase or confirmation adapter exists yet.
	if _, err := store.write.ExecContext(t.Context(), `INSERT INTO purchases(owner_id, id, retailer, purchased_at, currency, source_kind, created_at, updated_at)
		VALUES (?, 'purchase', 'walmart', 1, 'USD', 'user_confirmation', 1, 1)`, owner.id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.ExecContext(t.Context(), `INSERT INTO purchase_lines(owner_id, id, purchase_id, product_id, name_snapshot, category_snapshot, package_count_snapshot, package_size_snapshot, package_unit_snapshot, quantity, quantity_unit)
		VALUES (?, 'line', 'purchase', 'polar', 'Original package name', 'drinks', 8, '12', 'fl_oz', '1', 'pack')`, owner.id); err != nil {
		t.Fatal(err)
	}
	var amount sql.NullInt64
	if err := store.read.QueryRowContext(t.Context(), "SELECT line_total_minor FROM purchase_lines WHERE owner_id = ? AND id = 'line'", owner.id).Scan(&amount); err != nil || amount.Valid {
		t.Fatalf("unknown price must remain null: %#v, %v", amount, err)
	}
	if _, err := store.write.ExecContext(t.Context(), `INSERT INTO purchase_lines(owner_id, id, purchase_id, name_snapshot, category_snapshot, quantity, quantity_unit, line_total_minor)
		VALUES (?, 'zero-price', 'purchase', 'Zero-price fixture', 'drinks', '1', 'pack', 0)`, owner.id); err != nil {
		t.Fatal(err)
	}
	if err := store.read.QueryRowContext(t.Context(), "SELECT line_total_minor FROM purchase_lines WHERE owner_id = ? AND id = 'zero-price'", owner.id).Scan(&amount); err != nil || !amount.Valid || amount.Int64 != 0 {
		t.Fatalf("known zero price became unknown: %#v, %v", amount, err)
	}
	if _, err := store.write.ExecContext(t.Context(), "UPDATE purchases SET status = 'confirmed' WHERE owner_id = ? AND id = 'purchase'", owner.id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.ExecContext(t.Context(), "UPDATE products SET name = 'Revised product name', revision = revision + 1 WHERE owner_id = ? AND id = 'polar'", owner.id); err != nil {
		t.Fatal(err)
	}
	var snapshot string
	if err := store.read.QueryRowContext(t.Context(), "SELECT name_snapshot FROM purchase_lines WHERE owner_id = ? AND id = 'line'", owner.id).Scan(&snapshot); err != nil || snapshot != "Original package name" {
		t.Fatalf("product edit rewrote history: %q, %v", snapshot, err)
	}
	for name, statement := range map[string]string{
		"change snapshot": `UPDATE purchase_lines SET name_snapshot = 'rewritten' WHERE owner_id = ?`,
		"delete snapshot": `DELETE FROM purchase_lines WHERE owner_id = ?`,
		"append snapshot": `INSERT INTO purchase_lines(owner_id, id, purchase_id, name_snapshot, category_snapshot, quantity, quantity_unit) VALUES (?, 'extra', 'purchase', 'extra', 'drinks', '1', 'pack')`,
		"reopen draft":    `UPDATE purchases SET status = 'draft' WHERE owner_id = ?`,
		"delete purchase": `DELETE FROM purchases WHERE owner_id = ?`,
		"rewrite audit":   `UPDATE audit_events SET operation = 'rewritten' WHERE owner_id = ?`,
		"delete audit":    `DELETE FROM audit_events WHERE owner_id = ?`,
		"void no reason":  `UPDATE purchases SET status = 'void' WHERE owner_id = ?`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.write.ExecContext(t.Context(), statement, owner.id); err == nil {
				t.Fatal("immutable evidence was changed")
			}
		})
	}
	if _, err := store.write.ExecContext(t.Context(), "UPDATE purchases SET status = 'void', void_reason = 'fixture correction' WHERE owner_id = ?", owner.id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.ExecContext(t.Context(), "UPDATE purchase_lines SET quantity = '2' WHERE owner_id = ?", owner.id); err == nil {
		t.Fatal("voided snapshot became mutable")
	}
}
