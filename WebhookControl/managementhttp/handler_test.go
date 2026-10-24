package managementhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/sqlite"
	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type httpFixture struct {
	store   *sqlite.Store
	service *management.Service
	actor   management.Identity
	grant   domain.Grant
	config  Config
	server  *httptest.Server
}

func setupHTTP(t *testing.T) *httpFixture {
	t.Helper()
	f := &httpFixture{actor: management.Identity{Issuer: "https://issuer.example", Subject: "synthetic-subject"}}
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"), bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.store = s
	t.Cleanup(func() { s.Close() })
	if err := s.PutSecret(t.Context(), f.actor.PrincipalID(), domain.SecretRef{Name: "inbound", Version: "1"}, domain.SecretSourceAuth, bytes.Repeat([]byte("synthetic-"), 4)); err != nil {
		t.Fatal(err)
	}
	f.grant = domain.Grant{ID: "grant", PrincipalID: f.actor.PrincipalID(), SourceIDs: []domain.ID{"source"}, Actions: []domain.Action{domain.ActionReadConfig, domain.ActionWriteConfig, domain.ActionActivateConfig, domain.ActionPreview, domain.ActionReadEvents}, SecretNames: []string{"inbound"}, ExpiresAt: new(time.Now().Add(time.Hour))}
	if err := s.ProvisionGrant(t.Context(), f.actor.PrincipalID(), f.grant); err != nil {
		t.Fatal(err)
	}
	policy, _ := httptransport.NewPolicy(nil)
	f.service, err = management.New(s, policy, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewUnstartedServer(nil)
	f.config = Config{OAuth: OAuthConfig{Issuer: f.actor.Issuer, Resource: "http://" + f.server.Listener.Addr().String() + "/mcp", AllowInsecureLoopback: true}}
	verify := func(ctx context.Context, raw string, r *http.Request) (*auth.TokenInfo, error) {
		if raw == "invalid" {
			return nil, auth.ErrInvalidToken
		}
		actor := f.actor
		scopes := []string{Scope}
		expires := time.Now().Add(time.Hour)
		if raw == "wrong-subject" {
			actor.Subject = "ungranted-subject"
		}
		if raw == "no-scope" {
			scopes = nil
		}
		if raw == "expired" {
			expires = time.Now().Add(-time.Hour)
		}
		return &auth.TokenInfo{Scopes: scopes, Expiration: expires, UserID: actor.PrincipalID(), Extra: map[string]any{"identity": actor}}, nil
	}
	handler, err := NewHandler(f.config, f.service, verify)
	if err != nil {
		t.Fatal(err)
	}
	f.server.Config.Handler = handler
	f.server.Start()
	t.Cleanup(f.server.Close)
	return f
}

func (f *httpFixture) rest(t *testing.T, op string, body []byte, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+"/v1/manage/"+op, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
func encode(value any) []byte { raw, _ := json.Marshal(value); return raw }

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}

func TestRESTAndActualSDKMCPParityAndCurrentToolPermissions(t *testing.T) {
	f := setupHTTP(t)
	config := domain.Configuration{Source: domain.Source{ID: "source", Revision: 1, Enabled: true, Auth: domain.SourceAuth{Kind: domain.AuthBearer, Secret: domain.SecretRef{Name: "inbound", Version: "1"}}}}
	resp := f.rest(t, "config_put", encode(management.Request[management.PutInput]{SourceID: "source", ExpectedRevision: new(domain.Revision(0)), Input: management.PutInput{Configuration: config}}), "allowed")
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("put: %d %s", resp.StatusCode, raw)
	}
	resp = f.rest(t, "config_activate", encode(management.Request[management.ActivateInput]{SourceID: "source", ExpectedRevision: new(domain.Revision(0)), Input: management.ActivateInput{DraftRevision: 1}}), "allowed")
	if resp.StatusCode != 200 {
		t.Fatalf("activate: %d", resp.StatusCode)
	}
	request := management.Request[management.EmptyInput]{SourceID: "source", Input: management.EmptyInput{}}
	resp = f.rest(t, "config_get", encode(request), "allowed")
	restBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("get: %d %s", resp.StatusCode, restBody)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: f.server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{http.DefaultTransport, "allowed"}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Contains(names, "config_get") || slices.Contains(names, "deliveries_retry") || slices.Contains(names, "secret_put") || slices.Contains(names, "grant_put") {
		t.Fatalf("tools=%v", names)
	}
	discovery, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "sources_list", Arguments: map[string]any{"input": map[string]any{}}})
	if err != nil || discovery.IsError || !bytes.Contains(encode(discovery.StructuredContent), []byte(`"source"`)) {
		t.Fatalf("initial discovery=%+v err=%v", discovery, err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "config_get", Arguments: request})
	if err != nil || result.IsError {
		t.Fatalf("MCP get: %+v %v", result, err)
	}
	var restValue, mcpValue any
	if json.Unmarshal(restBody, &restValue) != nil {
		t.Fatal("invalid REST JSON")
	}
	mcpJSON, _ := json.Marshal(result.StructuredContent)
	json.Unmarshal(mcpJSON, &mcpValue)
	if !bytes.Equal(encode(restValue), encode(mcpValue)) {
		t.Fatalf("REST=%s MCP=%s", restBody, mcpJSON)
	}
	// A known but unlisted operation returns the same stable error as REST.
	deniedInput := management.Request[management.RetryInput]{SourceID: "source", Input: management.RetryInput{DeliveryID: "d", IdempotencyKey: "synthetic"}}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "deliveries_retry", Arguments: deniedInput})
	if err != nil || !result.IsError || !bytes.Contains(encode(result.StructuredContent), []byte(`"code":"forbidden"`)) {
		t.Fatalf("denied MCP call=%+v err=%v", result, err)
	}
	resp = f.rest(t, "deliveries_retry", encode(deniedInput), "allowed")
	if resp.StatusCode != 403 {
		t.Fatalf("denied REST status=%d", resp.StatusCode)
	}
	// The existing connection and its earlier listing confer no lasting grant.
	f.grant.Actions = []domain.Action{domain.ActionReadEvents}
	if err := f.store.ProvisionGrant(t.Context(), f.actor.PrincipalID(), f.grant); err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "config_get", Arguments: request})
	if err != nil || !result.IsError {
		t.Fatalf("revoked action accepted: %+v %v", result, err)
	}
	listed, err = session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "config_get" {
			t.Fatal("revoked action still advertised")
		}
	}
}

func TestAuthChallengesOriginsMetadataAndNoInitializeBypass(t *testing.T) {
	f := setupHTTP(t)
	for _, path := range []string{"/mcp", "/v1/manage/config_get"} {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+path, strings.NewReader(`{"source_id":"source","input":{}}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		challenge := resp.Header.Get("WWW-Authenticate")
		if resp.StatusCode != 401 || !strings.Contains(challenge, f.server.URL+"/.well-known/oauth-protected-resource") || !strings.Contains(challenge, Scope) {
			t.Fatalf("challenge %s status=%d header=%q", path, resp.StatusCode, challenge)
		}
	}
	for _, test := range []struct {
		token  string
		status int
	}{{"invalid", 401}, {"expired", 401}, {"no-scope", 403}, {"wrong-subject", 403}} {
		for _, method := range []string{"initialize", "tools/list"} {
			req, _ := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{}}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+test.token)
			resp, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != test.status {
				t.Fatalf("%s %s status=%d want=%d", test.token, method, resp.StatusCode, test.status)
			}
		}
	}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+"/v1/manage/health", strings.NewReader(`{"source_id":"","input":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer allowed")
	req.Header.Set("Origin", "https://untrusted.example")
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("Origin accepted: %d", resp.StatusCode)
	}
	req, _ = http.NewRequestWithContext(t.Context(), "GET", f.server.URL+"/.well-known/oauth-protected-resource", nil)
	req.Host = "untrusted-host.example"
	resp, err = f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(raw, []byte(f.config.OAuth.Resource)) || bytes.Contains(raw, []byte("untrusted-host")) {
		t.Fatalf("metadata derived from untrusted Host: %d %s", resp.StatusCode, raw)
	}
	f.grant.ExpiresAt = new(time.Now().Add(-time.Minute))
	if err := f.store.ProvisionGrant(t.Context(), f.actor.PrincipalID(), f.grant); err != nil {
		t.Fatal(err)
	}
	resp = f.rest(t, "health", []byte(`{"input":{}}`), "allowed")
	if resp.StatusCode != 403 {
		t.Fatalf("expired grant health=%d", resp.StatusCode)
	}
}

func TestBearerFailuresReturnSafeJSONAndOAuthChallenges(t *testing.T) {
	f := setupHTTP(t)
	for _, path := range []string{"/v1/manage/config_get", "/mcp"} {
		for _, test := range []struct {
			name, token, code, message string
			status                     int
		}{
			{"missing", "", "unauthenticated", "A valid bearer access token is required", http.StatusUnauthorized},
			{"invalid", "invalid", "unauthenticated", "A valid bearer access token is required", http.StatusUnauthorized},
			{"expired", "expired", "unauthenticated", "A valid bearer access token is required", http.StatusUnauthorized},
			{"insufficient scope", "no-scope", "forbidden", "Required OAuth scope is missing", http.StatusForbidden},
		} {
			t.Run(path+"/"+test.name, func(t *testing.T) {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+path, strings.NewReader(`{"source_id":"source","input":{}}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				if test.token != "" {
					req.Header.Set("Authorization", "Bearer "+test.token)
				}
				resp, err := f.server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != test.status || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("status=%d headers=%v", resp.StatusCode, resp.Header)
				}
				var public management.Error
				decoder := json.NewDecoder(resp.Body)
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&public); err != nil {
					t.Fatalf("invalid JSON error: %v", err)
				}
				if public.Code != test.code || public.Message != test.message || decoder.Decode(new(any)) != io.EOF {
					t.Fatalf("error=%+v", public)
				}
				challenge := resp.Header.Get("WWW-Authenticate")
				if !strings.Contains(challenge, `resource_metadata="`+f.server.URL+`/.well-known/oauth-protected-resource"`) || !strings.Contains(challenge, `scope="`+Scope+`"`) {
					t.Fatalf("missing pinned OAuth challenge: %q", challenge)
				}
			})
		}
	}
}
