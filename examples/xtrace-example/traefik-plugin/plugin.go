// Package traceplugin is both the demo's wire protocol and Traefik middleware.
// It uses Go 1.20 syntax and only standard packages for Traefik's interpreter.
package traceplugin

import (
	"context"
	"net/http"
	"time"
)

type Config struct {
	CollectorURL string `json:"collectorURL"`
}

func CreateConfig() *Config {
	return &Config{CollectorURL: "http://collector:8080/reports"}
}

type Middleware struct {
	next     http.Handler
	reporter *Reporter
}

func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	reporter, err := NewReporter(ctx, config.CollectorURL)
	if err != nil {
		return nil, err
	}
	return &Middleware{next: next, reporter: reporter}, nil
}

func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	taskID, parentID := r.Header.Get(TaskHeader), r.Header.Get(OpHeader)
	if !ValidID(taskID) || !ValidID(parentID) {
		http.Error(w, "missing or invalid X-Trace metadata", http.StatusBadRequest)
		return
	}
	opID, err := NewID()
	if err != nil {
		http.Error(w, "cannot create operation ID", http.StatusInternalServerError)
		return
	}
	m.reporter.Emit(Report{
		TaskID: taskID, OpID: opID, ParentID: parentID, EdgeType: "NEXT",
		Service: "traefik", Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Details: map[string]string{"path": r.URL.Path, "load_balancer": "round-robin"},
	})
	// Traefik's actual load balancer receives this updated causal context.
	r.Header.Set(OpHeader, opID)
	r.Header.Set(ParentHeader, parentID)
	r.Header.Set(EdgeHeader, "NEXT")
	m.next.ServeHTTP(w, r)
}
