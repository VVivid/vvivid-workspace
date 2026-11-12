package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/testoidc"
)

type jwksNetworkFailure struct{ failing atomic.Bool }

func (f *jwksNetworkFailure) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/keys" && f.failing.Load() {
		return nil, fmt.Errorf("synthetic signing-key network outage")
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestJWKSOutageKeepsRotatedGrantAcrossRestart(t *testing.T) {
	for _, failure := range []string{"network", "503"} {
		for _, keyChange := range []string{"cold cache", "access rotation", "ID rotation"} {
			for _, extended := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/expired=%t", failure, keyChange, extended), func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "auth.sqlite")
					m, p, h := setupAt(t, path)
					network := &jwksNetworkFailure{}
					keyClient := *m.client
					keyClient.Transport = network
					m.keys = providerKeys(&keyClient, m.metadata.JWKS)
					cookie := signIn(t, m, p, h, "work.example.test")
					_, initial, err := m.readSession(t.Context(), hash(cookie.Value), "work.example.test")
					if err != nil {
						t.Fatal(err)
					}
					switch keyChange {
					case "cold cache":
						m.keys = providerKeys(&keyClient, m.metadata.JWKS)
					case "access rotation":
						p.RotateSigningKey(t)
					case "ID rotation":
						p.RotateIDSigningKey(t)
					}
					if failure == "network" {
						network.failing.Store(true)
					} else {
						p.JWKSMode.Store(503)
					}
					p.Clock.Add(301)
					assertRetryable := func(handler http.Handler) {
						t.Helper()
						res := request(handler, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, "")
						if res.Code != 503 {
							t.Fatalf("JWKS outage returned %d: %s", res.Code, res.Body.String())
						}
						for _, c := range res.Result().Cookies() {
							if c.Name == sessionCookie && c.MaxAge < 0 {
								t.Fatal("JWKS outage cleared the browser session")
							}
						}
						if p.Refreshes.Load() != 1 || p.Revocations.Load() != 0 {
							t.Fatalf("pending grant replayed/revoked: refreshes=%d revocations=%d", p.Refreshes.Load(), p.Revocations.Load())
						}
					}
					assertRetryable(h)
					durable, pending, err := m.readSession(t.Context(), hash(cookie.Value), "work.example.test")
					if err != nil || pending.Pending == nil || pending.Refresh == initial.Refresh {
						t.Fatalf("rotated replacement was not retained: %v", err)
					}
					if bytes.Contains(durable.Cipher, []byte(pending.Refresh)) || bytes.Contains(durable.Cipher, []byte(pending.Pending.Access)) {
						t.Fatal("pending tokens were stored without encryption")
					}
					assertRetryable(h)
					if extended {
						p.Clock.Add(601)
						assertRetryable(h)
					}
					// Close and reopen the actual SQLite file, then create a new manager
					// with an empty key cache as a replacement process would do.
					if err = m.store.Close(); err != nil {
						t.Fatal(err)
					}
					reopened, err := store.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { reopened.Close() })
					m, err = New(t.Context(), m.cfg, reopened)
					if err != nil {
						t.Fatal(err)
					}
					m.now = p.Now
					m.keys = providerKeys(&keyClient, m.metadata.JWKS)
					h = browserHandler(m)
					assertRetryable(h)
					network.failing.Store(false)
					p.JWKSMode.Store(0)
					res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, "")
					if res.Code != 204 {
						t.Fatalf("JWKS recovery required login: %d %s", res.Code, res.Body.String())
					}
					wantRefreshes := int64(1)
					if extended {
						wantRefreshes++
					}
					if p.Refreshes.Load() != wantRefreshes {
						t.Fatalf("refreshes=%d, want %d", p.Refreshes.Load(), wantRefreshes)
					}
					_, recovered, err := m.readSession(t.Context(), hash(cookie.Value), "work.example.test")
					if err != nil || recovered.Pending != nil || recovered.Expiry <= p.Now().Unix() {
						t.Fatalf("recovery did not persist a verified, currently valid grant: %v", err)
					}
				})
			}
		}
	}
}

func TestColdJWKSOutageWithoutRefreshDoesNotClearSession(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	m.keys = providerKeys(m.client, m.metadata.JWKS)
	p.JWKSMode.Store(503)
	res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, "")
	if res.Code != 503 || p.Refreshes.Load() != 0 {
		t.Fatalf("cold cache without refresh: status=%d refreshes=%d", res.Code, p.Refreshes.Load())
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			t.Fatal("authentication collapsed temporary verification failure into login")
		}
	}
	p.JWKSMode.Store(0)
	if res = request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 204 {
		t.Fatalf("cold cache recovery=%d", res.Code)
	}
}

func TestCancelledRefreshRequestStillPersistsRotatedGrant(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	_, initial, err := m.readSession(t.Context(), hash(cookie.Value), "work.example.test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.client.Transport = changedTokenResponse{change: func(*tokenResponse) { cancel() }}
	p.Clock.Add(301)
	r := httptest.NewRequest("GET", "https://work.example.test/protected", nil).WithContext(ctx)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("cancelled request=%d", w.Code)
	}
	_, durable, err := m.readSession(t.Context(), hash(cookie.Value), "work.example.test")
	if err != nil || durable.Pending == nil || durable.Refresh == initial.Refresh || p.Refreshes.Load() != 1 {
		t.Fatalf("request cancellation lost the rotated grant: %v", err)
	}
	if res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 204 || p.Refreshes.Load() != 1 {
		t.Fatalf("cancelled request did not recover: status=%d refreshes=%d", res.Code, p.Refreshes.Load())
	}
}

type changedTokenResponse struct{ change func(*tokenResponse) }

func (c changedTokenResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || r.URL.Path != "/token" || res.StatusCode != 200 {
		return res, err
	}
	defer res.Body.Close()
	var tokens tokenResponse
	if err = json.NewDecoder(res.Body).Decode(&tokens); err != nil {
		return nil, err
	}
	c.change(&tokens)
	raw, err := json.Marshal(tokens)
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	res.ContentLength = int64(len(raw))
	return res, nil
}

func TestInvalidRefreshTokensRemainDefinitiveFailures(t *testing.T) {
	for _, invalid := range []string{"access signature", "access expired at receipt", "access client", "access subject", "access scopes", "ID signature", "ID nonce", "ID audience", "ID subject", "ID expired at receipt"} {
		t.Run(invalid, func(t *testing.T) {
			m, p, h := setup(t)
			cookie := signIn(t, m, p, h, "work.example.test")
			_, state, err := m.readSession(t.Context(), hash(cookie.Value), "work.example.test")
			if err != nil {
				t.Fatal(err)
			}
			forged := testoidc.New(t)
			p.Clock.Add(301)
			m.client.Transport = changedTokenResponse{change: func(tokens *tokenResponse) {
				access := map[string]any{"iss": p.URL(), "sub": state.Subject, "azp": m.cfg.WebClientID, "aud": []string{m.cfg.APIAudience}, "scope": store.Read + " " + store.Write + " offline_access", "typ": "Bearer", "iat": p.Now().Unix(), "exp": p.Now().Unix() + 300}
				id := map[string]any{"iss": p.URL(), "sub": state.Subject, "azp": m.cfg.WebClientID, "aud": []string{m.cfg.WebClientID}, "nonce": state.Nonce, "iat": p.Now().Unix(), "exp": p.Now().Unix() + 300}
				switch invalid {
				case "access signature":
					tokens.Access = forged.Sign(access, "JWT")
				case "access expired at receipt":
					access["exp"] = p.Now().Unix() - 1
					tokens.Access = p.Sign(access, "JWT")
				case "access client":
					access["azp"] = "another-client"
					tokens.Access = p.Sign(access, "JWT")
				case "access subject":
					access["sub"] = "unenrolled-subject"
					tokens.Access = p.Sign(access, "JWT")
				case "access scopes":
					access["scope"] = store.Read
					tokens.Access = p.Sign(access, "JWT")
				case "ID signature":
					tokens.ID = forged.Sign(id, "JWT")
				case "ID nonce":
					id["nonce"] = "wrong-nonce"
					tokens.ID = p.Sign(id, "JWT")
				case "ID audience":
					id["aud"] = []string{m.cfg.WebClientID, "another-audience"}
					tokens.ID = p.Sign(id, "JWT")
				case "ID subject":
					id["sub"] = "unenrolled-subject"
					tokens.ID = p.Sign(id, "JWT")
				case "ID expired at receipt":
					id["exp"] = p.Now().Unix() - 1
					tokens.ID = p.Sign(id, "JWT")
				}
			}}
			res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, "")
			if res.Code != 401 || p.Revocations.Load() != 1 {
				t.Fatalf("invalid token was not a definitive failure: status=%d revocations=%d", res.Code, p.Revocations.Load())
			}
			if _, err = m.store.Session(t.Context(), hash(cookie.Value), "work.example.test", p.Now()); err == nil {
				t.Fatal("invalid rotated grant retained a session")
			}
		})
	}
}

func TestIDVerificationDistinguishesUnavailableKeysFromInvalidTokens(t *testing.T) {
	m, p, _ := setup(t)
	claims := map[string]any{"iss": p.URL(), "sub": "test-subject", "aud": []string{m.cfg.WebClientID}, "azp": m.cfg.WebClientID, "nonce": "synthetic-nonce", "iat": p.Now().Unix(), "exp": p.Now().Unix() + 300}
	raw := p.Sign(claims, "JWT")
	p.JWKSMode.Store(503)
	if err := m.verifyID(t.Context(), raw, "", "test-subject", "synthetic-nonce", true, p.Now()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ID cold-cache JWKS outage=%v", err)
	}
	p.JWKSMode.Store(0)
	if err := m.verifyID(t.Context(), raw, "", "test-subject", "synthetic-nonce", true, p.Now()); err != nil {
		t.Fatalf("ID recovery=%v", err)
	}
	for _, bad := range []string{"issuer", "client", "nonce", "nbf", "at_hash"} {
		t.Run(bad, func(t *testing.T) {
			changed := maps.Clone(claims)
			switch bad {
			case "issuer":
				changed["iss"] = "https://another.example.test"
			case "client":
				changed["azp"] = "another-client"
			case "nonce":
				changed["nonce"] = "wrong"
			case "nbf":
				changed["nbf"] = p.Now().Unix() + 1
			case "at_hash":
				changed["at_hash"] = "wrong"
			}
			if err := m.verifyID(t.Context(), p.Sign(changed, "JWT"), "synthetic-access", "test-subject", "synthetic-nonce", true, p.Now()); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("invalid ID claims=%v", err)
			}
		})
	}
}

func TestDirectMCPJWKSOutageIsRetryable(t *testing.T) {
	m, p, _ := setup(t)
	p.JWKSMode.Store(503)
	token := p.Access("test-subject", "vocab-test", m.cfg.MCPAudience, store.Read, 300)
	r := httptest.NewRequest("POST", "https://work.example.test/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	m.MCP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("direct MCP JWKS failure=%d", w.Code)
	}
}
