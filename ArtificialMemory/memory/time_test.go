package memory

import (
	"errors"
	"testing"
	"time"
)

func TestResolveRangeDST(t *testing.T) {
	for _, tc := range []struct {
		date, end, zone, from string
		hours                 time.Duration
	}{
		{"2026-03-08", "2026-03-09", "", "2026-03-08T05:00:00Z", 23},
		{"2026-11-01", "2026-11-02", "America/New_York", "2026-11-01T04:00:00Z", 25},
		{"2026-03-08", "2026-03-09", "UTC", "2026-03-08T00:00:00Z", 24},
	} {
		t.Run(tc.date+tc.zone, func(t *testing.T) {
			r, err := ResolveRange(TimeRange{Calendar: &CalendarRange{StartDate: tc.date, EndDate: tc.end, Timezone: tc.zone}})
			if err != nil {
				t.Fatal(err)
			}
			if r.From.Format(time.RFC3339) != tc.from || r.Before.Sub(*r.From) != tc.hours*time.Hour || r.Calendar != nil {
				t.Fatalf("wrong applied range: %+v", r)
			}
		})
	}
	for _, r := range []TimeRange{
		{Calendar: &CalendarRange{StartDate: "2026-02-30", EndDate: "2026-03-01"}},
		{Calendar: &CalendarRange{StartDate: "2026-03-01", EndDate: "2026-03-01"}},
		{Calendar: &CalendarRange{StartDate: "2026-03-01", EndDate: "2026-03-02", Timezone: "Mars/Olympus"}},
		{Calendar: &CalendarRange{StartDate: "2026-03-01", EndDate: "2026-03-02", Timezone: "Local"}},
		{Calendar: &CalendarRange{StartDate: "2011-12-30", EndDate: "2011-12-31", Timezone: "Pacific/Apia"}},
		{From: new(time.Now()), Calendar: &CalendarRange{}},
		{From: new(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))},
	} {
		if _, err := ResolveRange(r); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected invalid range, got %v", err)
		}
	}
}
