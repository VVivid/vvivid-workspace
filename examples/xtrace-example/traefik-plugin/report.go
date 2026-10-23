package traceplugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

const (
	TaskHeader   = "X-XTrace-TaskID"
	OpHeader     = "X-XTrace-OpID"
	ParentHeader = "X-XTrace-ParentID"
	EdgeHeader   = "X-XTrace-EdgeType"
)

// Report records one operation. The parent ID, rather than the local clock,
// connects it to the operation that caused it.
type Report struct {
	TaskID    string            `json:"taskID"`
	OpID      string            `json:"opID"`
	ParentID  string            `json:"parentID"`
	EdgeType  string            `json:"edgeType"`
	Service   string            `json:"service"`
	Timestamp string            `json:"timestamp"`
	Details   map[string]string `json:"details"`
}

func NewID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// Reporter has one worker and at most 128 queued reports. A slow or unavailable
// collector drops telemetry instead of holding the image request open.
type Reporter struct {
	endpoint string
	client   *http.Client
	queue    chan []byte
}

func NewReporter(ctx context.Context, endpoint string) (*Reporter, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, fmt.Errorf("collector endpoint must be an absolute HTTP URL")
	}
	r := &Reporter{
		endpoint: endpoint,
		client:   &http.Client{Timeout: 500 * time.Millisecond, Transport: http.DefaultTransport},
		queue:    make(chan []byte, 128),
	}
	go r.run(ctx)
	return r, nil
}

func (r *Reporter) Emit(report Report) {
	payload, err := json.Marshal(report)
	if err != nil {
		log.Printf("report encoding failed: %v", err)
		return
	}
	select {
	case r.queue <- payload:
	default:
		log.Printf("report queue full: service=%s taskID=%s opID=%s", report.Service, report.TaskID, report.OpID)
	}
}

func (r *Reporter) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case payload := <-r.queue:
			r.send(ctx, payload)
		}
	}
}

func (r *Reporter) send(ctx context.Context, payload []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(payload))
	if err != nil {
		log.Printf("report request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		log.Printf("report delivery failed: %v", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		log.Printf("report rejected: status=%d", resp.StatusCode)
	}
}
