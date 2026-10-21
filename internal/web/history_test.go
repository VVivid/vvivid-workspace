package web

import (
	"math"
	"testing"

	"agentshoppinglist/internal/store/sqlite"
)

func TestPurchaseTotalText(t *testing.T) {
	complete := func() sqlite.Purchase {
		return sqlite.Purchase{
			Status:   "confirmed",
			Currency: new("USD"),
			Lines: []sqlite.PurchaseLine{
				{PurchaseLineDraft: sqlite.PurchaseLineDraft{LineTotalMinor: new(int64(499)), DiscountMinor: new(int64(100)), TaxMinor: new(int64(35)), TaxTreatment: "excluded"}},
				{PurchaseLineDraft: sqlite.PurchaseLineDraft{LineTotalMinor: new(int64(599)), TaxMinor: new(int64(0)), TaxTreatment: "excluded"}},
			},
		}
	}
	for _, tc := range []struct {
		name, want string
		change     func(*sqlite.Purchase)
	}{
		{name: "net merchandise plus tax without subtracting discount again", want: "$11.33"},
		{name: "known zero", want: "$0.00", change: func(p *sqlite.Purchase) {
			p.Lines = p.Lines[:1]
			p.Lines[0].LineTotalMinor, p.Lines[0].TaxMinor = new(int64(0)), new(int64(0))
		}},
		{name: "zero decimal currency", want: "JPY 1133", change: func(p *sqlite.Purchase) { p.Currency = new("JPY") }},
		{name: "three decimal currency", want: "KWD 1.133", change: func(p *sqlite.Purchase) { p.Currency = new("KWD") }},
		{name: "draft", want: "—", change: func(p *sqlite.Purchase) { p.Status = "draft" }},
		{name: "void", want: "—", change: func(p *sqlite.Purchase) { p.Status = "void" }},
		{name: "superseded", want: "—", change: func(p *sqlite.Purchase) { p.Status = "superseded" }},
		{name: "missing currency", want: "—", change: func(p *sqlite.Purchase) { p.Currency = nil }},
		{name: "empty currency", want: "—", change: func(p *sqlite.Purchase) { p.Currency = new("") }},
		{name: "invalid currency", want: "—", change: func(p *sqlite.Purchase) { p.Currency = new("XYZ") }},
		{name: "no lines", want: "—", change: func(p *sqlite.Purchase) { p.Lines = nil }},
		{name: "no total inferred from unit price", want: "—", change: func(p *sqlite.Purchase) {
			p.Lines[0].LineTotalMinor, p.Lines[0].UnitPriceMinor = nil, new(int64(599))
		}},
		{name: "partial merchandise is not a total", want: "—", change: func(p *sqlite.Purchase) { p.Lines[1].LineTotalMinor = nil }},
		{name: "unknown tax is not zero", want: "—", change: func(p *sqlite.Purchase) {
			p.Lines[1].TaxMinor, p.Lines[1].TaxTreatment = nil, "unknown"
		}},
		{name: "missing tax with excluded treatment", want: "—", change: func(p *sqlite.Purchase) { p.Lines[1].TaxMinor = nil }},
		{name: "tax basis must be separate", want: "—", change: func(p *sqlite.Purchase) { p.Lines[0].TaxTreatment = "unknown" }},
		{name: "negative merchandise", want: "—", change: func(p *sqlite.Purchase) { p.Lines[0].LineTotalMinor = new(int64(-1)) }},
		{name: "negative tax", want: "—", change: func(p *sqlite.Purchase) { p.Lines[0].TaxMinor = new(int64(-1)) }},
		{name: "line and tax overflow", want: "—", change: func(p *sqlite.Purchase) { p.Lines[0].LineTotalMinor = new(int64(math.MaxInt64)) }},
		{name: "overflow across lines", want: "—", change: func(p *sqlite.Purchase) {
			p.Lines[0].LineTotalMinor, p.Lines[0].TaxMinor = new(int64(math.MaxInt64-1)), new(int64(0))
		}},
		{name: "maximum exact sum", want: "$92233720368547758.07", change: func(p *sqlite.Purchase) {
			p.Lines = p.Lines[:1]
			p.Lines[0].LineTotalMinor, p.Lines[0].TaxMinor = new(int64(math.MaxInt64-1)), new(int64(1))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			purchase := complete()
			if tc.change != nil {
				tc.change(&purchase)
			}
			if got := purchaseTotalText(purchase); got != tc.want {
				t.Fatalf("purchase total = %q, want %q", got, tc.want)
			}
		})
	}
}
