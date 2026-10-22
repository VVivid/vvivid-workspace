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

func TestReceiptLineLabel(t *testing.T) {
	for _, tc := range []struct {
		quantity, unit, want string
	}{
		{quantity: "1", unit: "pack", want: "Saved product"},
		{quantity: "2", unit: "pack", want: "Saved product × 2"},
		{quantity: "0.123456", unit: "pack", want: "Saved product × 0.123456"},
		{quantity: "1", unit: "each", want: "Saved product × 1 each"},
		{quantity: "500", unit: "g", want: "Saved product × 500 g"},
		{quantity: "0.123456", unit: "kg", want: "Saved product × 0.123456 kg"},
		{quantity: "1.000001", unit: "l", want: "Saved product × 1.000001 l"},
		{quantity: "12", unit: "us_fl_oz", want: "Saved product × 12 US fl oz"},
	} {
		t.Run(tc.quantity+" "+tc.unit, func(t *testing.T) {
			line := sqlite.PurchaseLine{Name: "Saved product", PurchaseLineDraft: sqlite.PurchaseLineDraft{Quantity: tc.quantity, QuantityUnit: tc.unit}}
			if got := receiptLineLabel(line); got != tc.want {
				t.Fatalf("receipt label = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReceiptMoneyText(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		amount     *int64
		currency   *string
	}{
		{name: "USD", amount: new(int64(499)), currency: new("USD"), want: "$4.99"},
		{name: "known zero", amount: new(int64(0)), currency: new("USD"), want: "$0.00"},
		{name: "unknown amount", currency: new("USD"), want: "—"},
		{name: "unknown currency", amount: new(int64(499)), want: "—"},
		{name: "invalid currency", amount: new(int64(499)), currency: new("XYZ"), want: "—"},
		{name: "negative amount", amount: new(int64(-1)), currency: new("USD"), want: "—"},
		{name: "EUR", amount: new(int64(499)), currency: new("EUR"), want: "EUR 4.99"},
		{name: "zero decimal currency", amount: new(int64(499)), currency: new("JPY"), want: "JPY 499"},
		{name: "three decimal currency", amount: new(int64(499)), currency: new("KWD"), want: "KWD 0.499"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := receiptMoneyText(tc.amount, tc.currency); got != tc.want {
				t.Fatalf("receipt money = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPurchaseTaxText(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*sqlite.Purchase)
	}{
		{name: "sum separate taxes only", want: "$0.60"},
		{name: "unknown merchandise does not hide known tax", want: "$0.60", change: func(p *sqlite.Purchase) { p.Lines[1].LineTotalMinor = nil }},
		{name: "known zero", want: "$0.00", change: func(p *sqlite.Purchase) {
			p.Lines[0].TaxMinor, p.Lines[1].TaxMinor = new(int64(0)), new(int64(0))
		}},
		{name: "missing tax", want: "—", change: func(p *sqlite.Purchase) { p.Lines[1].TaxMinor = nil }},
		{name: "unsupported tax basis", want: "—", change: func(p *sqlite.Purchase) { p.Lines[1].TaxTreatment = "included" }},
		{name: "negative tax", want: "—", change: func(p *sqlite.Purchase) { p.Lines[1].TaxMinor = new(int64(-1)) }},
		{name: "missing currency", want: "—", change: func(p *sqlite.Purchase) { p.Currency = nil }},
		{name: "invalid currency", want: "—", change: func(p *sqlite.Purchase) { p.Currency = new("XYZ") }},
		{name: "no lines", want: "—", change: func(p *sqlite.Purchase) { p.Lines = nil }},
		{name: "overflow", want: "—", change: func(p *sqlite.Purchase) { p.Lines[1].TaxMinor = new(int64(math.MaxInt64)) }},
		{name: "maximum exact tax", want: "$92233720368547758.07", change: func(p *sqlite.Purchase) { p.Lines[1].TaxMinor = new(int64(math.MaxInt64 - 35)) }},
		{name: "zero decimal currency", want: "JPY 60", change: func(p *sqlite.Purchase) { p.Currency = new("JPY") }},
		{name: "three decimal currency", want: "KWD 0.060", change: func(p *sqlite.Purchase) { p.Currency = new("KWD") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			purchase := sqlite.Purchase{
				Currency: new("USD"),
				Lines: []sqlite.PurchaseLine{
					{PurchaseLineDraft: sqlite.PurchaseLineDraft{LineTotalMinor: new(int64(499)), DiscountMinor: new(int64(100)), TaxMinor: new(int64(35)), TaxTreatment: "excluded"}},
					{PurchaseLineDraft: sqlite.PurchaseLineDraft{LineTotalMinor: new(int64(599)), TaxMinor: new(int64(25)), TaxTreatment: "excluded"}},
				},
			}
			if tc.change != nil {
				tc.change(&purchase)
			}
			if got := purchaseTaxText(purchase); got != tc.want {
				t.Fatalf("receipt tax = %q, want %q", got, tc.want)
			}
		})
	}
}
