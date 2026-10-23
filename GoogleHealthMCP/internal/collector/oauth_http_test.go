package collector

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthLoopbackPKCEStateAndPartialGrant(t *testing.T) {
	var challenge string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "synthetic-code" {
			t.Error("token exchange lost its PKCE verifier or code")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expires_in": 3600, "token_type": "Bearer", "scope": RequiredScopes[1]})
	}))
	defer tokenServer.Close()
	o := &OAuth{HTTP: NewHTTP(), Credentials: ClientCredentials{"synthetic-client", "synthetic-client-secret"}, TokenDir: filepath.Join(t.TempDir(), "tokens"), tokenURL: tokenServer.URL, Tokens: Tokens{PublishingStatus: "testing"}}
	err := o.Authorize(t.Context(), "0", true, func(event AuthorizationEvent) {
		u, err := url.Parse(event.URL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		challenge = q.Get("code_challenge")
		if q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" || q.Get("scope") != strings.Join(RequiredScopes, " ") {
			t.Fatal("authorization requested the wrong permissions or PKCE mode")
		}
		callback, _ := url.Parse(q.Get("redirect_uri"))
		callback.RawQuery = url.Values{"code": {"synthetic-code"}, "state": {"wrong-state"}}.Encode()
		response, err := http.Get(callback.String())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatal("OAuth callback accepted an invalid state")
		}
		callback.RawQuery = url.Values{"code": {"synthetic-code"}, "state": {q.Get("state")}}.Encode()
		response, err = http.Get(callback.String())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	})
	if err != nil || o.Tokens.Verified || !o.HasAnyScope() || len(o.Tokens.Scopes) != 1 || o.Tokens.Scopes[0] != RequiredScopes[1] {
		t.Fatalf("partial grant mishandled: %v", err)
	}
	state, err := ReadTokens(o.TokenDir)
	if err != nil || state.Refresh != "synthetic-refresh" || state.Verified {
		t.Fatal("initial tokens were not saved pending actual account verification")
	}
}

func TestRefreshRotationPersistsAcrossProcessRecreationAndRevocation(t *testing.T) {
	e, _ := fixture(t)
	o := e.OAuth
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		r.ParseForm()
		if call == 1 && r.Form.Get("refresh_token") != "synthetic-refresh-secret" {
			t.Error("wrong refresh token used")
		}
		if call == 3 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PRIVATE_SYNTHETIC_VALUE"}`))
			return
		}
		response := map[string]any{"access_token": "fresh-access", "expires_in": 3600, "token_type": "Bearer"}
		if call == 1 {
			response["refresh_token"] = "rotated-refresh"
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	o.tokenURL = server.URL
	before, _ := os.Stat(filepath.Join(o.TokenDir, "tokens.json"))
	if _, err := o.Access(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(o.TokenDir, "tokens.json"))
	if os.SameFile(before, after) || after.Mode().Perm() != 0600 {
		t.Fatal("rotation was not an atomic restricted-file replacement")
	}
	saved, err := ReadTokens(o.TokenDir)
	if err != nil || saved.Refresh != "rotated-refresh" || len(saved.Scopes) != len(RequiredScopes) {
		t.Fatal("rotation or omitted-scope preservation lost across recreation")
	}
	// A new OAuth instance represents a new container using the same directory.
	recreated := &OAuth{HTTP: NewHTTP(), TokenDir: o.TokenDir, Tokens: saved, tokenURL: server.URL}
	if _, err := recreated.Access(t.Context(), true); err != nil || recreated.Tokens.Refresh != "rotated-refresh" {
		t.Fatal("refresh response without a replacement discarded the refresh token")
	}
	if _, err := recreated.Access(t.Context(), true); Code(err) != "reauthorization_required" || recreated.Tokens.Verified {
		t.Fatal("revoked refresh token remained authorized")
	}
	saved, _ = ReadTokens(o.TokenDir)
	if saved.Problem != "reauthorization_required" || saved.Verified || calls.Load() != 3 {
		t.Fatal("revocation state was not persisted or token exchange was retried")
	}
}

func TestTokenRequestIsNotRetriedAndSecretsAreSanitized(t *testing.T) {
	e, _ := fixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"PRIVATE health payload and refresh token"}}`))
	}))
	defer server.Close()
	e.OAuth.tokenURL = server.URL
	err := e.OAuth.exchange(t.Context(), url.Values{"grant_type": {"refresh_token"}}, false)
	if calls.Load() != 1 || err == nil || strings.Contains(err.Error(), "PRIVATE") || e.OAuth.Tokens.Refresh != "synthetic-refresh-secret" {
		t.Fatal("uncertain token exchange was retried or leaked credentials")
	}
}

func TestRetryBoundedAndProviderErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "999999")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"PRIVATE"}}`))
	}))
	defer server.Close()
	h := NewHTTP()
	var sleeps []time.Duration
	h.Sleep = func(_ context.Context, delay time.Duration) error { sleeps = append(sleeps, delay); return nil }
	var result any
	err := h.JSON(t.Context(), http.MethodGet, server.URL, "synthetic-access", "", nil, true, &result)
	if Code(err) != "rate_limited" || calls.Load() != 5 || len(sleeps) != 4 {
		t.Fatal("request retries were not bounded")
	}
	for _, d := range sleeps {
		if d > 60*time.Second {
			t.Fatal("Retry-After bypassed the backoff cap")
		}
	}
	for _, tc := range []struct{ reason, code string }{
		{"ACCOUNT_NOT_LINKED", "account_not_linked"}, {"MISSING_OAUTH_SCOPE", "missing_scope"},
		{"SERVICE_DISABLED", "api_not_enabled"}, {"CONSUMER_INVALID", "project_access_denied"},
		{"INVALID_PAGE_TOKEN", "invalid_page_token"}, {"PRIVATE_USER_VALUE", "api_error"},
	} {
		err := providerError(400, []byte(fmt.Sprintf(`{"error":{"message":"PRIVATE","details":[{"reason":%q}]}}`, tc.reason)))
		if Code(err) != tc.code || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("provider reason classification or sanitization failed")
		}
	}
}

func TestAPIUsesReconciliationAndExactRollupPaginationBody(t *testing.T) {
	e, _ := fixture(t)
	var getSeen, rollupSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-access-secret" {
			t.Error("missing API bearer token")
		}
		if r.Method == http.MethodGet {
			getSeen = true
			if r.URL.Path != "/v4/users/me/dataTypes/sleep/dataPoints:reconcile" || r.URL.Query().Get("pageSize") != "25" || r.URL.Query().Get("pageToken") != "page-two" || r.URL.Query().Get("dataSourceFamily") != "users/me/dataSourceFamilies/google-sources" || !strings.Contains(r.URL.Query().Get("filter"), "sleep.interval.end_time") {
				t.Error("GET did not use documented reconciliation/filter/pagination fields")
			}
			_, _ = w.Write([]byte(`{"dataPoints":[],"nextPageToken":"page-three"}`))
			return
		}
		rollupSeen = true
		var body struct {
			Range struct {
				Start, End struct {
					Date struct{ Year, Month, Day int }
				}
			}
			WindowSizeDays   int
			PageToken        string
			DataSourceFamily string
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Range.Start.Date.Day != 1 || body.Range.End.Date.Day != 3 || body.PageToken != "rollup-cursor" || body.WindowSizeDays != 1 || body.DataSourceFamily != "users/me/dataSourceFamilies/google-sources" || r.URL.Path != "/v4/users/me/dataTypes/total-calories/dataPoints:dailyRollUp" {
			t.Error("rollup body did not preserve its date range and page cursor")
		}
		_, _ = w.Write([]byte(`{"rollupDataPoints":[{"civilStartTime":{"date":{"year":2026,"month":10,"day":1}},"totalCalories":{"kilocaloriesSum":1}}],"nextPageToken":"rollup-next"}`))
	}))
	defer server.Close()
	a := NewAPI(e.OAuth)
	a.Base = server.URL
	w := Window{Since: "2026-10-01", Until: "2026-10-03"}
	page, err := a.Page(t.Context(), metric("sleep", "sleep", "sleep"), w, "page-two", 0)
	if err != nil || page.Next != "page-three" {
		t.Fatal("GET page token was lost")
	}
	m := metric("total-calories", "rollup", "activity_and_fitness")
	m.Method = "dailyRollUp"
	page, err = a.Page(t.Context(), m, w, "rollup-cursor", 0)
	if err != nil || len(page.Data) != 1 || page.Next != "rollup-next" || !getSeen || !rollupSeen {
		t.Fatal("rollup page or original response was lost")
	}
}

func TestAPI401RefreshesOnceAndPinsIdentity(t *testing.T) {
	e, _ := fixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600,"token_type":"Bearer"}`))
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer new-access" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"Expired"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"healthUserId":"test-owner"}`))
	}))
	defer server.Close()
	e.OAuth.tokenURL = server.URL + "/token"
	a := NewAPI(e.OAuth)
	a.Base = server.URL
	id, err := a.Identity(t.Context())
	if err != nil || id != "test-owner" || calls.Load() != 2 {
		t.Fatal("401 recovery did not refresh exactly once")
	}
}

func TestSecretPermissionsAndTokenDirectoryMountContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.json")
	if err := os.WriteFile(path, []byte(`{"installed":{"client_id":"synthetic"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredentials(path); Code(err) != "insecure_secret_file" {
		t.Fatal("publicly readable client file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredentials(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredentials(link); Code(err) != "insecure_secret_file" {
		t.Fatal("secret symlink accepted")
	}
}
