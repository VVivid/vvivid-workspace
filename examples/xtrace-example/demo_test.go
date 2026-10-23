package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	trace "github.com/xtrace-example/traceplugin"
)

const (
	testTask = "11111111111111111111111111111111"
	rootOp   = "22222222222222222222222222222222"
	proxyOp  = "33333333333333333333333333333333"
	appOp    = "44444444444444444444444444444444"
)

func sampleChain() []trace.Report {
	return []trace.Report{
		{TaskID: testTask, OpID: rootOp, Service: "nginx", Timestamp: "2026-10-11T12:03:00Z", Details: map[string]string{"proxy_cache": "disabled"}},
		{TaskID: testTask, OpID: proxyOp, ParentID: rootOp, EdgeType: "NEXT", Service: "traefik", Timestamp: "2026-10-11T12:02:00Z"},
		{TaskID: testTask, OpID: appOp, ParentID: proxyOp, EdgeType: "NEXT", Service: "replica-B", Timestamp: "2026-10-11T12:01:00Z", Details: map[string]string{"cache_hit": "true", "image_timestamp": "2026-10-10T12:00:00Z"}},
	}
}

func postReport(t *testing.T, handler http.Handler, report trace.Report) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/reports", bytes.NewReader(body)).WithContext(t.Context())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestReportsPersistOutOfOrderAndRejectConflictingIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports.db")
	collector, err := NewCollector(path)
	if err != nil {
		t.Fatal(err)
	}
	chain := sampleChain()
	for _, i := range []int{2, 0, 1} {
		if w := postReport(t, collector.Handler(), chain[i]); w.Code != http.StatusCreated {
			t.Fatalf("insert: %d %s", w.Code, w.Body)
		}
	}
	if w := postReport(t, collector.Handler(), chain[2]); w.Code != http.StatusOK {
		t.Fatalf("identical duplicate: %d", w.Code)
	}
	conflicting := chain[2]
	conflicting.Service = "replica-A"
	if w := postReport(t, collector.Handler(), conflicting); w.Code != http.StatusConflict {
		t.Fatalf("conflicting duplicate: %d", w.Code)
	}
	if err := collector.db.Close(); err != nil {
		t.Fatal(err)
	}
	collector, err = NewCollector(path)
	if err != nil {
		t.Fatal(err)
	}
	defer collector.db.Close()
	req := httptest.NewRequest(http.MethodGet, "/worktree?taskID="+testTask, nil).WithContext(t.Context())
	reports, err := collector.reports(req, testTask)
	if err != nil || len(reports) != 3 {
		t.Fatalf("reopened reports: %v, count %d", err, len(reports))
	}
	tree := reconstruct(testTask, reports)
	if !tree.Complete || tree.Rows[2].Report.Service != "replica-B" {
		t.Fatalf("stored chain changed: %+v", tree)
	}
	other, err := collector.reports(req, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || len(other) != 0 {
		t.Fatalf("task isolation: %v, count %d", err, len(other))
	}
}

func TestCollectorRejectsInvalidReportsWithoutStoringThem(t *testing.T) {
	collector, err := NewCollector(filepath.Join(t.TempDir(), "reports.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer collector.db.Close()
	for _, mutate := range []struct {
		name string
		edit func(*trace.Report)
	}{
		{"invalid task", func(r *trace.Report) { r.TaskID = "bad" }},
		{"root parent", func(r *trace.Report) { r.ParentID = proxyOp }},
		{"root edge", func(r *trace.Report) { r.EdgeType = "NEXT" }},
		{"unknown service", func(r *trace.Report) { r.Service = "other" }},
		{"invalid timestamp", func(r *trace.Report) { r.Timestamp = "yesterday" }},
		{"self parent", func(r *trace.Report) { r.Service, r.ParentID, r.EdgeType = "traefik", r.OpID, "NEXT" }},
		{"wrong edge", func(r *trace.Report) { r.Service, r.ParentID, r.EdgeType = "traefik", proxyOp, "OTHER" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			report := sampleChain()[0]
			mutate.edit(&report)
			if w := postReport(t, collector.Handler(), report); w.Code != http.StatusBadRequest {
				t.Fatalf("invalid report accepted: %d", w.Code)
			}
		})
	}
	for _, body := range []string{`{`, `{ "unknown": true }`, `null`, `{} {}`} {
		w := httptest.NewRecorder()
		collector.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/reports", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid JSON accepted: %q: %d", body, w.Code)
		}
	}
	oversized := sampleChain()[0]
	oversized.Details = map[string]string{"large": strings.Repeat("x", 20*1024)}
	if w := postReport(t, collector.Handler(), oversized); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized report: %d", w.Code)
	}
	reports, err := collector.reports(httptest.NewRequest(http.MethodGet, "/worktree", nil), testTask)
	if err != nil || len(reports) != 0 {
		t.Fatalf("invalid reports mutated storage: %v, count %d", err, len(reports))
	}
}

func TestReconstructionUsesIDsAndShowsMissingParentsAndCycles(t *testing.T) {
	chain := sampleChain()
	tree := reconstruct(testTask, []trace.Report{chain[2], chain[0], chain[1]})
	if !tree.Complete || len(tree.Rows) != 3 {
		t.Fatalf("complete chain: %+v", tree)
	}
	for i, service := range []string{"nginx", "traefik", "replica-B"} {
		if tree.Rows[i].Report.Service != service || tree.Rows[i].Depth != i {
			t.Fatalf("causal order ignored IDs: %+v", tree.Rows)
		}
	}
	missing := reconstruct(testTask, []trace.Report{chain[2], chain[0]})
	if missing.Complete {
		t.Fatal("missing proxy marked complete")
	}
	found := false
	for _, row := range missing.Rows {
		if row.Missing && row.Report.OpID == proxyOp {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing parent placeholder absent: %+v", missing)
	}
	chain[1].ParentID = appOp
	cycle := reconstruct(testTask, chain)
	if cycle.Complete || !strings.Contains(strings.Join(cycle.Issues, " "), "Cycle") {
		t.Fatalf("cycle not detected: %+v", cycle)
	}
	if reconstruct(testTask, nil).Complete {
		t.Fatal("zero reports marked complete")
	}
}

func TestWorktreeEscapesDetailsAndWarnsAboutIncompleteObservations(t *testing.T) {
	collector, err := NewCollector(filepath.Join(t.TempDir(), "reports.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer collector.db.Close()
	report := sampleChain()[2]
	report.Details["untrusted"] = "<script>alert(1)</script>"
	postReport(t, collector.Handler(), report)
	w := httptest.NewRecorder()
	collector.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/worktree?taskID="+testTask, nil))
	for _, expected := range []string{"Incomplete observations", "MISSING parent report", "absence alone does not establish a fault", "&lt;script&gt;"} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("missing HTML content %q: %s", expected, w.Body)
		}
	}
	if strings.Contains(w.Body.String(), "<script>alert(1)</script>") {
		t.Fatal("details rendered as executable HTML")
	}
	bad := httptest.NewRecorder()
	collector.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/worktree?taskID=bad", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid task query: %d", bad.Code)
	}
}

func TestReplicaFreshAndStaleImagesMatchTheirReports(t *testing.T) {
	var emitted []trace.Report
	emit := func(r trace.Report) { emitted = append(emitted, r) }
	seedStarted := time.Now().UTC()
	fresh, err := NewReplica("A", 0, emit)
	if err != nil {
		t.Fatal(err)
	}
	seedFinished := time.Now().UTC()
	stale, err := NewReplica("B", 24*time.Hour, emit)
	if err != nil {
		t.Fatal(err)
	}
	request := func(replica *Replica) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/cat.svg", nil)
		req.Header.Set(trace.TaskHeader, testTask)
		req.Header.Set(trace.OpHeader, proxyOp)
		w := httptest.NewRecorder()
		replica.Handler().ServeHTTP(w, req)
		return w
	}
	a1, a2, b1, b2 := request(fresh), request(fresh), request(stale), request(stale)
	if a1.Body.String() != a2.Body.String() || b1.Body.String() != b2.Body.String() || a1.Body.String() == b1.Body.String() {
		t.Fatal("both images must stay cached and have distinct baked-in timestamps")
	}
	for i, w := range []*httptest.ResponseRecorder{a1, a2, b1, b2} {
		report := emitted[i]
		if w.Code != http.StatusOK || report.ParentID != proxyOp || report.EdgeType != "NEXT" || report.TaskID != testTask || !trace.ValidID(report.OpID) || report.OpID == proxyOp {
			t.Fatalf("response/report metadata: %d %+v", w.Code, report)
		}
		if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), report.Details["image_timestamp"]) || w.Header().Get("X-Image-Timestamp") != report.Details["image_timestamp"] || w.Header().Get("X-Cache-Hit") != report.Details["cache_hit"] {
			t.Fatalf("image/report mismatch: %+v", report)
		}
		imageTime, err := time.Parse(time.RFC3339Nano, report.Details["image_timestamp"])
		if err != nil {
			t.Fatal(err)
		}
		servedTime, err := time.Parse(time.RFC3339Nano, report.Timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && (imageTime.Before(seedStarted) || imageTime.After(seedFinished) || report.Details["cache_hit"] != "true" || report.Details["cache_seed_age"] != "0s") {
			t.Fatal("A report does not describe its current startup cache")
		}
		if i >= 2 && (servedTime.Sub(imageTime) < 24*time.Hour || report.Details["cache_hit"] != "true") {
			t.Fatal("stale report does not describe the seeded old cache")
		}
	}
	bad := httptest.NewRecorder()
	fresh.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/cat.svg", nil))
	if bad.Code != http.StatusBadRequest || len(emitted) != 4 {
		t.Fatal("request without causal context produced a report")
	}
}

type handlerTransport struct {
	handler http.Handler
}

func (transport handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	transport.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

// This feeds the real collector handler through an in-memory HTTP transport.
// Nginx Lua, Traefik's interpreter, and round robin still need a Compose run.
func TestProxyReplicaAndCollectorPropagation(t *testing.T) {
	collector, err := NewCollector(filepath.Join(t.TempDir(), "reports.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer collector.db.Close()
	stored := make(chan struct{}, 3)
	transport := handlerTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		collector.Handler().ServeHTTP(w, r)
		if r.Method == http.MethodPost {
			stored <- struct{}{}
		}
	})}
	originalTransport := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = originalTransport }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reporter, err := trace.NewReporter(ctx, "http://collector/reports")
	if err != nil {
		t.Fatal(err)
	}
	replica, err := NewReplica("B", 24*time.Hour, reporter.Emit)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := trace.New(ctx, replica.Handler(), &trace.Config{CollectorURL: "http://collector/reports"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if w := postReport(t, collector.Handler(), sampleChain()[0]); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/cat.svg", nil)
	req.Header.Set(trace.TaskHeader, testTask)
	req.Header.Set(trace.OpHeader, rootOp)
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("proxy response: %d %s", w.Code, w.Body)
	}
	for range 2 {
		select {
		case <-stored:
		case <-time.After(2 * time.Second):
			t.Fatal("reports not delivered")
		}
	}
	reports, err := collector.reports(req, testTask)
	if err != nil {
		t.Fatal(err)
	}
	tree := reconstruct(testTask, reports)
	if !tree.Complete || tree.Rows[1].Report.ParentID != rootOp || tree.Rows[2].Report.ParentID != tree.Rows[1].Report.OpID || tree.Rows[2].Report.Details["cache_hit"] != "true" {
		t.Fatalf("propagated chain: %+v", tree)
	}
}
