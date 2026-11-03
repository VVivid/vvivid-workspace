package web_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
	"github.com/go-jose/go-jose/v4"
)

func TestGatewayMCPPreservesExistingOwnershipBrowserWritesAndReplay(t *testing.T) {
	fixture, _, original, product, list := setup(t)
	// Establish data and a browser session in direct mode before switching MCP
	// trust. The mode change must not create another account or replace a session.
	cookie := fixture.Cookie(t, "alice")
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
	settings["SHOPPING_MCP_AUTH_MODE"] = "gateway"
	settings["SHOPPING_MCP_GATEWAY_ISSUER"] = "https://mcp.rd.lab.vvivid.dev"
	settings["SHOPPING_MCP_GATEWAY_JWKS_FILE"] = path
	settings["SHOPPING_MCP_AUDIENCE"] = "agent-shopping-list-mcp"
	settings["SHOPPING_MCP_CLIENT_IDS"] = "shopping-chatgpt"
	cfg, err := identity.LoadConfig(func(name string) (string, bool) { value, present := settings[name]; return value, present })
	if err != nil {
		t.Fatal(err)
	}
	compose := func() http.Handler {
		t.Helper()
		fixture.Manager, err = identity.New(t.Context(), *cfg, fixture.Store)
		if err != nil {
			t.Fatal(err)
		}
		return newHandler(t, fixture, shopping.New(fixture.Store))
	}
	handler := compose()
	browser, csrf := fixture.BrowserPrincipal(t, cookie)
	if browser.Owner() != original.Owner() {
		t.Fatal("gateway mode changed existing browser ownership")
	}
	token := func(subject, scope string) string {
		t.Helper()
		now := time.Now().Unix()
		claims := map[string]any{
			"iss":             cfg.GatewayIssuer,
			"aud":             []string{cfg.MCPAudience},
			"sub":             subject,
			"identity_issuer": cfg.Issuer,
			"client_id":       "shopping-chatgpt",
			"scope":           scope,
			"iat":             now,
			"nbf":             now,
			"exp":             now + 60,
			"jti":             rand.Text(),
		}
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
	alice := mcpClient(t, server.URL, token("alice", allScopes))
	tools, err := alice.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 16 || tools.CacheScope != "private" || tools.TTLMs != 0 {
		t.Fatalf("gateway tools/cache policy: %v", err)
	}
	fetched := resultRecord[sqlite.Product](t, call(t, alice, "get_product", map[string]any{"product_id": product.ID}))
	current := resultRecord[sqlite.ListView](t, call(t, alice, "get_list", shopping.ListInput{}))
	if fetched.ID != product.ID || current.List.ID != list.List.ID || len(current.Items) != 1 {
		t.Fatal("gateway lost existing product/list ownership")
	}
	input := shopping.UpdateItemsInput{WriteMeta: meta("gateway-increment"), ListID: list.List.ID, ExpectedRevision: list.List.Revision,
		Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("2"), Merge: "increment"}}}
	first := resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, alice, "update_list_items", input))
	if first.Replayed || first.Record.Items[0].Quantity != "3" || first.Record.List.Revision != list.List.Revision+1 {
		t.Fatal("gateway list mutation changed quantity/revision semantics")
	}
	if retry := resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, alice, "update_list_items", input)); !retry.Replayed || retry.Record.Items[0].Quantity != "3" {
		t.Fatal("gateway retry duplicated a mutation")
	}
	stale := input
	stale.WriteMeta = meta("gateway-stale")
	if !call(t, alice, "update_list_items", stale).IsError {
		t.Fatal("gateway accepted a stale list revision")
	}
	readOnly := mcpClient(t, server.URL, token("alice", identity.Read))
	if !call(t, readOnly, "update_list_items", input).IsError {
		t.Fatal("gateway read scope wrote list items")
	}
	writeOnly := mcpClient(t, server.URL, token("alice", identity.ListsWrite))
	if !call(t, writeOnly, "get_list", shopping.ListInput{}).IsError {
		t.Fatal("gateway write scope implied reading")
	}
	bob := mcpClient(t, server.URL, token("bob", allScopes))
	if !call(t, bob, "get_product", map[string]any{"product_id": product.ID}).IsError || !call(t, bob, "update_list_items", input).IsError {
		t.Fatal("gateway exposed another owner's records")
	}
	// The original browser session sees gateway edits and uses its normal CSRF,
	// revision and idempotency rules when writing back to the same list.
	item := first.Record.Items[0]
	response := apiRequest(handler, cookie, csrf, "PATCH", "/api/v1/lists/"+list.List.ID+"/items/"+item.ID, "gateway-browser-check",
		map[string]any{"expected_list_revision": first.Record.List.Revision, "expected_revision": item.Revision, "picked_up": true})
	if response.Code != http.StatusOK {
		t.Fatalf("browser mutation in gateway mode: %d", response.Code)
	}
	current = resultRecord[sqlite.ListView](t, call(t, alice, "get_list", shopping.ListInput{}))
	if !current.Items[0].PickedUp || current.Items[0].Quantity != "3" {
		t.Fatal("MCP did not see the original browser's list change")
	}
	for _, rejected := range []string{fixture.Bearer("alice", "shopping-chatgpt", cfg.MCPAudience, allScopes), token("unknown", allScopes), ""} {
		request := httptest.NewRequest("POST", testfixture.Origin+"/mcp", nil)
		request.Header.Set("Authorization", "Bearer "+rejected)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("gateway MCP accepted an external/unregistered identity or advertised direct OAuth")
		}
	}
	request := httptest.NewRequest("GET", testfixture.Origin+"/api/v1/lists/active", nil)
	request.Header.Set("Authorization", "Bearer "+token("alice", allScopes))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("gateway JWT authenticated a browser API route")
	}
	server.Close()
	fixture.Reopen(t)
	restartedHandler := compose()
	restarted := httptest.NewServer(restartedHandler)
	t.Cleanup(restarted.Close)
	replay := resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, mcpClient(t, restarted.URL, token("alice", allScopes)), "update_list_items", input))
	if !replay.Replayed || replay.Record.Items[0].Quantity != "3" || replay.Record.Items[0].PickedUp {
		t.Fatal("gateway restart changed the original persisted retry result")
	}
	response = apiRequest(restartedHandler, cookie, csrf, "GET", "/api/v1/lists/active", "", nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"picked_up":true`)) {
		t.Fatal("restart lost the original browser session or later list state")
	}
}
