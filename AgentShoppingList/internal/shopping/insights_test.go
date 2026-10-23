package shopping

import (
	"math"
	"strings"
	"testing"
	"time"

	"agentshoppinglist/internal/store/sqlite"
)

func TestCalendarWeekDSTBoundariesRecordedCoverageAndMissingAmounts(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, location)
	boundary := time.Date(2026, 3, 9, 0, 0, 0, 0, location)
	to := time.Date(2026, 3, 16, 0, 0, 0, 0, location)
	prefs := sqlite.Preferences{Timezone: location.String(), WeekStart: "monday"}
	lines := []sqlite.InsightLine{
		{PurchasedAt: boundary.Add(-time.Millisecond).UnixMilli(), Currency: "USD", Category: "drinks", Quantity: "1", Unit: "l", Merchandise: new(int64(100))},
		{PurchasedAt: boundary.UnixMilli(), Currency: "USD", Category: "drinks", Quantity: "2", Unit: "l", Merchandise: new(int64(200))},
		{PurchasedAt: boundary.Add(time.Second).UnixMilli(), Currency: "USD", Category: "pantry", Quantity: "1", Unit: "pack"},
	}
	result, err := calculateInsights(lines, prefs, from, to, to.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Weeks) != 2 || time.Duration(result.Weeks[0].To-result.Weeks[0].From)*time.Millisecond != 167*time.Hour || !result.Weeks[0].CompleteCalendarWeek || !result.Weeks[1].CompleteCalendarWeek {
		t.Fatalf("DST calendar boundaries: %#v", result.Weeks)
	}
	if *result.Weeks[0].Spend[0].MerchandiseMinor != 100 || *result.Weeks[1].Spend[0].MerchandiseMinor != 200 || result.Weeks[1].Spend[0].MissingMerchandiseLines != 1 || result.UnavailableQuantityLines != 1 {
		t.Fatalf("boundary/missing counts: %#v", result)
	}
	partial, err := calculateInsights(nil, prefs, from.Add(time.Hour), boundary.Add(time.Hour), to)
	if err != nil || len(partial.Weeks) != 2 || partial.Weeks[0].CompleteCalendarWeek || partial.Weeks[1].CompleteCalendarWeek || len(partial.Spend) != 0 || !strings.Contains(partial.Weeks[0].Coverage, "unknown") {
		t.Fatal("partial or absent weeks claimed complete/zero")
	}
	fallFrom := time.Date(2026, 10, 26, 0, 0, 0, 0, location)
	fallTo := time.Date(2026, 11, 2, 0, 0, 0, 0, location)
	fall, err := calculateInsights(nil, prefs, fallFrom, fallTo, fallTo)
	if err != nil || time.Duration(fall.Weeks[0].To-fall.Weeks[0].From)*time.Millisecond != 169*time.Hour {
		t.Fatal("fall DST week forced into 168 hours")
	}
	if got := startOfWeek(boundary.Add(-time.Hour), "sunday").Format("2006-01-02"); got != "2026-03-08" {
		t.Fatalf("week-start preference ignored: %s", got)
	}
}

func TestInsightExactConversionsAndOverflowFailure(t *testing.T) {
	for _, test := range []struct {
		line           sqlite.InsightLine
		quantity, unit string
		known          bool
	}{
		{sqlite.InsightLine{Quantity: "1", Unit: "pack", PackCount: new(int64(8)), PackSize: new("12"), PackUnit: new("fl oz")}, "2.839058838", "L", true},
		{sqlite.InsightLine{Quantity: "2.5", Unit: "kg"}, "2500", "g", true},
		{sqlite.InsightLine{Quantity: "1", Unit: "lb"}, "453.59237", "g", true},
		{sqlite.InsightLine{Quantity: "1", Unit: "oz"}, "28.349523125", "g", true},
		{sqlite.InsightLine{Quantity: "250", Unit: "ml"}, "0.25", "L", true},
		{sqlite.InsightLine{Quantity: "2", Unit: "pack", PackCount: new(int64(6))}, "12", "each", true},
		{sqlite.InsightLine{Quantity: "1", Unit: "pack", PackSize: new("12"), PackUnit: new("fl oz")}, "", "", false},
		{sqlite.InsightLine{Quantity: "1", Unit: "pack", PackCount: new(int64(8)), PackSize: new("12"), PackUnit: new("unknown")}, "", "", false},
	} {
		quantity, unit, known := comparableQuantity(test.line)
		if known != test.known || unit != test.unit {
			t.Fatalf("conversion availability: %#v", test)
		}
		if known && strings.TrimRight(strings.TrimRight(quantity.FloatString(18), "0"), ".") != test.quantity {
			t.Fatalf("inexact conversion: %#v -> %s", test, quantity)
		}
	}
	location := time.UTC
	from := time.Date(2026, 10, 12, 0, 0, 0, 0, location)
	lines := []sqlite.InsightLine{
		{PurchasedAt: from.UnixMilli(), Currency: "USD", Category: "drinks", Quantity: "1", Unit: "each", Merchandise: new(int64(math.MaxInt64))},
		{PurchasedAt: from.UnixMilli(), Currency: "USD", Category: "drinks", Quantity: "1", Unit: "each", Merchandise: new(int64(1))},
	}
	if _, err := calculateInsights(lines, sqlite.Preferences{Timezone: "UTC", WeekStart: "monday"}, from, from.AddDate(0, 0, 7), from.AddDate(0, 0, 8)); err == nil {
		t.Fatal("overflow wrapped a recorded total")
	}
	amount := sqlite.PurchaseLineDraft{Quantity: "0.5", UnitPriceMinor: new(int64(199)), DiscountMinor: new(int64(0)), LineTotalMinor: new(int64(100)), TaxTreatment: "unknown"}
	if err := validatePurchaseMoney(amount); err != nil {
		t.Fatalf("half-up arithmetic rejected: %v", err)
	}
	amount.LineTotalMinor = new(int64(99))
	if err := validatePurchaseMoney(amount); err == nil {
		t.Fatal("fractional price silently truncated")
	}
}
