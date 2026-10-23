package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	_ "github.com/mattn/go-sqlite3"
	trace "github.com/xtrace-example/traceplugin"
)

type Collector struct {
	db *sql.DB
}

func NewCollector(path string) (*Collector, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS reports (
		task_id TEXT NOT NULL, op_id TEXT NOT NULL, payload TEXT NOT NULL,
		PRIMARY KEY (task_id, op_id)
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Collector{db: db}, nil
}

func (c *Collector) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /reports", c.receiveReport)
	mux.HandleFunc("GET /worktree", c.showWorktree)
	return mux
}

func validateReport(r trace.Report) error {
	if !trace.ValidID(r.TaskID) || !trace.ValidID(r.OpID) {
		return fmt.Errorf("taskID and opID must be 32 lowercase hexadecimal characters")
	}
	if r.Service == "nginx" {
		if r.ParentID != "" || r.EdgeType != "" {
			return fmt.Errorf("nginx is the root and must have empty parentID and edgeType")
		}
	} else {
		if r.Service != "traefik" && r.Service != "replica-A" && r.Service != "replica-B" {
			return fmt.Errorf("unknown service")
		}
		if !trace.ValidID(r.ParentID) || r.ParentID == r.OpID || r.EdgeType != "NEXT" {
			return fmt.Errorf("child report must have a different valid parentID and NEXT edgeType")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, r.Timestamp); err != nil {
		return fmt.Errorf("timestamp must be RFC3339")
	}
	for key, value := range r.Details {
		if len(key) == 0 || len(key) > 64 || len(value) > 1024 {
			return fmt.Errorf("details keys must be 1-64 bytes and values at most 1024 bytes")
		}
	}
	return nil
}

func (c *Collector) receiveReport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var report trace.Report
	err := decoder.Decode(&report)
	if err == nil {
		var extra any
		if trailingErr := decoder.Decode(&extra); !errors.Is(trailingErr, io.EOF) {
			err = fmt.Errorf("body must contain one JSON object")
		}
	}
	if err != nil {
		status := http.StatusBadRequest
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return
	}
	if err := validateReport(report); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	payload, err := json.Marshal(report)
	if err != nil {
		http.Error(w, "cannot encode report", http.StatusInternalServerError)
		return
	}
	result, err := c.db.ExecContext(r.Context(),
		"INSERT INTO reports(task_id, op_id, payload) VALUES (?, ?, ?) ON CONFLICT DO NOTHING",
		report.TaskID, report.OpID, string(payload))
	if err != nil {
		log.Printf("store report: %v", err)
		http.Error(w, "cannot store report", http.StatusInternalServerError)
		return
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		http.Error(w, "cannot confirm report storage", http.StatusInternalServerError)
		return
	}
	if inserted == 0 {
		var existing string
		if err := c.db.QueryRowContext(r.Context(), "SELECT payload FROM reports WHERE task_id = ? AND op_id = ?",
			report.TaskID, report.OpID).Scan(&existing); err != nil {
			http.Error(w, "cannot read existing report", http.StatusInternalServerError)
			return
		}
		if existing != string(payload) {
			http.Error(w, "operation ID already has a different report", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (c *Collector) reports(r *http.Request, taskID string) ([]trace.Report, error) {
	rows, err := c.db.QueryContext(r.Context(), "SELECT payload FROM reports WHERE task_id = ? ORDER BY op_id", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reports []trace.Report
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var report trace.Report
		if err := json.Unmarshal([]byte(payload), &report); err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, rows.Err()
}
