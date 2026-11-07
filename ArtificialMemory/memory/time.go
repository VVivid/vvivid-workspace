package memory

import (
	"fmt"
	"time"
	_ "time/tzdata" // Calendar semantics must not depend on host tzdata installation.
)

// ResolveRange converts local calendar dates to UTC instants, including DST.
func ResolveRange(r TimeRange) (TimeRange, error) {
	if r.Calendar != nil {
		if r.From != nil || r.Before != nil {
			return TimeRange{}, fmt.Errorf("%w: calendar and instant bounds cannot be combined", ErrInvalid)
		}
		zone := r.Calendar.Timezone
		if zone == "" {
			zone = DefaultTimezone
		}
		if zone == "Local" || len(zone) > 128 {
			return TimeRange{}, fmt.Errorf("%w: timezone must be an explicit IANA name", ErrInvalid)
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return TimeRange{}, fmt.Errorf("%w: invalid timezone", ErrInvalid)
		}
		from, err := time.ParseInLocation(time.DateOnly, r.Calendar.StartDate, loc)
		if err != nil || from.Format(time.DateOnly) != r.Calendar.StartDate {
			return TimeRange{}, fmt.Errorf("%w: invalid start date", ErrInvalid)
		}
		before, err := time.ParseInLocation(time.DateOnly, r.Calendar.EndDate, loc)
		if err != nil || before.Format(time.DateOnly) != r.Calendar.EndDate {
			return TimeRange{}, fmt.Errorf("%w: invalid end date", ErrInvalid)
		}
		r = TimeRange{From: new(from.UTC()), Before: new(before.UTC())}
	}
	for _, instant := range []*time.Time{r.From, r.Before} {
		if instant != nil && (instant.UTC().Year() < 1 || instant.UTC().Year() > 9999) {
			return TimeRange{}, fmt.Errorf("%w: timestamp outside years 1..9999", ErrInvalid)
		}
	}
	if r.From != nil {
		r.From = new(r.From.UTC())
	}
	if r.Before != nil {
		r.Before = new(r.Before.UTC())
	}
	if r.From != nil && r.Before != nil && !r.From.Before(*r.Before) {
		return TimeRange{}, fmt.Errorf("%w: empty or reversed time range", ErrInvalid)
	}
	return r, nil
}
