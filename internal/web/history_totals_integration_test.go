package web_test

import (
	"strings"
	"testing"

	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
)

func TestHistoryPurchaseSummaryTotals(t *testing.T) {
	f, service, principal, product, _ := setup(t)
	handler := newHandler(t, f, service)
	cookie := f.Cookie(t, "alice")
	for _, tc := range []struct {
		name, want       string
		lineAmounts      []string
		tax              string
		firstLabelSuffix string
		draftOnly        bool
		change           func(*sqlite.PurchaseDraft)
	}{
		{name: "discount-and-tax", want: "$11.33", lineAmounts: []string{"$4.99", "$5.99"}, tax: "$0.35"},
		{name: "known-zero", want: "$0.00", lineAmounts: []string{"$0.00"}, tax: "$0.00", change: func(p *sqlite.PurchaseDraft) {
			p.Lines = p.Lines[:1]
			p.Lines[0].UnitPriceMinor, p.Lines[0].LineTotalMinor, p.Lines[0].DiscountMinor, p.Lines[0].TaxMinor = new(int64(0)), new(int64(0)), new(int64(0)), new(int64(0))
		}},
		{name: "missing-merchandise", want: "—", lineAmounts: []string{"$4.99", "—"}, tax: "$0.35", change: func(p *sqlite.PurchaseDraft) { p.Lines[1].LineTotalMinor = nil }},
		{name: "missing-tax", want: "—", lineAmounts: []string{"$4.99", "$5.99"}, tax: "—", change: func(p *sqlite.PurchaseDraft) {
			p.Lines[1].TaxMinor, p.Lines[1].TaxTreatment = nil, "unknown"
		}},
		{name: "unconfirmed", want: "—", lineAmounts: []string{"$4.99", "$5.99"}, tax: "$0.35", draftOnly: true},
		{name: "two-packs", want: "$11.33", lineAmounts: []string{"$4.99", "$5.99"}, tax: "$0.35", firstLabelSuffix: " × 2", change: func(p *sqlite.PurchaseDraft) {
			p.Lines[0].Quantity, p.Lines[0].UnitPriceMinor = "2", nil
		}},
		{name: "measured", want: "$11.33", lineAmounts: []string{"$4.99", "$5.99"}, tax: "$0.35", firstLabelSuffix: " × 500 g", change: func(p *sqlite.PurchaseDraft) {
			p.Lines[0].Quantity, p.Lines[0].QuantityUnit, p.Lines[0].UnitPriceMinor = "500", "g", nil
		}},
		{name: "exact-measured", want: "$11.33", lineAmounts: []string{"$4.99", "$5.99"}, tax: "$0.35", firstLabelSuffix: " × 0.123456 kg", change: func(p *sqlite.PurchaseDraft) {
			p.Lines[0].Quantity, p.Lines[0].QuantityUnit, p.Lines[0].UnitPriceMinor = "0.123456", "kg", nil
		}},
		{name: "summed-tax", want: "$11.58", lineAmounts: []string{"$4.99", "$5.99"}, tax: "$0.60", change: func(p *sqlite.PurchaseDraft) { p.Lines[1].TaxMinor = new(int64(25)) }},
		{name: "non-USD", want: "EUR 11.33", lineAmounts: []string{"EUR 4.99", "EUR 5.99"}, tax: "EUR 0.35", change: func(p *sqlite.PurchaseDraft) { p.Currency = "EUR" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft := purchaseDraft(product.ID, "history-total-"+tc.name)
			draft.Lines = append(draft.Lines, draft.Lines[0])
			draft.Lines[0].LineTotalMinor, draft.Lines[0].DiscountMinor, draft.Lines[0].TaxMinor = new(int64(499)), new(int64(100)), new(int64(35))
			if tc.change != nil {
				tc.change(&draft)
			}
			var purchaseID string
			if tc.draftOnly {
				saved, err := service.SavePurchase(t.Context(), principal, shopping.SavePurchaseInput{WriteMeta: meta(tc.name), Purchase: draft})
				if err != nil {
					t.Fatal(err)
				}
				purchaseID = saved.Record.ID
			} else {
				purchaseID = saveAndConfirm(t, service, principal, tc.name, draft).ID
			}

			paths := []string{"/history?purchase=" + purchaseID}
			if !tc.draftOnly {
				paths = append(paths, "/history")
			}
			for _, path := range paths {
				response := apiRequest(handler, cookie, "", "GET", path, "", nil)
				if response.Code != 200 {
					t.Fatalf("%s: %d %s", path, response.Code, response.Body)
				}
				_, record, ok := strings.Cut(response.Body.String(), `<details class="purchase-record" data-purchase-id="`+purchaseID+`">`)
				if !ok {
					t.Fatalf("%s: missing collapsed purchase", path)
				}
				record, _, ok = strings.Cut(record, "</details>")
				if !ok {
					t.Fatal("missing purchase details boundary")
				}
				_, summary, ok := strings.Cut(record, "<summary>")
				if !ok {
					t.Fatal("missing native summary control")
				}
				summary, _, ok = strings.Cut(summary, "</summary>")
				if !ok || !strings.Contains(summary, `<span class="purchase-heading">`) || !strings.Contains(summary, "<strong>Walmart</strong>") {
					t.Fatalf("missing retailer heading: %s", summary)
				}
				_, total, ok := strings.Cut(summary, `class="purchase-total"`)
				if !ok {
					t.Fatalf("missing heading amount: %s", summary)
				}
				_, total, _ = strings.Cut(total, ">")
				total, _, _ = strings.Cut(total, "</strong>")
				if total != tc.want {
					t.Fatalf("%s heading amount = %q, want %q", path, total, tc.want)
				}
				if strings.Index(summary, `class="purchase-date"`) < strings.Index(summary, `class="purchase-total"`) {
					t.Fatalf("date must follow retailer and amount: %s", summary)
				}
				if tc.draftOnly && !strings.Contains(summary, "<small>draft</small>") {
					t.Fatal("draft status label missing")
				}
				compact, details, ok := strings.Cut(record, `<details class="receipt-details">`)
				if !ok || !strings.HasPrefix(details, "\n    <summary>Receipt details</summary>") {
					t.Fatal("missing collapsed secondary receipt disclosure")
				}
				if strings.Contains(compact, "Buy again") || strings.Contains(compact, "merchandise") || strings.Contains(compact, "category-icon") || strings.Contains(compact, "pack bought") {
					t.Fatal("compact receipt contains detailed-row clutter")
				}
				_, receipt, ok := strings.Cut(compact, `<ul class="receipt" aria-label="Purchased items">`)
				if !ok || strings.Count(receipt, `<li`) != len(draft.Lines)+1 {
					t.Fatal("compact receipt must contain each purchased line and one tax row")
				}
				for i, amount := range tc.lineAmounts {
					_, row, _ := strings.Cut(receipt, `<li>`)
					row, receipt, _ = strings.Cut(row, `</li>`)
					label := product.Name
					if i == 0 {
						label += tc.firstLabelSuffix
					}
					if !strings.Contains(row, `<span>`+label+`</span>`) || !strings.HasSuffix(row, amount+`</span>`) {
						t.Fatalf("compact line %d = %s, want saved name and %s", i, row, amount)
					}
					if amount == "—" && !strings.Contains(row, `aria-label="Amount unavailable"`) {
						t.Fatal("unknown line amount lacks accessible text")
					}
				}
				if !strings.Contains(receipt, `<li class="receipt-tax"><span>Tax</span>`) || !strings.Contains(receipt, `>`+tc.tax+`</span>`) {
					t.Fatalf("compact tax = %s, want %s", receipt, tc.tax)
				}
				if tc.tax == "—" && !strings.Contains(receipt, `aria-label="Tax unavailable"`) {
					t.Fatal("unknown tax lacks accessible text")
				}
				if !strings.Contains(record, product.Name) || !strings.Contains(record, draft.SourceEvidence) {
					t.Fatal("expanded details lost item or source")
				}
				if !tc.draftOnly && !strings.Contains(record, "Buy again") {
					t.Fatal("confirmed purchase lost Buy again")
				}
				if tc.draftOnly && strings.Contains(record, "Buy again") {
					t.Fatal("draft purchase offered Buy again")
				}
				if tc.name == "discount-and-tax" && (!strings.Contains(record, "USD 4.99 merchandise") || !strings.Contains(record, "Allocated discount: USD 1.00") || !strings.Contains(record, "Tax: USD 0.35")) {
					t.Fatal("expanded receipt amounts changed")
				}
			}
		})
	}
}

func TestHistoryCompactReceiptKeepsSnapshotAndOwnership(t *testing.T) {
	f, service, principal, product, _ := setup(t)
	draft := purchaseDraft(product.ID, "compact-snapshot")
	purchase := saveAndConfirm(t, service, principal, "compact-snapshot", draft)
	savedName := product.Name
	product.Name = "Renamed catalog product"
	if _, err := service.SaveProduct(t.Context(), principal, shopping.SaveProductInput{WriteMeta: meta("rename-after-purchase"), Product: product, ExpectedRevision: product.Revision}); err != nil {
		t.Fatal(err)
	}
	handler := newHandler(t, f, service)
	owner := f.Cookie(t, "alice")
	for _, path := range []string{"/history", "/history?purchase=" + purchase.ID} {
		response := apiRequest(handler, owner, "", "GET", path, "", nil)
		compact, _, ok := strings.Cut(response.Body.String(), `<details class="receipt-details">`)
		if response.Code != 200 || !ok || !strings.Contains(compact, `<li><span>`+savedName+`</span>`) || strings.Contains(compact, product.Name) {
			t.Fatalf("%s did not retain the saved name: %s", path, response.Body)
		}
		foreign := apiRequest(handler, f.Cookie(t, "bob"), "", "GET", path, "", nil)
		if strings.Contains(foreign.Body.String(), savedName) || strings.Contains(foreign.Body.String(), draft.SourceEvidence) || strings.Contains(foreign.Body.String(), `class="receipt"`) {
			t.Fatalf("%s disclosed another owner's receipt", path)
		}
	}
}
