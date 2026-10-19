package sqlite

// PurchaseDraft contains evidence supplied by an agent. Product facts are read
// from this owner's catalog; callers cannot supply historical snapshots.
type PurchaseDraft struct {
	ID               string              `json:"id,omitempty"`
	Retailer         string              `json:"retailer"`
	StoreLocation    *string             `json:"store_location"`
	PurchasedAt      int64               `json:"purchased_at"`
	Currency         string              `json:"currency"`
	SourceKind       string              `json:"source_kind"`
	SourceReference  string              `json:"source_reference"`
	SourceEvidence   string              `json:"source_evidence"`
	SourceURL        *string             `json:"source_url"`
	SourceObservedAt int64               `json:"source_observed_at"`
	ReplacesID       *string             `json:"replaces_id"`
	Lines            []PurchaseLineDraft `json:"lines"`
}

type PurchaseListLink struct {
	ListID               string `json:"list_id"`
	ItemID               string `json:"item_id"`
	ExpectedListRevision int64  `json:"expected_list_revision"`
	ExpectedItemRevision int64  `json:"expected_item_revision"`
}

type PurchaseLineDraft struct {
	ProductID               string            `json:"product_id"`
	ExpectedProductRevision int64             `json:"expected_product_revision"`
	Quantity                string            `json:"quantity"`
	QuantityUnit            string            `json:"quantity_unit"`
	UnitPriceMinor          *int64            `json:"unit_price_minor"`
	LineTotalMinor          *int64            `json:"line_total_minor"`
	DiscountMinor           *int64            `json:"discount_minor"`
	TaxMinor                *int64            `json:"tax_minor"`
	TaxTreatment            string            `json:"tax_treatment"`
	SourceReference         *string           `json:"source_reference"`
	ListLink                *PurchaseListLink `json:"list_link"`
}

type PurchaseLine struct {
	PurchaseLineDraft
	ID              string        `json:"id"`
	Name            string        `json:"name_snapshot"`
	Brand           string        `json:"brand_snapshot"`
	Flavor          string        `json:"flavor_snapshot"`
	Category        string        `json:"category_snapshot"`
	PackageCount    *int64        `json:"package_count_snapshot"`
	PackageSize     *string       `json:"package_size_snapshot"`
	PackageUnit     *string       `json:"package_unit_snapshot"`
	ProductRevision *int64        `json:"product_revision_snapshot"`
	Sources         []FieldSource `json:"sources_snapshot"`
}

type PurchaseReconciliation struct {
	ListID            string   `json:"list_id"`
	ItemID            string   `json:"item_id"`
	PurchasedQuantity string   `json:"purchased_quantity"`
	Before            ListItem `json:"before"`
	After             ListItem `json:"after"`
}

type Purchase struct {
	ID                             string                   `json:"id"`
	Retailer                       string                   `json:"retailer"`
	StoreLocation                  *string                  `json:"store_location"`
	PurchasedAt                    *int64                   `json:"purchased_at"`
	Currency                       *string                  `json:"currency"`
	Status                         string                   `json:"status"`
	SourceKind                     string                   `json:"source_kind"`
	SourceReference                *string                  `json:"source_reference"`
	SourceEvidence                 string                   `json:"source_evidence"`
	SourceURL                      *string                  `json:"source_url"`
	SourceObservedAt               *int64                   `json:"source_observed_at"`
	ReplacesID                     *string                  `json:"replaces_id"`
	SupersededByID                 *string                  `json:"superseded_by_id"`
	VoidReason                     *string                  `json:"void_reason"`
	Revision                       int64                    `json:"revision"`
	ReconciliationApplied          bool                     `json:"reconciliation_applied"`
	OriginalReconciliationPurchase *string                  `json:"original_reconciliation_purchase_id"`
	Lines                          []PurchaseLine           `json:"lines"`
	Reconciliations                []PurchaseReconciliation `json:"reconciliations"`
}

// PurchaseAudit identifies an immutable purchase snapshot and its state change.
// Drafts are still editable, so retain their complete submitted inputs here.
// Confirmed line/source snapshots and reconciliation before/after evidence stay
// in the owner-qualified purchase records instead of repeating in every event.
type PurchaseAudit struct {
	ID                             string         `json:"id"`
	Retailer                       string         `json:"retailer"`
	PurchasedAt                    *int64         `json:"purchased_at"`
	Currency                       *string        `json:"currency"`
	Status                         string         `json:"status"`
	SourceKind                     string         `json:"source_kind"`
	SourceReference                *string        `json:"source_reference"`
	ReplacesID                     *string        `json:"replaces_id"`
	SupersededByID                 *string        `json:"superseded_by_id"`
	VoidReason                     *string        `json:"void_reason"`
	Revision                       int64          `json:"revision"`
	LineCount                      int            `json:"line_count"`
	ReconciliationApplied          bool           `json:"reconciliation_applied"`
	OriginalReconciliationPurchase *string        `json:"original_reconciliation_purchase_id"`
	ReconciliationCount            int            `json:"reconciliation_count"`
	Draft                          *PurchaseDraft `json:"draft,omitzero"`
}

type PurchaseFilter struct {
	From, To                             int64
	Retailer, ProductID, SourceReference string
	Status                               string
	BeforeTime                           int64
	BeforeID                             string
	Limit                                int
}
