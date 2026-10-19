package shopping

import (
	"context"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
)

// PurchaseOutcome is the authoritative result of one purchase mutation. Its
// receipt, line facts and reconciliation evidence describe that attempt even
// after later edits. Field-source arrays belong to GetPurchase's full record:
// repeating up to 600 long URLs here would exceed the durable replay budget.
type PurchaseOutcome struct {
	ID                             string                          `json:"id"`
	Retailer                       string                          `json:"retailer"`
	StoreLocation                  *string                         `json:"store_location"`
	PurchasedAt                    *int64                          `json:"purchased_at"`
	Currency                       *string                         `json:"currency"`
	Status                         string                          `json:"status"`
	SourceKind                     string                          `json:"source_kind"`
	SourceReference                *string                         `json:"source_reference"`
	SourceEvidence                 string                          `json:"source_evidence"`
	SourceURL                      *string                         `json:"source_url"`
	SourceObservedAt               *int64                          `json:"source_observed_at"`
	ReplacesID                     *string                         `json:"replaces_id"`
	SupersededByID                 *string                         `json:"superseded_by_id"`
	VoidReason                     *string                         `json:"void_reason"`
	Revision                       int64                           `json:"revision"`
	ReconciliationApplied          bool                            `json:"reconciliation_applied"`
	OriginalReconciliationPurchase *string                         `json:"original_reconciliation_purchase_id"`
	Lines                          []PurchaseLineOutcome           `json:"lines"`
	Reconciliations                []sqlite.PurchaseReconciliation `json:"reconciliations"`
}

// PurchaseLineOutcome retains the line's identity, price and package facts.
// Read sources_snapshot through get_purchase using the outcome's purchase ID.
type PurchaseLineOutcome struct {
	sqlite.PurchaseLineDraft
	ID              string  `json:"id"`
	Name            string  `json:"name_snapshot"`
	Brand           string  `json:"brand_snapshot"`
	Flavor          string  `json:"flavor_snapshot"`
	Category        string  `json:"category_snapshot"`
	PackageCount    *int64  `json:"package_count_snapshot"`
	PackageSize     *string `json:"package_size_snapshot"`
	PackageUnit     *string `json:"package_unit_snapshot"`
	ProductRevision *int64  `json:"product_revision_snapshot"`
}

func purchaseOutcome(purchase sqlite.Purchase) PurchaseOutcome {
	outcome := PurchaseOutcome{
		ID:                             purchase.ID,
		Retailer:                       purchase.Retailer,
		StoreLocation:                  purchase.StoreLocation,
		PurchasedAt:                    purchase.PurchasedAt,
		Currency:                       purchase.Currency,
		Status:                         purchase.Status,
		SourceKind:                     purchase.SourceKind,
		SourceReference:                purchase.SourceReference,
		SourceEvidence:                 purchase.SourceEvidence,
		SourceURL:                      purchase.SourceURL,
		SourceObservedAt:               purchase.SourceObservedAt,
		ReplacesID:                     purchase.ReplacesID,
		SupersededByID:                 purchase.SupersededByID,
		VoidReason:                     purchase.VoidReason,
		Revision:                       purchase.Revision,
		ReconciliationApplied:          purchase.ReconciliationApplied,
		OriginalReconciliationPurchase: purchase.OriginalReconciliationPurchase,
		Lines:                          make([]PurchaseLineOutcome, len(purchase.Lines)),
		Reconciliations:                purchase.Reconciliations,
	}
	for i, line := range purchase.Lines {
		outcome.Lines[i] = PurchaseLineOutcome{
			PurchaseLineDraft: line.PurchaseLineDraft,
			ID:                line.ID,
			Name:              line.Name,
			Brand:             line.Brand,
			Flavor:            line.Flavor,
			Category:          line.Category,
			PackageCount:      line.PackageCount,
			PackageSize:       line.PackageSize,
			PackageUnit:       line.PackageUnit,
			ProductRevision:   line.ProductRevision,
		}
	}
	return outcome
}

// writePurchase projects each successful mutation before write serializes its
// durable result. Replays bypass this callback and decode the saved outcome;
// historical full-Purchase JSON also decodes into the narrower outcome shape.
func writePurchase(s *Service, ctx context.Context, p identity.Principal, meta WriteMeta, operation string, input any, apply func(*sqlite.Tx) (sqlite.Purchase, error)) (WriteResult[PurchaseOutcome], error) {
	return write(s, ctx, p, meta, operation, input, func(tx *sqlite.Tx) (PurchaseOutcome, error) {
		purchase, err := apply(tx)
		if err != nil {
			return PurchaseOutcome{}, err
		}
		return purchaseOutcome(purchase), nil
	})
}
