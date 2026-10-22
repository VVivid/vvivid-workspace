package web

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
)

type purchaseView struct {
	Purchase     *sqlite.Purchase
	List         sqlite.ListView
	Timezone     string
	Reorderable  map[string]int64
	TotalText    string
	ReceiptLines []receiptLineView
	TaxText      string
}

type receiptLineView struct {
	Label      string
	AmountText string
}

func dictPurchase(purchase sqlite.Purchase, list sqlite.ListView, timezone string, reorderable map[string]int64) purchaseView {
	lines := make([]receiptLineView, 0, len(purchase.Lines))
	for _, line := range purchase.Lines {
		lines = append(lines, receiptLineView{
			Label:      receiptLineLabel(line),
			AmountText: receiptMoneyText(line.LineTotalMinor, purchase.Currency),
		})
	}
	return purchaseView{
		Purchase:     new(purchase),
		List:         list,
		Timezone:     timezone,
		Reorderable:  reorderable,
		TotalText:    purchaseTotalText(purchase),
		ReceiptLines: lines,
		TaxText:      purchaseTaxText(purchase),
	}
}

func receiptLineLabel(line sqlite.PurchaseLine) string {
	if line.QuantityUnit == "pack" && line.Quantity == "1" {
		return line.Name
	}
	// Preserve the stored decimal exactly; the Insights quantity formatter rounds.
	label := line.Name + " × " + line.Quantity
	if line.QuantityUnit != "pack" {
		unit := line.QuantityUnit
		if unit == "us_fl_oz" {
			unit = "US fl oz"
		}
		label += " " + unit
	}
	return label
}

func receiptMoneyText(amount *int64, currency *string) string {
	if amount == nil || *amount < 0 || currency == nil || !sqlite.ValidCurrency(*currency) {
		return "—"
	}
	text := moneyText(amount, *currency)
	if *currency == "USD" {
		return "$" + strings.TrimPrefix(text, "USD ")
	}
	return text
}

func purchaseTaxText(purchase sqlite.Purchase) string {
	if len(purchase.Lines) == 0 {
		return "—"
	}
	var tax int64
	for _, line := range purchase.Lines {
		if line.TaxTreatment != "excluded" || line.TaxMinor == nil || *line.TaxMinor < 0 || *line.TaxMinor > math.MaxInt64-tax {
			return "—"
		}
		tax += *line.TaxMinor
	}
	return receiptMoneyText(new(tax), purchase.Currency)
}

func purchaseTotalText(purchase sqlite.Purchase) string {
	if purchase.Status != "confirmed" || purchase.Currency == nil || !sqlite.ValidCurrency(*purchase.Currency) || len(purchase.Lines) == 0 {
		return "—"
	}
	var total int64
	for _, line := range purchase.Lines {
		if line.TaxTreatment != "excluded" {
			return "—"
		}
		// Line totals already include allocated discounts. Only separately
		// recorded tax is added; missing amounts cannot form a complete total.
		for _, amount := range []*int64{line.LineTotalMinor, line.TaxMinor} {
			if amount == nil || *amount < 0 || *amount > math.MaxInt64-total {
				return "—"
			}
			total += *amount
		}
	}
	return receiptMoneyText(new(total), purchase.Currency)
}

func canBuyAgain(line sqlite.PurchaseLine, list sqlite.ListView, retailer string) bool {
	if list.List == nil || list.List.Status != "active" || line.QuantityUnit != "pack" || line.ProductID == "" {
		return false
	}
	if list.List.Retailer != nil && !strings.EqualFold(*list.List.Retailer, retailer) {
		return false
	}
	for _, item := range list.Items {
		if item.ProductID == line.ProductID && item.Retailer != nil && !strings.EqualFold(*item.Retailer, retailer) {
			return false
		}
	}
	return true
}

func buyAgainRetailer(line sqlite.PurchaseLine, list sqlite.ListView, retailer string) string {
	for _, item := range list.Items {
		if item.ProductID == line.ProductID && item.Retailer == nil {
			return ""
		}
	}
	return retailer
}

func snapshotPack(line sqlite.PurchaseLine) string {
	return packText(sqlite.ProductSummary{PackageCount: line.PackageCount, PackageSize: line.PackageSize, PackageUnit: line.PackageUnit})
}

func currencyCode(code *string) string {
	if code == nil {
		return ""
	}
	return *code
}

func moneyText(amount *int64, currency string) string {
	if amount == nil || currency == "" {
		return "Unknown"
	}
	digits := 2
	switch currency {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		digits = 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		digits = 3
	}
	divisor := int64(1)
	for range digits {
		divisor *= 10
	}
	if digits == 0 {
		return fmt.Sprintf("%s %d", currency, *amount)
	}
	return fmt.Sprintf("%s %d.%0*d", currency, *amount/divisor, digits, *amount%divisor)
}

func sourceKindText(kind string) string {
	if kind == "receipt" {
		return "Receipt"
	}
	if kind == "user_confirmation" {
		return "Confirmed by you"
	}
	return "Source"
}

func categoryText(category string) string {
	return map[string]string{"produce": "Produce", "meat": "Meat", "drinks": "Drinks", "pantry": "Pantry", "dairy": "Dairy", "other": "Other"}[category]
}

func spendText(spend shopping.Spend) string {
	missing := fmt.Sprintf("%d prices missing", spend.MissingMerchandiseLines)
	if spend.MissingMerchandiseLines == 1 {
		missing = "1 price missing"
	}
	if spend.MerchandiseMinor == nil {
		return spend.Currency + " amount unknown · " + missing
	}
	text := moneyText(spend.MerchandiseMinor, spend.Currency)
	if spend.MissingMerchandiseLines > 0 {
		text += " known subtotal · " + missing
	}
	return text
}

func quantityText(raw string) string {
	value, ok := new(big.Rat).SetString(raw)
	if !ok {
		return "Unknown"
	}
	rounded := value.FloatString(2)
	displayed, _ := new(big.Rat).SetString(rounded)
	prefix := ""
	if displayed.Cmp(value) != 0 {
		prefix = "≈"
	}
	return prefix + strings.TrimRight(strings.TrimRight(rounded, "0"), ".")
}

func categoryMaximum(insights shopping.Insights, currency string) int64 {
	for _, spend := range insights.Spend {
		if spend.Currency == currency && spend.MerchandiseMinor != nil {
			return max(*spend.MerchandiseMinor, 1)
		}
	}
	return 1
}

func quantityMaximum(insights shopping.Insights, category, unit string) string {
	peak := new(big.Rat)
	for _, week := range insights.Weeks {
		for _, quantity := range week.Quantities {
			if quantity.Category == category && quantity.Unit == unit {
				value, ok := new(big.Rat).SetString(quantity.Quantity)
				if ok && value.Cmp(peak) > 0 {
					peak.Set(value)
				}
			}
		}
	}
	if peak.Sign() == 0 {
		return "1"
	}
	return peak.FloatString(18)
}

func purchaseDate(timestamp *int64, timezone string) string {
	if timestamp == nil {
		return "Date unknown"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "Date unavailable"
	}
	return time.UnixMilli(*timestamp).In(location).Format("Jan 2, 2006 · 3:04 PM")
}

type chartPoint struct {
	X      int
	Height int
	Label  string
	Amount string
	Known  bool
}

type spendChart struct {
	Currency string
	Points   []chartPoint
}

// Geometry is computed only from validated server totals. The template emits
// the SVG and escaped labels; no caller-controlled markup is trusted.
func weeklyChart(insights shopping.Insights) []spendChart {
	charts := []spendChart{}
	for _, total := range insights.Spend {
		var peak int64
		for _, week := range insights.Weeks {
			for _, spend := range week.Spend {
				if spend.Currency == total.Currency && spend.MerchandiseMinor != nil {
					peak = max(peak, *spend.MerchandiseMinor)
				}
			}
		}
		chart := spendChart{Currency: total.Currency, Points: []chartPoint{}}
		for i, week := range insights.Weeks {
			point := chartPoint{X: 5 + i*34, Label: strings.TrimPrefix(week.LocalStart[5:], "0"), Amount: "No recorded amount"}
			for _, spend := range week.Spend {
				if spend.Currency == total.Currency && spend.MerchandiseMinor != nil {
					point.Known = true
					// Floating point is used only for display geometry. Source
					// money and every calculated amount remain exact integers.
					if peak > 0 {
						point.Height = int(float64(*spend.MerchandiseMinor) / float64(peak) * 70)
					}
					point.Amount = moneyText(spend.MerchandiseMinor, total.Currency)
					if spend.MissingMerchandiseLines > 0 {
						point.Amount += " known subtotal"
					}
				}
			}
			chart.Points = append(chart.Points, point)
		}
		charts = append(charts, chart)
	}
	return charts
}
