package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

func readPurchase(ctx context.Context, q queryer, owner Owner, id string) (Purchase, error) {
	p := Purchase{Lines: []PurchaseLine{}, Reconciliations: []PurchaseReconciliation{}}
	err := q.QueryRowContext(ctx, `SELECT id,retailer,store_location,purchased_at,currency,status,source_kind,source_reference,
		source_evidence,source_url,source_observed_at,replaces_id,superseded_by_id,void_reason,revision,reconciliation_origin_id
		FROM purchases WHERE owner_id=? AND id=?`, owner.id, id).Scan(
		&p.ID, &p.Retailer, &p.StoreLocation, &p.PurchasedAt, &p.Currency, &p.Status, &p.SourceKind, &p.SourceReference,
		&p.SourceEvidence, &p.SourceURL, &p.SourceObservedAt, &p.ReplacesID, &p.SupersededByID, &p.VoidReason, &p.Revision, &p.OriginalReconciliationPurchase)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id,product_id,quantity,quantity_unit,unit_price_minor,line_total_minor,discount_minor,tax_minor,tax_treatment,source_reference,
		name_snapshot,brand_snapshot,flavor_snapshot,category_snapshot,package_count_snapshot,package_size_snapshot,package_unit_snapshot,product_revision_snapshot,sources_snapshot_json,list_link_json
		FROM purchase_lines WHERE owner_id=? AND purchase_id=? ORDER BY line_number,id`, owner.id, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var line PurchaseLine
		var productID *string
		var sources string
		var link *string
		if err := rows.Scan(&line.ID, &productID, &line.Quantity, &line.QuantityUnit, &line.UnitPriceMinor, &line.LineTotalMinor, &line.DiscountMinor, &line.TaxMinor, &line.TaxTreatment, &line.SourceReference,
			&line.Name, &line.Brand, &line.Flavor, &line.Category, &line.PackageCount, &line.PackageSize, &line.PackageUnit, &line.ProductRevision, &sources, &link); err != nil {
			rows.Close()
			return p, err
		}
		if productID != nil {
			line.ProductID = *productID
		}
		if line.ProductRevision != nil {
			line.ExpectedProductRevision = *line.ProductRevision
		}
		if err := json.Unmarshal([]byte(sources), &line.Sources); err != nil {
			rows.Close()
			return p, err
		}
		if link != nil {
			if err := json.Unmarshal([]byte(*link), &line.ListLink); err != nil {
				rows.Close()
				return p, err
			}
		}
		p.Lines = append(p.Lines, line)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return p, err
	}
	// Replacement records show the original durable reconciliation evidence. The
	// false flag distinguishes inherited evidence from a newly applied side effect.
	origin := id
	if p.OriginalReconciliationPurchase != nil {
		origin = *p.OriginalReconciliationPurchase
	}
	rows, err = q.QueryContext(ctx, `SELECT list_id,item_id,purchased_quantity,before_json,after_json FROM purchase_reconciliations WHERE owner_id=? AND purchase_id=? ORDER BY list_id,item_id`, owner.id, origin)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var change PurchaseReconciliation
		var before, after string
		if err := rows.Scan(&change.ListID, &change.ItemID, &change.PurchasedQuantity, &before, &after); err != nil {
			rows.Close()
			return p, err
		}
		if err := json.Unmarshal([]byte(before), &change.Before); err != nil {
			rows.Close()
			return p, err
		}
		if err := json.Unmarshal([]byte(after), &change.After); err != nil {
			rows.Close()
			return p, err
		}
		p.Reconciliations = append(p.Reconciliations, change)
	}
	p.ReconciliationApplied = p.ReplacesID == nil && len(p.Reconciliations) > 0
	return p, errors.Join(rows.Err(), rows.Close())
}

func (s *Store) Purchase(ctx context.Context, owner Owner, id string) (Purchase, error) {
	if owner.id == "" {
		return Purchase{}, ErrOwnerRequired
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Purchase{}, err
	}
	defer tx.Rollback()
	p, err := readPurchase(ctx, tx, owner, id)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func (tx *Tx) Purchase(id string) (Purchase, error) {
	return readPurchase(tx.ctx, tx.sql, tx.owner, id)
}

// Reorders compare variant facts, allowing harmless description/source edits.
// Return the current product revision so the list mutation can pin it too.
func (s *Store) ReorderableLines(ctx context.Context, owner Owner, purchaseIDs []string) (map[string]int64, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	if len(purchaseIDs) == 0 {
		return map[string]int64{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(purchaseIDs)), ",")
	args := []any{owner.id}
	for _, id := range purchaseIDs {
		args = append(args, id)
	}
	rows, err := s.read.QueryContext(ctx, `SELECT l.id,p.revision,l.brand_snapshot,p.brand,l.flavor_snapshot,p.flavor,
		l.package_count_snapshot,p.package_count,l.package_size_snapshot,p.package_size,l.package_unit_snapshot,p.package_unit
		FROM purchase_lines l
		JOIN products p ON p.owner_id=l.owner_id AND p.id=l.product_id
		JOIN purchases h ON h.owner_id=l.owner_id AND h.id=l.purchase_id
		WHERE l.owner_id=? AND h.status='confirmed' AND l.quantity_unit='pack'
		AND p.archived=0
		AND l.purchase_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int64{}
	for rows.Next() {
		var id string
		var revision int64
		var oldBrand, brand, oldFlavor, flavor string
		var oldCount, count *int64
		var oldSize, size, oldUnit, unit *string
		if err := rows.Scan(&id, &revision, &oldBrand, &brand, &oldFlavor, &flavor, &oldCount, &count, &oldSize, &size, &oldUnit, &unit); err != nil {
			return nil, err
		}
		countsEqual := oldCount == nil && count == nil || oldCount != nil && count != nil && *oldCount == *count
		sizesEqual := sameString(oldSize, size)
		if oldSize != nil && size != nil {
			oldMicros, oldErr := decimalMicros(*oldSize)
			micros, err := decimalMicros(*size)
			sizesEqual = oldErr == nil && err == nil && oldMicros == micros
		}
		if oldBrand == brand && oldFlavor == flavor && countsEqual && sizesEqual && sameString(oldUnit, unit) {
			result[id] = revision
		}
	}
	return result, rows.Err()
}

func (s *Store) SearchPurchases(ctx context.Context, owner Owner, f PurchaseFilter) ([]Purchase, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT p.id FROM purchases p WHERE p.owner_id=?
		AND (?=0 OR p.purchased_at>=?) AND (?=0 OR p.purchased_at<?)
		AND (?='' OR lower(p.retailer)=lower(?)) AND (?='' OR p.source_reference=?)
		AND (?='' OR p.status=?) AND (?='' OR EXISTS(SELECT 1 FROM purchase_lines l WHERE l.owner_id=p.owner_id AND l.purchase_id=p.id AND l.product_id=?))
		AND (?='' OR coalesce(p.purchased_at,0)<? OR (coalesce(p.purchased_at,0)=? AND p.id<?))
		ORDER BY coalesce(p.purchased_at,0) DESC,p.id DESC LIMIT ?`, owner.id,
		f.From, f.From, f.To, f.To, f.Retailer, f.Retailer, f.SourceReference, f.SourceReference, f.Status, f.Status,
		f.ProductID, f.ProductID, f.BeforeID, f.BeforeTime, f.BeforeTime, f.BeforeID, f.Limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	result := make([]Purchase, 0, len(ids))
	for _, id := range ids {
		p, err := readPurchase(ctx, tx, owner, id)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, tx.Commit()
}

func sameString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (tx *Tx) SavePurchase(draft PurchaseDraft, expected int64) (Purchase, error) {
	var before *Purchase
	if expected > 0 {
		old, err := readPurchase(tx.ctx, tx.sql, tx.owner, draft.ID)
		if err != nil {
			return Purchase{}, err
		}
		if old.Status != "draft" || old.Revision != expected || !sameString(old.ReplacesID, draft.ReplacesID) {
			return Purchase{}, ErrConflict
		}
		before = &old
	}
	var origin *string
	if draft.ReplacesID != nil {
		old, err := readPurchase(tx.ctx, tx.sql, tx.owner, *draft.ReplacesID)
		if err != nil {
			return Purchase{}, err
		}
		if old.Status != "confirmed" || !strings.EqualFold(old.Retailer, draft.Retailer) || old.SourceKind != draft.SourceKind || old.SourceReference == nil || *old.SourceReference != draft.SourceReference {
			return Purchase{}, ErrConflict
		}
		origin = new(old.ID)
		if old.OriginalReconciliationPurchase != nil {
			origin = old.OriginalReconciliationPurchase
		}
		var anotherDraft bool
		if err := tx.sql.QueryRowContext(tx.ctx, `SELECT EXISTS(SELECT 1 FROM purchases WHERE owner_id=? AND replaces_id=? AND status='draft' AND id<>?)`, tx.owner.id, old.ID, draft.ID).Scan(&anotherDraft); err != nil {
			return Purchase{}, err
		}
		if anotherDraft {
			return Purchase{}, ErrConflict
		}
	}
	claimID := draft.ID
	if origin != nil {
		claimID = *origin
	}
	var existingClaim string
	err := tx.sql.QueryRowContext(tx.ctx, `SELECT purchase_id FROM purchase_sources WHERE owner_id=? AND retailer=? COLLATE NOCASE AND source_kind=? AND source_reference=?`, tx.owner.id, draft.Retailer, draft.SourceKind, draft.SourceReference).Scan(&existingClaim)
	if err == nil && existingClaim != claimID {
		return Purchase{}, ErrConflict
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Purchase{}, err
	}
	if expected == 0 {
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO purchases(owner_id,id,retailer,source_kind,created_at,updated_at) VALUES (?,?,?,?,?,?)`, tx.owner.id, draft.ID, draft.Retailer, draft.SourceKind, tx.now, tx.now); err != nil {
			return Purchase{}, err
		}
	}
	if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE purchases SET retailer=?,store_location=?,purchased_at=?,currency=?,source_kind=?,source_reference=?,source_evidence=?,source_url=?,source_observed_at=?,replaces_id=?,reconciliation_origin_id=?,revision=?,updated_at=? WHERE owner_id=? AND id=?`,
		draft.Retailer, draft.StoreLocation, draft.PurchasedAt, draft.Currency, draft.SourceKind, draft.SourceReference, draft.SourceEvidence, draft.SourceURL, draft.SourceObservedAt, draft.ReplacesID, origin, expected+1, tx.now, tx.owner.id, draft.ID); err != nil {
		return Purchase{}, err
	}
	if origin == nil {
		if _, err := tx.sql.ExecContext(tx.ctx, `DELETE FROM purchase_sources WHERE owner_id=? AND purchase_id=?`, tx.owner.id, draft.ID); err != nil {
			return Purchase{}, err
		}
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO purchase_sources(owner_id,retailer,source_kind,source_reference,purchase_id) VALUES (?,?,?,?,?)`, tx.owner.id, draft.Retailer, draft.SourceKind, draft.SourceReference, draft.ID); err != nil {
			return Purchase{}, err
		}
	}
	if err := tx.writePurchaseLines(draft.ID, draft.Lines); err != nil {
		return Purchase{}, err
	}
	saved, err := readPurchase(tx.ctx, tx.sql, tx.owner, draft.ID)
	if err != nil {
		return Purchase{}, err
	}
	return saved, tx.purchaseChange(before, saved)
}

func (tx *Tx) writePurchaseLines(id string, lines []PurchaseLineDraft) error {
	if _, err := tx.sql.ExecContext(tx.ctx, `DELETE FROM purchase_lines WHERE owner_id=? AND purchase_id=?`, tx.owner.id, id); err != nil {
		return err
	}
	for i, line := range lines {
		product, err := tx.FullProduct(line.ProductID)
		if err != nil {
			return err
		}
		if product.Revision != line.ExpectedProductRevision {
			return ErrConflict
		}
		sources, err := json.Marshal(product.Sources)
		if err != nil {
			return err
		}
		var linkJSON *string
		var itemID *string
		if line.ListLink != nil {
			link, err := json.Marshal(line.ListLink)
			if err != nil {
				return err
			}
			linkJSON, itemID = new(string(link)), new(line.ListLink.ItemID)
			item, err := tx.item(line.ListLink.ListID, line.ListLink.ItemID)
			if err != nil {
				return err
			}
			if item.ProductID != line.ProductID {
				return ErrConflict
			}
		}
		_, err = tx.sql.ExecContext(tx.ctx, `INSERT INTO purchase_lines(owner_id,id,purchase_id,product_id,source_list_item_id,name_snapshot,brand_snapshot,flavor_snapshot,category_snapshot,package_count_snapshot,package_size_snapshot,package_unit_snapshot,quantity,quantity_unit,unit_price_minor,line_total_minor,discount_minor,tax_minor,tax_treatment,source_reference,line_number,product_revision_snapshot,sources_snapshot_json,list_link_json)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, tx.owner.id, rand.Text(), id, line.ProductID, itemID, product.Name, product.Brand, product.Flavor, product.Category, product.PackageCount, product.PackageSize, product.PackageUnit, line.Quantity, line.QuantityUnit, line.UnitPriceMinor, line.LineTotalMinor, line.DiscountMinor, line.TaxMinor, line.TaxTreatment, line.SourceReference, i, product.Revision, string(sources), linkJSON)
		if err != nil {
			return err
		}
	}
	return nil
}

func purchaseAudit(p *Purchase) *PurchaseAudit {
	if p == nil {
		return nil
	}
	audit := &PurchaseAudit{
		ID:                             p.ID,
		Retailer:                       p.Retailer,
		PurchasedAt:                    p.PurchasedAt,
		Currency:                       p.Currency,
		Status:                         p.Status,
		SourceKind:                     p.SourceKind,
		SourceReference:                p.SourceReference,
		ReplacesID:                     p.ReplacesID,
		SupersededByID:                 p.SupersededByID,
		VoidReason:                     p.VoidReason,
		Revision:                       p.Revision,
		LineCount:                      len(p.Lines),
		ReconciliationApplied:          p.ReconciliationApplied,
		OriginalReconciliationPurchase: p.OriginalReconciliationPurchase,
		ReconciliationCount:            len(p.Reconciliations),
	}
	if p.Status != "draft" {
		return audit
	}
	// A later draft edit replaces its rows. Preserve the submitted inputs here;
	// immutable purchase states can be recovered from the compact references above.
	draft := &PurchaseDraft{
		ID:             p.ID,
		Retailer:       p.Retailer,
		StoreLocation:  p.StoreLocation,
		SourceKind:     p.SourceKind,
		SourceEvidence: p.SourceEvidence,
		SourceURL:      p.SourceURL,
		ReplacesID:     p.ReplacesID,
		Lines:          make([]PurchaseLineDraft, len(p.Lines)),
	}
	if p.PurchasedAt != nil {
		draft.PurchasedAt = *p.PurchasedAt
	}
	if p.Currency != nil {
		draft.Currency = *p.Currency
	}
	if p.SourceReference != nil {
		draft.SourceReference = *p.SourceReference
	}
	if p.SourceObservedAt != nil {
		draft.SourceObservedAt = *p.SourceObservedAt
	}
	for i, line := range p.Lines {
		draft.Lines[i] = line.PurchaseLineDraft
	}
	audit.Draft = draft
	return audit
}

func (tx *Tx) purchaseChange(before *Purchase, after Purchase) error {
	return tx.RecordChange("purchase", after.ID, purchaseAudit(before), purchaseAudit(&after))
}

func (tx *Tx) ConfirmPurchase(id string, expected, expectedReplaced int64) (Purchase, error) {
	p, err := readPurchase(tx.ctx, tx.sql, tx.owner, id)
	if err != nil {
		return p, err
	}
	if p.Status != "draft" || p.Revision != expected {
		return p, ErrConflict
	}
	before := p
	if p.ReplacesID != nil {
		old, err := readPurchase(tx.ctx, tx.sql, tx.owner, *p.ReplacesID)
		if err != nil {
			return p, err
		}
		if old.Status != "confirmed" || old.Revision != expectedReplaced {
			return p, ErrConflict
		}
		oldBefore := old
		if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE purchases SET status='superseded',superseded_by_id=?,revision=revision+1,updated_at=? WHERE owner_id=? AND id=?`, id, tx.now, tx.owner.id, old.ID); err != nil {
			return p, err
		}
		old.Status, old.SupersededByID, old.Revision = "superseded", new(id), old.Revision+1
		if err := tx.purchaseChange(&oldBefore, old); err != nil {
			return p, err
		}
	} else if err := tx.reconcilePurchase(p); err != nil {
		return p, err
	}
	// Pin the catalog revision supplied with the receipt. Capture its facts inside
	// this transaction; any intervening catalog edit aborts all reconciliation.
	drafts := make([]PurchaseLineDraft, len(p.Lines))
	for i, line := range p.Lines {
		drafts[i] = line.PurchaseLineDraft
	}
	if err := tx.writePurchaseLines(id, drafts); err != nil {
		return p, err
	}
	if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE purchases SET status='confirmed',revision=revision+1,updated_at=? WHERE owner_id=? AND id=?`, tx.now, tx.owner.id, id); err != nil {
		return p, err
	}
	confirmed, err := readPurchase(tx.ctx, tx.sql, tx.owner, id)
	if err != nil {
		return p, err
	}
	return confirmed, tx.purchaseChange(&before, confirmed)
}

func (tx *Tx) VoidPurchase(id string, expected int64, reason string) (Purchase, error) {
	p, err := readPurchase(tx.ctx, tx.sql, tx.owner, id)
	if err != nil {
		return p, err
	}
	if p.Status != "confirmed" || p.Revision != expected {
		return p, ErrConflict
	}
	before := p
	if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE purchases SET status='void',void_reason=?,revision=revision+1,updated_at=? WHERE owner_id=? AND id=?`, reason, tx.now, tx.owner.id, id); err != nil {
		return p, err
	}
	p.Status, p.VoidReason, p.Revision = "void", new(reason), p.Revision+1
	return p, tx.purchaseChange(&before, p)
}

func (tx *Tx) reconcilePurchase(p Purchase) error {
	listRevisions := map[string]int64{}
	for _, line := range p.Lines {
		if line.ListLink == nil {
			continue
		}
		link := line.ListLink
		list, err := readList(tx.ctx, tx.sql, tx.owner, link.ListID)
		if err != nil {
			return err
		}
		if list.Revision != link.ExpectedListRevision || list.Retailer != nil && !strings.EqualFold(*list.Retailer, p.Retailer) {
			return ErrConflict
		}
		item, err := tx.item(link.ListID, link.ItemID)
		if err != nil {
			return err
		}
		if item.ProductID != line.ProductID || item.Revision != link.ExpectedItemRevision || item.Retailer != nil && !strings.EqualFold(*item.Retailer, p.Retailer) {
			return ErrConflict
		}
		remaining, err := SubtractDecimal(item.Quantity, line.Quantity)
		if err != nil {
			return ErrConflict
		}
		before := item
		if remaining == "0" {
			if item.RemovedAt == nil {
				item.RemovedAt = new(tx.now)
			}
		} else {
			// Preserve checked/removed evidence. A partially bought active item
			// retains its remainder; an explicitly removed item stays removed.
			item.Quantity = remaining
		}
		item.Revision++
		if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE list_items SET quantity=?,removed_at=?,revision=?,updated_at=?,updated_by=? WHERE owner_id=? AND list_id=? AND id=?`, item.Quantity, item.RemovedAt, item.Revision, tx.now, tx.identity.Subject, tx.owner.id, link.ListID, link.ItemID); err != nil {
			return err
		}
		beforeJSON, err := json.Marshal(before)
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO purchase_reconciliations(owner_id,purchase_id,list_id,item_id,purchased_quantity,before_json,after_json) VALUES (?,?,?,?,?,?,?)`, tx.owner.id, p.ID, link.ListID, link.ItemID, line.Quantity, string(beforeJSON), string(afterJSON)); err != nil {
			return err
		}
		if err := tx.RecordChange("list_item", item.ID, before, item); err != nil {
			return err
		}
		listRevisions[list.ID] = list.Revision
	}
	for listID, revision := range listRevisions {
		if _, err := tx.sql.ExecContext(tx.ctx, `UPDATE shopping_lists SET revision=revision+1,updated_at=? WHERE owner_id=? AND id=?`, tx.now, tx.owner.id, listID); err != nil {
			return err
		}
		if err := tx.RecordChange("list", listID, revision, revision+1); err != nil {
			return err
		}
	}
	return nil
}
