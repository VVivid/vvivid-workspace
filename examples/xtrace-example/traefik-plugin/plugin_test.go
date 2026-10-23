package traceplugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func installTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
}

func createdResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
}

func TestMiddlewareAdvancesContextAndReportsIndependently(t *testing.T) {
	reports := make(chan Report, 1)
	installTransport(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		var report Report
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		reports <- report
		return createdResponse(), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var forwarded http.Header
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		forwarded = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})
	middleware, err := New(ctx, next, &Config{CollectorURL: "http://collector/reports"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	taskID, parentID := "11111111111111111111111111111111", "22222222222222222222222222222222"
	req := httptest.NewRequest(http.MethodGet, "/cat.svg", nil)
	req.Header.Set(TaskHeader, taskID)
	req.Header.Set(OpHeader, parentID)
	w := httptest.NewRecorder()
	middleware.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent || forwarded.Get(TaskHeader) != taskID || forwarded.Get(ParentHeader) != parentID || forwarded.Get(EdgeHeader) != "NEXT" || !ValidID(forwarded.Get(OpHeader)) || forwarded.Get(OpHeader) == parentID {
		t.Fatalf("forwarded context: %d %+v", w.Code, forwarded)
	}
	select {
	case report := <-reports:
		if report.TaskID != taskID || report.ParentID != parentID || report.OpID != forwarded.Get(OpHeader) || report.EdgeType != "NEXT" || report.Service != "traefik" {
			t.Fatalf("proxy report: %+v", report)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxy report was not delivered")
	}
	bad := httptest.NewRecorder()
	middleware.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/cat.svg", nil))
	if bad.Code != http.StatusBadRequest || called != 1 {
		t.Fatal("invalid trace reached downstream handler")
	}
}

func TestSlowCollectorDoesNotBlockRequestsAndQueueIsBounded(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	installTransport(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return createdResponse(), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}))
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	handler, err := New(ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
	}), &Config{CollectorURL: "http://collector/reports"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/cat.svg", nil)
	req.Header.Set(TaskHeader, "11111111111111111111111111111111")
	req.Header.Set(OpHeader, "22222222222222222222222222222222")
	returned := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), req)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("request waited for the blocked collector")
	}
	select {
	case <-called:
	default:
		t.Fatal("downstream handler was not called")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("report worker did not start delivery")
	}
	middleware := handler.(*Middleware)
	for i := 0; i < 200; i++ {
		middleware.reporter.Emit(Report{Service: "test"})
	}
	if len(middleware.reporter.queue) != 128 {
		t.Fatalf("queue length = %d; want bounded capacity 128", len(middleware.reporter.queue))
	}
}
