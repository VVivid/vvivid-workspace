package shopping

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"agentshoppinglist/internal/store/sqlite"
)

func TestPurchaseOutcomeConservativeEncodedBound(t *testing.T) {
	// Exercise every bounded string at once, including 128-byte identifiers.
	// Runtime-generated IDs are shorter. '&' expands sixfold in JSON, so this
	// also bounds simultaneous receipt, line and reconciliation escaping.
	id := strings.Repeat("&", 128)
	purchase := sqlite.Purchase{
		ID:                             id,
		Retailer:                       strings.Repeat("&", 128),
		StoreLocation:                  new(strings.Repeat("&", 200)),
		PurchasedAt:                    new(int64(math.MaxInt64)),
		Currency:                       new("USD"),
		Status:                         "superseded",
		SourceKind:                     "user_confirmation",
		SourceReference:                new(strings.Repeat("&", 200)),
		SourceEvidence:                 strings.Repeat("&", 2000),
		SourceURL:                      new("https://example.com/?" + strings.Repeat("&", 2028)),
		SourceObservedAt:               new(int64(math.MaxInt64)),
		ReplacesID:                     new(id),
		SupersededByID:                 new(id),
		VoidReason:                     new(strings.Repeat("&", 1000)),
		Revision:                       math.MaxInt64,
		OriginalReconciliationPurchase: new(id),
		ReconciliationApplied:          true,
		Lines:                          make([]sqlite.PurchaseLine, 50),
		Reconciliations:                make([]sqlite.PurchaseReconciliation, 50),
	}
	for i := range 50 {
		itemID := strings.Repeat("&", 125) + fmt.Sprintf("%03d", i)
		line := sqlite.PurchaseLine{
			PurchaseLineDraft: sqlite.PurchaseLineDraft{
				ProductID:               id,
				ExpectedProductRevision: math.MaxInt64,
				Quantity:                "999999999.999999",
				QuantityUnit:            "us_fl_oz",
				UnitPriceMinor:          new(int64(999_999_999)),
				LineTotalMinor:          new(int64(999_999_999)),
				DiscountMinor:           new(int64(999_999_999)),
				TaxMinor:                new(int64(999_999_999)),
				TaxTreatment:            "excluded",
				SourceReference:         new(strings.Repeat("&", 200)),
				ListLink: &sqlite.PurchaseListLink{
					ListID:               id,
					ItemID:               itemID,
					ExpectedListRevision: math.MaxInt64,
					ExpectedItemRevision: math.MaxInt64,
				},
			},
			ID:              id,
			Name:            strings.Repeat("&", 300),
			Brand:           strings.Repeat("&", 150),
			Flavor:          strings.Repeat("&", 150),
			Category:        "produce",
			PackageCount:    new(int64(10000)),
			PackageSize:     new("999999999.999999"),
			PackageUnit:     new(strings.Repeat("&", 32)),
			ProductRevision: new(int64(math.MaxInt64)),
		}
		purchase.Lines[i] = line
		item := sqlite.ListItem{
			ID:        itemID,
			ListID:    id,
			ProductID: id,
			Quantity:  "999999999.999999",
			Retailer:  new(strings.Repeat("&", 128)),
			Revision:  math.MaxInt64,
			RemovedAt: new(int64(math.MaxInt64)),
		}
		purchase.Reconciliations[i] = sqlite.PurchaseReconciliation{
			ListID:            id,
			ItemID:            itemID,
			PurchasedQuantity: "999999999.999999",
			Before:            item,
			After:             item,
		}
	}
	raw, err := json.Marshal(purchaseOutcome(purchase))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 1<<20 {
		t.Fatalf("bounded purchase outcome exceeds the persisted result cap: %d bytes", len(raw))
	}
	t.Logf("conservative escaped purchase outcome: %d JSON bytes", len(raw))
}
