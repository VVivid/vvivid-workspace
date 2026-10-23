package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HTTP struct {
	Client *http.Client
	Sleep  func(context.Context, time.Duration) error
}

func NewHTTP() *HTTP {
	return &HTTP{Client: &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, Sleep: func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return &Error{Code: "interrupted"}
		case <-timer.C:
			return nil
		}
	}}
}

// JSON retries only idempotent reads (including the read-only rollup POST).
// Token exchanges are never retried automatically: a refresh token may rotate.
func (h *HTTP) JSON(ctx context.Context, method, url, token, contentType string, body []byte, retry bool, out any) error {
	attempts := 1
	if retry {
		attempts = 5
	}
	for attempt := range attempts {
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return &Error{Code: "request_failed"}
		}
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := h.Client.Do(req)
		var data []byte
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20+1))
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return &Error{Code: "interrupted"}
		}
		transient := err != nil || resp.StatusCode == 429 || resp.StatusCode >= 500
		if transient && attempt+1 < attempts {
			delay := time.Second*time.Duration(1<<attempt) + time.Duration(rand.IntN(250))*time.Millisecond
			if resp != nil {
				if seconds, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && seconds > 0 {
					delay = min(time.Duration(seconds)*time.Second, 60*time.Second)
				} else if when, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
					delay = min(max(time.Until(when), delay), 60*time.Second)
				}
			}
			if err := h.Sleep(ctx, delay); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return &Error{Code: "network_error"}
		}
		if len(data) > 64<<20 {
			return &Error{Code: "response_too_large"}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return providerError(resp.StatusCode, data)
		}
		if err := json.Unmarshal(data, out); err != nil {
			return &Error{Code: "invalid_response"}
		}
		return nil
	}
	return &Error{Code: "retry_exhausted"}
}

func providerError(status int, data []byte) error {
	// Extract only recognized enum values; never expose message, metadata,
	// request URL, or an arbitrary provider error string.
	var oauth struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &oauth) == nil && oauth.Error == "invalid_grant" {
		return &Error{Code: "reauthorization_required", Status: status}
	}
	var api struct {
		Error struct {
			Details []struct {
				Reason   string `json:"reason"`
				Metadata struct {
					DetailedReasons json.RawMessage `json:"detailedReasons"`
				} `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &api)
	for _, detail := range api.Error.Details {
		var reasons []string
		if json.Unmarshal(detail.Metadata.DetailedReasons, &reasons) != nil {
			var single string
			if json.Unmarshal(detail.Metadata.DetailedReasons, &single) == nil {
				reasons = strings.FieldsFunc(single, func(r rune) bool { return r == ',' || r == ' ' })
			}
		}
		for _, reason := range append(reasons, detail.Reason) {
			switch reason {
			case "MISSING_OAUTH_SCOPE", "ACCESS_TOKEN_SCOPE_INSUFFICIENT":
				return &Error{Code: "missing_scope", Status: status}
			case "INVALID_PAGE_TOKEN", "PAGE_TOKEN_EXPIRED", "PAGE_TOKEN_INVALID":
				return &Error{Code: "invalid_page_token", Status: status}
			case "ACCOUNT_NOT_LINKED":
				return &Error{Code: "account_not_linked", Status: status}
			case "SERVICE_DISABLED", "API_DISABLED":
				return &Error{Code: "api_not_enabled", Status: status}
			case "CONSUMER_INVALID", "PROJECT_DENIED":
				return &Error{Code: "project_access_denied", Status: status}
			}
		}
	}
	code := "api_error"
	switch status {
	case 401:
		code = "unauthorized"
	case 403:
		code = "permission_denied"
	case 404:
		code = "resource_unavailable"
	case 429:
		code = "rate_limited"
	}
	return &Error{Code: code, Status: status}
}
