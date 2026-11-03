package web_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
	"agentnewsfeed/internal/thumbnail"
	"agentnewsfeed/internal/web"
	"github.com/go-jose/go-jose/v4"
)

func TestGatewayMCPPreservesToolsOwnershipScopesAndBrowserRecords(t *testing.T) {
	fixture := testfixture.New(t)
	fixture.Enroll(t, "alice")
	fixture.Enroll(t, "bob")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "gateway", Algorithm: "RS256", Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public.jwks.json")
	if err := os.WriteFile(path, public, 0o644); err != nil {
		t.Fatal(err)
	}
	settings := fixture.Environment()
	settings["NEWSFEED_MCP_AUTH_MODE"] = "gateway"
	settings["NEWSFEED_MCP_GATEWAY_ISSUER"] = "https://mcp.rd.lab.vvivid.dev"
	settings["NEWSFEED_MCP_GATEWAY_JWKS_FILE"] = path
	settings["NEWSFEED_MCP_AUDIENCE"] = "agent-newsfeed-mcp"
	settings["NEWSFEED_MCP_CLIENT_IDS"] = "newsfeed-chatgpt"
	cfg, err := identity.LoadConfig(func(name string) (string, bool) { value, present := settings[name]; return value, present })
	if err != nil {
		t.Fatal(err)
	}
	auth, err := identity.New(t.Context(), *cfg, fixture.Store)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := thumbnail.NewPolicy("")
	service := feed.New(fixture.Store, policy)
	handler, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), auth, service, thumbnail.NewProxy(policy))
	if err != nil {
		t.Fatal(err)
	}
	token := func(subject, scope string) string {
		t.Helper()
		now := time.Now().Unix()
		claims := map[string]any{"iss": cfg.GatewayIssuer, "aud": []string{cfg.MCPAudience}, "sub": subject,
			"identity_issuer": cfg.Issuer, "client_id": "newsfeed-chatgpt", "scope": scope,
			"iat": now, "nbf": now, "exp": now + 60, "jti": rand.Text()}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("mcp-gateway+jwt").WithHeader("kid", "gateway"))
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		object, err := signer.Sign(payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := object.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	alice := client(t, server.URL, token("alice", allScopes))
	tools, err := alice.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("gateway tools: %v", err)
	}
	input := feed.AddInput{IdempotencyKey: "gateway-add", Article: feed.Content{URL: "https://example.com/gateway-story", Title: "Gateway story", Summary: "A sourced gateway test."}}
	first := decode[sqlite.MutationResult](t, call(t, alice, "add_article", input))
	if !first.Created || first.Article.Revision != 1 {
		t.Fatal("gateway add failed")
	}
	if retry := decode[sqlite.MutationResult](t, call(t, alice, "add_article", input)); retry != first {
		t.Fatal("gateway idempotent retry changed result")
	}
	input.Article.Title = "Updated through gateway"
	update := feed.UpdateInput{IdempotencyKey: "gateway-update", ArticleID: first.Article.ID, ExpectedRevision: 1, Article: input.Article}
	updated := decode[sqlite.MutationResult](t, call(t, alice, "update_article", update))
	if updated.Article.Revision != 2 || updated.Article.Title != input.Article.Title {
		t.Fatal("gateway revision update failed")
	}
	update.IdempotencyKey = "gateway-stale"
	if !call(t, alice, "update_article", update).IsError {
		t.Fatal("stale gateway update accepted")
	}
	readOnly := client(t, server.URL, token("alice", identity.Read))
	if !call(t, readOnly, "add_article", input).IsError {
		t.Fatal("gateway read scope wrote an article")
	}
	bob := client(t, server.URL, token("bob", allScopes))
	if page := decode[feed.Page](t, call(t, bob, "list_articles", feed.ListInput{})); len(page.Articles) != 0 {
		t.Fatal("gateway exposed another owner's records")
	}
	update.ExpectedRevision = 2
	if !call(t, bob, "update_article", update).IsError {
		t.Fatal("gateway changed another owner's record")
	}
	cookie := fixture.CookieWithScopes(t, "alice", allScopes)
	response := request(handler, "GET", "/api/v1/articles/"+first.Article.ID, cookie, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), updated.Article.Title) {
		t.Fatal("direct browser session did not see gateway-owned article")
	}
	for _, rejected := range []string{fixture.Bearer("alice", "newsfeed-chatgpt", cfg.MCPAudience, allScopes), token("unknown", allScopes), ""} {
		response := request(handler, "POST", "/mcp", nil, rejected)
		if response.Code != 401 || response.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("gateway-mode MCP accepted external/unregistered identity or advertised direct OAuth")
		}
	}
}
