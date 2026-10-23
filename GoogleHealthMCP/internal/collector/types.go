package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const ScopePrefix = "https://www.googleapis.com/auth/googlehealth."

var RequiredScopes = []string{
	ScopePrefix + "activity_and_fitness.readonly",
	ScopePrefix + "sleep.readonly",
	ScopePrefix + "health_metrics_and_measurements.readonly",
}

// Error exposes only an application-controlled code. Provider bodies and URLs
// can contain health information or credentials and must not reach log output.
type Error struct {
	Code       string
	Status     int
	SQLiteCode int
}

func (e *Error) Error() string { return e.Code }

func Code(err error) string {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return "internal_error"
}

// DiagnosticCodes returns numeric protocol/driver codes only. Error messages
// remain private because they may include request URLs, SQL values, or paths.
func DiagnosticCodes(err error) (httpStatus, sqliteCode int) {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Status, e.SQLiteCode
	}
	return 0, 0
}

type Metric struct {
	Name    string
	Kind    string // interval, sample, daily, exercise, sleep, rollup
	Scope   string
	Version string
	Method  string // reconcile, list, dailyRollUp
}

func metric(name, kind, scope string) Metric {
	return Metric{name, kind, ScopePrefix + scope + ".readonly", "v4", "reconcile"}
}

// This catalog represents importer capabilities, not a restriction on future
// SQL readers. Unsupported device metrics are reported as empty/unavailable.
func Metrics() []Metric {
	var result []Metric
	for _, name := range []string{"active-energy-burned", "active-minutes", "active-zone-minutes", "activity-level", "altitude", "distance", "floors", "sedentary-period", "steps", "swim-lengths-data", "time-in-heart-rate-zone"} {
		result = append(result, metric(name, "interval", "activity_and_fitness"))
	}
	result = append(result, metric("daily-vo2-max", "daily", "activity_and_fitness"), metric("exercise", "exercise", "activity_and_fitness"))
	for _, name := range []string{"run-vo2-max", "vo2-max"} {
		result = append(result, metric(name, "sample", "activity_and_fitness"))
	}
	result = append(result, metric("sleep", "sleep", "sleep"))
	for _, name := range []string{"blood-glucose", "body-fat", "core-body-temperature", "heart-rate", "heart-rate-variability", "height", "oxygen-saturation", "respiratory-rate-sleep-summary", "weight"} {
		result = append(result, metric(name, "sample", "health_metrics_and_measurements"))
	}
	for _, name := range []string{"daily-heart-rate-variability", "daily-heart-rate-zones", "daily-oxygen-saturation", "daily-respiratory-rate", "daily-resting-heart-rate", "daily-sleep-temperature-derivations"} {
		result = append(result, metric(name, "daily", "health_metrics_and_measurements"))
	}
	skin := metric("skin-temperature-sensors", "sample", "health_metrics_and_measurements")
	skin.Version, skin.Method = "v4beta", "list"
	result = append(result, skin)
	for _, name := range []string{"calories-in-heart-rate-zone", "total-calories"} {
		m := metric(name, "rollup", "activity_and_fitness")
		m.Method = "dailyRollUp"
		result = append(result, m)
	}
	return result
}

func (m Metric) Stream() string { return m.Version + "/" + m.Name + "/" + m.Method }

func (m Metric) usesCivilDates() bool {
	return m.Kind == "daily" || m.Kind == "exercise" || m.Kind == "rollup"
}

// dateBounds returns the earliest still-open date and the exclusive query end.
// Civil records can come from any location, including after travel. Without a
// profile scope, use the UTC-12 through UTC+14 envelope rather than a host zone.
func (m Metric) dateBounds(now time.Time) (partial, until string) {
	now = now.UTC()
	if m.usesCivilDates() {
		return now.Add(-12 * time.Hour).Format(time.DateOnly), now.Add(14*time.Hour).AddDate(0, 0, 1).Format(time.DateOnly)
	}
	return now.Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly)
}

func (m Metric) historyKey() string {
	if m.usesCivilDates() {
		return "history_civil_until"
	}
	return "history_until"
}

func (m Metric) JSONField() string {
	parts := strings.Split(m.Name, "-")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func (m Metric) FilterField() string {
	prefix := strings.ReplaceAll(m.Name, "-", "_")
	switch m.Kind {
	case "sample":
		return prefix + ".sample_time.physical_time"
	case "daily":
		return prefix + ".date"
	case "exercise":
		return prefix + ".interval.civil_start_time"
	case "sleep":
		return prefix + ".interval.end_time"
	default:
		return prefix + ".interval.start_time"
	}
}

type Window struct {
	Since string // empty is the unbounded historical tail
	Until string // exclusive date
	// DailyDate is the earliest date still partial when a daily transfer began.
	// Empty identifies a historical/manual window; daily retries retain this date.
	DailyDate string
}

func (w Window) purpose() string {
	if w.DailyDate != "" {
		return "sync"
	}
	return "backfill"
}

func (w Window) ID(m Metric) string { return windowID(m.Stream(), w) }

func windowID(stream string, w Window) string {
	key := stream + "|" + w.Since + "|" + w.Until
	if w.DailyDate != "" {
		// Namespace daily transfers separately, but keep their ID stable when
		// a completed range is refreshed on a later partial date.
		key = "sync|" + key
	}
	return digest(key)
}

func (w Window) Filter(m Metric) string {
	format := func(s string) string {
		if m.Kind == "daily" {
			return s
		}
		if m.Kind == "exercise" {
			return s + "T00:00:00"
		}
		return s + "T00:00:00Z"
	}
	upper := fmt.Sprintf(`%s < %q`, m.FilterField(), format(w.Until))
	if w.Since == "" {
		return upper
	}
	return fmt.Sprintf(`%s >= %q AND %s`, m.FilterField(), format(w.Since), upper)
}

func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil || t.Year() < 1 || t.Format(time.DateOnly) != s {
		return time.Time{}, &Error{Code: "invalid_date"}
	}
	return t, nil
}

func Windows(since, until string, days int) ([]Window, error) {
	from, err := ParseDate(since)
	if err != nil {
		return nil, err
	}
	to, err := ParseDate(until)
	if err != nil || !from.Before(to) || days < 1 {
		return nil, &Error{Code: "invalid_range"}
	}
	var windows []Window
	for from.Before(to) {
		end := minTime(from.AddDate(0, 0, days), to)
		windows = append(windows, Window{Since: from.Format(time.DateOnly), Until: end.Format(time.DateOnly)})
		from = end
	}
	return windows, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type Record struct {
	Key, Anchor, Start, End, Date, Source, Original string
	HasValue                                        bool
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func calendarDate(v any) (string, error) {
	m := object(v)
	number := func(k string) int {
		value, _ := m[k].(json.Number)
		n, _ := strconv.Atoi(string(value))
		return n
	}
	s := fmt.Sprintf("%04d-%02d-%02d", number("year"), number("month"), number("day"))
	_, err := ParseDate(s)
	return s, err
}

func physicalTime(v any) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, text(v))
	if err != nil {
		return "", &Error{Code: "invalid_record_time"}
	}
	// Fixed precision preserves chronological order in SQLite text indexes.
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z"), nil
}

func civilStart(interval map[string]any) (string, error) {
	if civil := object(interval["civilStartTime"]); civil != nil {
		day, err := calendarDate(civil["date"])
		if err != nil {
			return "", err
		}
		clock := object(civil["time"])
		integer := func(k string) int {
			n, _ := strconv.Atoi(textNumber(clock[k]))
			return n
		}
		h, min, sec := integer("hours"), integer("minutes"), integer("seconds")
		if h > 23 || min > 59 || sec > 59 || h < 0 || min < 0 || sec < 0 {
			return "", &Error{Code: "invalid_record_time"}
		}
		return fmt.Sprintf("%sT%02d:%02d:%02d", day, h, min, sec), nil
	}
	t, err := time.Parse(time.RFC3339Nano, text(interval["startTime"]))
	if err != nil {
		return "", &Error{Code: "invalid_record_time"}
	}
	offset, err := time.ParseDuration(text(interval["startUtcOffset"]))
	if err != nil {
		return "", &Error{Code: "missing_record_offset"}
	}
	return t.UTC().Add(offset).Format("2006-01-02T15:04:05"), nil
}

func textNumber(v any) string {
	if n, ok := v.(json.Number); ok {
		return string(n)
	}
	return ""
}

// DecodeRecord preserves the exact JSON element returned by the API, while
// indexing the same anchor used by its filter (sleep uses end time).
func DecodeRecord(m Metric, raw json.RawMessage, w Window) (Record, error) {
	var point map[string]any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err := d.Decode(&point); err != nil {
		return Record{}, &Error{Code: "invalid_record"}
	}
	payload := object(point[m.JSONField()])
	if value, exists := point[m.JSONField()]; exists && value != nil && payload == nil {
		return Record{}, &Error{Code: "invalid_record_payload"}
	}
	if payload == nil && m.Kind != "rollup" {
		return Record{}, &Error{Code: "missing_record_payload"}
	}
	r := Record{Original: string(raw), HasValue: payload != nil}
	var err error
	switch m.Kind {
	case "daily":
		r.Date, err = calendarDate(payload["date"])
		r.Anchor = r.Date
	case "rollup":
		r.Date, err = calendarDate(object(point["civilStartTime"])["date"])
		r.Anchor = r.Date
	case "sample":
		r.Start, err = physicalTime(object(payload["sampleTime"])["physicalTime"])
		r.End, r.Anchor = r.Start, r.Start
	default:
		interval := object(payload["interval"])
		r.Start, err = physicalTime(interval["startTime"])
		if err == nil {
			r.End, err = physicalTime(interval["endTime"])
		}
		r.Anchor = r.Start
		if m.Kind == "sleep" {
			r.Anchor = r.End
		} else if m.Kind == "exercise" && err == nil {
			r.Anchor, err = civilStart(interval)
		}
	}
	if err != nil || r.Anchor == "" || (r.End != "" && r.End < r.Start) {
		return Record{}, &Error{Code: "invalid_record_time"}
	}
	if (w.Since != "" && r.Anchor < w.Since) || r.Anchor >= w.Until {
		return Record{}, &Error{Code: "record_outside_window"}
	}
	source, _ := json.Marshal(point["dataSource"])
	r.Source = string(source)
	name := text(point["name"])
	if name == "" {
		name = text(point["dataPointName"])
	}
	// Reconciliation may split a named record into multiple intervals. Include
	// boundaries and source so those fragments cannot overwrite one another.
	if name == "" {
		name = digest(string(raw))
	}
	r.Key = digest(name + "|" + r.Start + "|" + r.End + "|" + r.Anchor + "|" + r.Source)
	return r, nil
}
