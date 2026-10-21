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
		name, want string
		draftOnly  bool
		change     func(*sqlite.PurchaseDraft)
	}{
		{name: "discount-and-tax", want: "$11.33"},
		{name: "known-zero", want: "$0.00", change: func(p *sqlite.PurchaseDraft) {
			p.Lines = p.Lines[:1]
			p.Lines[0].UnitPriceMinor, p.Lines[0].LineTotalMinor, p.Lines[0].DiscountMinor, p.Lines[0].TaxMinor = new(int64(0)), new(int64(0)), new(int64(0)), new(int64(0))
		}},
		{name: "missing-merchandise", want: "—", change: func(p *sqlite.PurchaseDraft) { p.Lines[1].LineTotalMinor = nil }},
		{name: "missing-tax", want: "—", change: func(p *sqlite.PurchaseDraft) {
			p.Lines[1].TaxMinor, p.Lines[1].TaxTreatment = nil, "unknown"
		}},
		{name: "unconfirmed", want: "—", draftOnly: true},
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
