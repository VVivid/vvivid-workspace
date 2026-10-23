package shopping

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"strings"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
)

type InsightsInput struct {
	From     int64  `json:"from,omitzero"`
	To       int64  `json:"to,omitzero"`
	Interval string `json:"interval,omitempty"`
}

type Spend struct {
	Currency                            string `json:"currency"`
	MerchandiseMinor                    *int64 `json:"merchandise_minor"`
	TaxMinor                            *int64 `json:"tax_minor"`
	RecordedLines                       int    `json:"recorded_lines"`
	MissingMerchandiseLines             int    `json:"missing_merchandise_lines"`
	MissingTaxLines                     int    `json:"missing_tax_lines"`
	MerchandiseCompleteForRecordedLines bool   `json:"merchandise_complete_for_recorded_lines"`
}

type CategorySpend struct {
	Category string `json:"category"`
	Spend
}

type QuantityBought struct {
	Category string `json:"category"`
	Unit     string `json:"unit"`
	Quantity string `json:"quantity"`
}

type InsightWeek struct {
	From                     int64            `json:"from"`
	To                       int64            `json:"to"`
	LocalStart               string           `json:"local_start"`
	CompleteCalendarWeek     bool             `json:"complete_calendar_week"`
	RecordedLines            int              `json:"recorded_lines"`
	Spend                    []Spend          `json:"spend"`
	Quantities               []QuantityBought `json:"quantities_bought"`
	UnavailableQuantityLines int              `json:"unavailable_quantity_lines"`
	Coverage                 string           `json:"coverage"`
}

type Insights struct {
	From                     int64            `json:"from"`
	To                       int64            `json:"to"`
	Timezone                 string           `json:"timezone"`
	WeekStart                string           `json:"week_start"`
	Basis                    string           `json:"basis"`
	Coverage                 string           `json:"coverage"`
	Comparison               string           `json:"comparison"`
	QuantityDefinition       string           `json:"quantity_definition"`
	Weeks                    []InsightWeek    `json:"weeks"`
	Categories               []CategorySpend  `json:"categories"`
	Spend                    []Spend          `json:"spend"`
	Quantities               []QuantityBought `json:"quantities_bought"`
	UnavailableQuantityLines int              `json:"unavailable_quantity_lines"`
}

func (s *Service) GetInsights(ctx context.Context, p identity.Principal, in InsightsInput) (Insights, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return Insights{}, err
	}
	prefs, err := s.store.Preferences(ctx, p.Owner())
	if err != nil {
		return Insights{}, err
	}
	location, err := time.LoadLocation(prefs.Timezone)
	if err != nil {
		return Insights{}, err
	}
	now := time.Now()
	from, to, err := insightBounds(in, now, location, prefs.WeekStart)
	if err != nil {
		return Insights{}, err
	}
	lines, err := s.store.InsightLines(ctx, p.Owner(), from.UnixMilli(), to.UnixMilli())
	if err != nil {
		if errors.Is(err, sqlite.ErrLimit) {
			return Insights{}, invalid("insights exceed 25000 recorded lines; narrow the date range")
		}
		return Insights{}, err
	}
	return calculateInsights(lines, prefs, from, to, now)
}

func insightBounds(in InsightsInput, now time.Time, location *time.Location, weekStart string) (time.Time, time.Time, error) {
	if in.Interval != "" && in.Interval != "week" {
		return time.Time{}, time.Time{}, invalid("interval must be week")
	}
	to := now
	if in.To != 0 {
		to = time.UnixMilli(in.To)
	}
	from := startOfWeek(to.In(location), weekStart).AddDate(0, 0, -49)
	if in.From != 0 {
		from = time.UnixMilli(in.From)
	}
	if in.From < 0 || in.To < 0 || !from.Before(to) || to.After(now.Add(5*time.Minute)) || to.Sub(from) > 371*24*time.Hour {
		return time.Time{}, time.Time{}, invalid("insights require from before exclusive to, at most 371 days, with no future purchases")
	}
	return from.In(location), to.In(location), nil
}

func startOfWeek(t time.Time, first string) time.Time {
	weekdays := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	start := slices.Index(weekdays, first)
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return midnight.AddDate(0, 0, -(int(t.Weekday())-start+7)%7)
}

type insightTotals struct {
	spend                    map[string]*Spend
	quantities               map[string]*big.Rat
	unavailableQuantityLines int
}

func newInsightTotals() *insightTotals {
	return &insightTotals{spend: map[string]*Spend{}, quantities: map[string]*big.Rat{}}
}

func addKnown(sum **int64, value *int64) error {
	if value == nil {
		return nil
	}
	if *sum == nil {
		*sum = new(int64(0))
	}
	if *value < 0 || **sum > math.MaxInt64-*value {
		return invalid("recorded amounts exceed the supported total; narrow the date range")
	}
	**sum += *value
	return nil
}

func (totals *insightTotals) add(line sqlite.InsightLine) error {
	spend := totals.spend[line.Currency]
	if spend == nil {
		spend = &Spend{Currency: line.Currency}
		totals.spend[line.Currency] = spend
	}
	spend.RecordedLines++
	if err := addKnown(&spend.MerchandiseMinor, line.Merchandise); err != nil {
		return err
	}
	if err := addKnown(&spend.TaxMinor, line.Tax); err != nil {
		return err
	}
	if line.Merchandise == nil {
		spend.MissingMerchandiseLines++
	}
	if line.Tax == nil {
		spend.MissingTaxLines++
	}
	spend.MerchandiseCompleteForRecordedLines = spend.MissingMerchandiseLines == 0
	quantity, unit, ok := comparableQuantity(line)
	if !ok {
		totals.unavailableQuantityLines++
		return nil
	}
	key := line.Category + ":" + unit
	if totals.quantities[key] == nil {
		totals.quantities[key] = new(big.Rat)
	}
	totals.quantities[key].Add(totals.quantities[key], quantity)
	return nil
}

func (totals *insightTotals) spends() []Spend {
	result := []Spend{}
	for _, currency := range slices.Sorted(maps.Keys(totals.spend)) {
		result = append(result, *totals.spend[currency])
	}
	return result
}

func (totals *insightTotals) quantitiesBought() []QuantityBought {
	keys := slices.Sorted(maps.Keys(totals.quantities))
	result := []QuantityBought{}
	for _, key := range keys {
		category, unit, _ := strings.Cut(key, ":")
		value := strings.TrimRight(strings.TrimRight(totals.quantities[key].FloatString(18), "0"), ".")
		if value == "" {
			value = "0"
		}
		result = append(result, QuantityBought{Category: category, Unit: unit, Quantity: value})
	}
	return result
}

func calculateInsights(lines []sqlite.InsightLine, prefs sqlite.Preferences, from, to, now time.Time) (Insights, error) {
	result := Insights{
		From:               from.UnixMilli(),
		To:                 to.UnixMilli(),
		Timezone:           prefs.Timezone,
		WeekStart:          prefs.WeekStart,
		Basis:              "Recorded current confirmed merchandise after allocated discounts, excluding tax. Tax is shown separately. Currencies never combine.",
		Coverage:           "Recorded purchases only; historical completeness is unknown. No recorded lines means unknown coverage, never zero spending.",
		Comparison:         "No change percentage is claimed. Partial weeks are labelled; compare only complete calendar weeks with recorded coverage.",
		QuantityDefinition: "Bought, not consumed. Compatible category quantities use grams, liters or each. Pack conversion requires explicit count and supported size/unit; US fluid ounce = 0.0295735295625 L. Decimal output rounds only after exact aggregation to 18 fractional places.",
		Weeks:              []InsightWeek{},
		Categories:         []CategorySpend{},
	}
	all := newInsightTotals()
	categories := map[string]*insightTotals{}
	weekTotals := []*insightTotals{}
	weekIndex := map[int64]int{}
	for week := startOfWeek(from, prefs.WeekStart); week.Before(to); week = week.AddDate(0, 0, 7) {
		end := week.AddDate(0, 0, 7)
		weekIndex[week.UnixMilli()] = len(result.Weeks)
		result.Weeks = append(result.Weeks, InsightWeek{
			From:                 max(week.UnixMilli(), from.UnixMilli()),
			To:                   min(end.UnixMilli(), to.UnixMilli()),
			LocalStart:           week.Format("2006-01-02"),
			CompleteCalendarWeek: !week.Before(from) && !end.After(to) && !end.After(now),
			Coverage:             "No recorded purchases; coverage unknown",
			Spend:                []Spend{},
			Quantities:           []QuantityBought{},
		})
		weekTotals = append(weekTotals, newInsightTotals())
	}
	for _, line := range lines {
		if err := all.add(line); err != nil {
			return Insights{}, err
		}
		if categories[line.Category] == nil {
			categories[line.Category] = newInsightTotals()
		}
		if err := categories[line.Category].add(line); err != nil {
			return Insights{}, err
		}
		key := startOfWeek(time.UnixMilli(line.PurchasedAt).In(from.Location()), prefs.WeekStart).UnixMilli()
		index := weekIndex[key]
		if err := weekTotals[index].add(line); err != nil {
			return Insights{}, err
		}
		result.Weeks[index].RecordedLines++
	}
	for i, totals := range weekTotals {
		week := &result.Weeks[i]
		week.Spend = totals.spends()
		week.Quantities = totals.quantitiesBought()
		week.UnavailableQuantityLines = totals.unavailableQuantityLines
		if week.RecordedLines > 0 {
			week.Coverage = "Recorded purchases only; historical completeness unknown"
		}
	}
	for _, category := range []string{"produce", "meat", "drinks", "pantry", "dairy", "other"} {
		if totals := categories[category]; totals != nil {
			for _, spend := range totals.spends() {
				result.Categories = append(result.Categories, CategorySpend{Category: category, Spend: spend})
			}
		}
	}
	result.Spend = all.spends()
	result.Quantities = all.quantitiesBought()
	result.UnavailableQuantityLines = all.unavailableQuantityLines
	return result, nil
}

func comparableQuantity(line sqlite.InsightLine) (*big.Rat, string, bool) {
	quantity, ok := new(big.Rat).SetString(line.Quantity)
	if !ok || quantity.Sign() <= 0 {
		return nil, "", false
	}
	unit := line.Unit
	if unit == "pack" {
		if line.PackCount == nil || *line.PackCount < 1 {
			return nil, "", false
		}
		quantity.Mul(quantity, new(big.Rat).SetInt64(*line.PackCount))
		if line.PackSize == nil && line.PackUnit == nil {
			return quantity, "each", true
		}
		if line.PackSize == nil || line.PackUnit == nil {
			return nil, "", false
		}
		size, ok := new(big.Rat).SetString(*line.PackSize)
		if !ok || size.Sign() <= 0 {
			return nil, "", false
		}
		quantity.Mul(quantity, size)
		unit = strings.ToLower(strings.TrimSpace(*line.PackUnit))
	}
	canonical, factor := "", ""
	switch unit {
	case "each":
		canonical, factor = "each", "1"
	case "g":
		canonical, factor = "g", "1"
	case "kg":
		canonical, factor = "g", "1000"
	case "oz":
		canonical, factor = "g", "28.349523125"
	case "lb":
		canonical, factor = "g", "453.59237"
	case "ml":
		canonical, factor = "L", "0.001"
	case "l":
		canonical, factor = "L", "1"
	case "us_fl_oz", "fl oz", "us fl oz":
		canonical, factor = "L", "0.0295735295625"
	default:
		return nil, "", false
	}
	conversion, ok := new(big.Rat).SetString(factor)
	if !ok {
		panic(fmt.Sprintf("invalid built-in quantity conversion %s", factor))
	}
	return quantity.Mul(quantity, conversion), canonical, true
}
