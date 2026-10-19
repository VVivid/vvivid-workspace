package web_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
	"agentshoppinglist/internal/web"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const allScopes = identity.Read + " " + identity.ProductsWrite + " " + identity.ListsWrite + " " + identity.PurchasesWrite + " " + identity.PreferencesWrite

func meta(key string) shopping.WriteMeta {
	return shopping.WriteMeta{RequestID: "request-" + key, IdempotencyKey: key}
}
func setup(t *testing.T) (*testfixture.Identity, *shopping.Service, identity.Principal, sqlite.Product, sqlite.ListView) {
	t.Helper()
	f := testfixture.New(t)
	f.Enroll(t, "alice")
	f.Enroll(t, "bob")
	service := shopping.New(f.Store)
	p := f.Principal(t, "alice", allScopes)
	product, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("polar"), Product: testfixture.Polar()})
	if err != nil {
		t.Fatal(err)
	}
	list, err := service.SaveList(t.Context(), p, shopping.SaveListInput{WriteMeta: meta("list"), Title: "Walmart", Retailer: new("Walmart"), MakeActive: true})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("add"), ListID: list.Record.List.ID, ExpectedRevision: 1, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.Record.ID, Quantity: new("1"), Merge: "increment"}}})
	if err != nil {
		t.Fatal(err)
	}
	return f, service, p, product.Record, added.Record
}
func TestDomainAtomicityReplayVariantsSourcesAndOwnership(t *testing.T) {
	f, service, p, product, list := setup(t)
	if product.Category != "drinks" || len(product.Media) != 1 || product.Listings[0].PriceMinor != nil || product.Listings[0].ObservedAt != nil || product.Nutrition.Facts[2].Value != nil {
		t.Fatal("fixture lost exact category, photo or unknown fields")
	}
	bob := f.Principal(t, "bob", allScopes)
	if _, err := service.GetProduct(t.Context(), bob, product.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	if _, err := service.GetList(t.Context(), bob, shopping.ListInput{ListID: list.List.ID}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign list: %v", err)
	}
	if _, err := service.ArchiveProduct(t.Context(), bob, shopping.ArchiveProductInput{WriteMeta: meta("foreign-product"), ProductID: product.ID, ExpectedRevision: 1, Archived: true}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign product write: %v", err)
	}
	foreign, err := service.SaveList(t.Context(), bob, shopping.SaveListInput{WriteMeta: meta("bob-list"), Title: "Bob", MakeActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateItems(t.Context(), bob, shopping.UpdateItemsInput{WriteMeta: meta("foreign-add"), ListID: foreign.Record.List.ID, ExpectedRevision: 1, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("1"), Merge: "increment"}}}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign add: %v", err)
	}
	writeOnly := f.Principal(t, "alice", identity.ProductsWrite)
	if _, err := service.GetProduct(t.Context(), writeOnly, product.ID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("write scope granted read: %v", err)
	}
	browserCookie := f.Cookie(t, "alice")
	browser, _ := f.BrowserPrincipal(t, browserCookie)
	if _, err := service.SaveProduct(t.Context(), browser, shopping.SaveProductInput{WriteMeta: meta("browser-product"), Product: testfixture.Polar()}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("browser product write: %v", err)
	}
	variant := testfixture.Polar()
	variant.Flavor = "Lime"
	variant.PackageCount = new(int64(12))
	variant.SourceKey = new("walmart:lime12")
	saved, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("variant"), Product: variant})
	if err != nil || saved.Record.ID == product.ID {
		t.Fatalf("variant merged by name: %v", err)
	}
	if _, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("source-collision"), Product: variant}); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("stable source collision: %v", err)
	}
	for _, badURL := range []string{"javascript:alert(1)", "http://example.com/photo", "https://localhost/photo", "https://127.0.0.1/photo", "https://169.254.169.254/latest", "https://user:password@example.com/photo", "https://example.com/#fragment"} {
		bad := testfixture.Polar()
		bad.Media[0].ImageURL = badURL
		if _, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("invalid-url"), Product: bad}); err == nil {
			t.Fatalf("unsafe URL accepted %s", badURL)
		}
	}
	input := shopping.UpdateItemsInput{WriteMeta: meta("increment"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("0.25"), Merge: "increment"}}}
	once, err := service.UpdateItems(t.Context(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	input.RequestID = "retry-new-correlation"
	again, err := service.UpdateItems(t.Context(), p, input)
	if err != nil || !again.Replayed || again.Record.Items[0].Quantity != "1.25" {
		t.Fatalf("replay changed quantity: %#v %v", again, err)
	}
	changed := input
	changed.Operations = append([]sqlite.ItemOperation(nil), input.Operations...)
	changed.Operations[0].Quantity = new("2")
	if _, err := service.UpdateItems(t.Context(), p, changed); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatalf("changed retry accepted: %v", err)
	}
	item := once.Record.Items[0]
	failed := shopping.UpdateItemsInput{WriteMeta: meta("rollback"), ListID: list.List.ID, ExpectedRevision: once.Record.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "set_picked_up", ItemID: item.ID, ExpectedRevision: item.Revision, PickedUp: new(true)}, {Kind: "remove", ItemID: "foreign-missing", ExpectedRevision: 1}}}
	if _, err := service.UpdateItems(t.Context(), p, failed); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("batch failure: %v", err)
	}
	after, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || after.List.Revision != once.Record.List.Revision || after.Items[0].PickedUp {
		t.Fatalf("batch partially applied %#v %v", after, err)
	}
	failed.Operations = failed.Operations[:1]
	checked, err := service.UpdateItems(t.Context(), p, failed)
	if err != nil {
		t.Fatalf("failed key reserved: %v", err)
	}
	checkedItem := checked.Record.Items[0]
	removed, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("remove"), ListID: list.List.ID, ExpectedRevision: checked.Record.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "remove", ItemID: item.ID, ExpectedRevision: checkedItem.Revision}}})
	if err != nil || len(removed.Record.Items) != 0 {
		t.Fatalf("remove %v", err)
	}
	retained, err := service.GetList(t.Context(), p, shopping.ListInput{IncludeRemoved: true})
	if err != nil || len(retained.Items) != 1 || !retained.Items[0].PickedUp || retained.Items[0].RemovedAt == nil {
		t.Fatalf("picked-up evidence lost: %#v %v", retained, err)
	}
	restored, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("restore"), ListID: list.List.ID, ExpectedRevision: removed.Record.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "restore", ItemID: item.ID, ExpectedRevision: retained.Items[0].Revision}}})
	if err != nil || !restored.Record.Items[0].PickedUp {
		t.Fatalf("restore lost picked-up intent: %v", err)
	}
	activity, err := service.GetActivity(t.Context(), p, shopping.ActivityInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(activity)
	if !bytes.Contains(raw, []byte(`"picked_up":true`)) || bytes.Contains(raw, []byte("foreign-missing")) {
		t.Fatal("activity lost checkoff or included rolled back mutation")
	}
	page, err := service.SearchProducts(t.Context(), p, shopping.SearchInput{Limit: 1})
	if err != nil || len(page.Products) != 1 || page.NextCursor == "" {
		t.Fatalf("pagination first: %#v %v", page, err)
	}
	next, err := service.SearchProducts(t.Context(), p, shopping.SearchInput{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Products) != 1 || next.Products[0].ID == page.Products[0].ID {
		t.Fatalf("pagination next: %#v %v", next, err)
	}
	if err := f.Store.DisableAccount(t.Context(), p.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetList(t.Context(), p, shopping.ListInput{}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("cached principal survived disable: %v", err)
	}
}

func newHandler(t *testing.T, f *testfixture.Identity, service *shopping.Service) http.Handler {
	t.Helper()
	h, err := web.NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), f.Manager, service)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func apiRequest(handler http.Handler, cookie *http.Cookie, csrf, method, path, key string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(method, testfixture.Origin+path, bytes.NewReader(raw))
	if cookie != nil {
		request.AddCookie(cookie)
	}
	request.Header.Set("Origin", testfixture.Origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+a.token)
	copy.Host = "shopping.example"
	return a.base.RoundTrip(copy)
}
func mcpClient(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "integration-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: url + "/mcp", HTTPClient: &http.Client{Transport: authTransport{base: http.DefaultTransport, token: token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
func call(t *testing.T, session *sdk.ClientSession, name string, input any) *sdk.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestBrowserAndOfficialMCPProtocolShareConcurrencyRules(t *testing.T) {
	f, service, p, product, list := setup(t)
	handler := newHandler(t, f, service)
	server := httptest.NewServer(handler)
	defer server.Close()
	cookie := f.Cookie(t, "alice")
	_, csrf := f.BrowserPrincipal(t, cookie)
	browserGet := apiRequest(handler, cookie, csrf, "GET", "/api/v1/lists/active", "", nil)
	if browserGet.Code != 200 || !bytes.Contains(browserGet.Body.Bytes(), []byte(product.Name)) {
		t.Fatalf("browser list: %d %s", browserGet.Code, browserGet.Body)
	}
	session := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 16 {
		t.Fatalf("got %d tools", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.OutputSchema == nil || tool.InputSchema == nil {
			t.Fatalf("untyped tool %s", tool.Name)
		}
	}
	contextResult := call(t, session, "get_context", map[string]any{})
	if contextResult.IsError {
		t.Fatalf("context: %#v", contextResult)
	}
	productResult := call(t, session, "get_product", map[string]any{"product_id": product.ID})
	if productResult.IsError || productResult.StructuredContent == nil {
		t.Fatalf("product details: %#v", productResult)
	}
	denied := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), identity.ListsWrite))
	if result := call(t, denied, "get_list", shopping.ListInput{}); !result.IsError {
		t.Fatal("MCP write implied read")
	}
	bob := mcpClient(t, server.URL, f.Bearer("bob", "shopping-chatgpt", f.Manager.MCPAudience(), allScopes))
	if result := call(t, bob, "get_product", map[string]any{"product_id": product.ID}); !result.IsError {
		t.Fatal("MCP foreign product read")
	}
	// The browser and MCP race at the same list/item revisions: exactly one wins.
	item := list.Items[0]
	var group sync.WaitGroup
	results := make(chan bool, 2)
	group.Go(func() {
		response := apiRequest(handler, cookie, csrf, "PATCH", "/api/v1/lists/"+list.List.ID+"/items/"+item.ID, "browser-race", map[string]any{"expected_list_revision": list.List.Revision, "expected_revision": item.Revision, "picked_up": true})
		results <- response.Code == 200
	})
	group.Go(func() {
		result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "update_list_items", Arguments: shopping.UpdateItemsInput{WriteMeta: meta("mcp-race"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "set_quantity", ItemID: item.ID, ExpectedRevision: item.Revision, Quantity: new("2")}}}})
		results <- err == nil && !result.IsError
	})
	group.Wait()
	close(results)
	wins := 0
	for success := range results {
		if success {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent wins %d", wins)
	}
	current, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || current.List.Revision != list.List.Revision+1 {
		t.Fatalf("race revision: %#v %v", current, err)
	}
	request := shopping.UpdateItemsInput{WriteMeta: meta("mcp-replay"), ListID: list.List.ID, ExpectedRevision: current.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("1"), Merge: "increment"}}}
	first := call(t, session, "update_list_items", request)
	second := call(t, session, "update_list_items", request)
	if first.IsError || second.IsError {
		t.Fatalf("MCP replay: %#v %#v", first, second)
	}
	raw, _ := json.Marshal(second.StructuredContent)
	if !bytes.Contains(raw, []byte(`"replayed":true`)) {
		t.Fatalf("no replay flag: %s", raw)
	}
	// Headers and route permissions are tested through the composed handler.
	missing := apiRequest(handler, nil, "", "GET", "/api/v1/me", "", nil)
	if missing.Code != 401 {
		t.Fatalf("anonymous API status %d", missing.Code)
	}
	csrfFailure := apiRequest(handler, cookie, "wrong", "PATCH", "/api/v1/lists/"+list.List.ID+"/items/"+item.ID, "csrf", map[string]any{})
	if csrfFailure.Code != 403 {
		t.Fatal("CSRF accepted")
	}
	if response := apiRequest(handler, cookie, csrf, "POST", "/api/v1/products", "product-write", testfixture.Polar()); response.Code != 405 {
		t.Fatalf("browser product write route status %d", response.Code)
	}
	for _, token := range []string{"", f.Bearer("alice", "shopping-codex", "https://ssh.example/mcp", identity.Read)} {
		req := httptest.NewRequest("POST", testfixture.Origin+"/mcp", strings.NewReader(`{}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != 401 || !strings.Contains(response.Header().Get("WWW-Authenticate"), "resource_metadata") {
			t.Fatalf("MCP auth status %d", response.Code)
		}
	}
	// Imported strings stay plain data in HTML and never become executable markup.
	edit := product
	edit.Name = `<img src=x onerror=alert(1)>`
	edit.ID = product.ID
	if _, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("escape"), Product: edit, ExpectedRevision: product.Revision}); err != nil {
		t.Fatal(err)
	}
	html := apiRequest(handler, cookie, csrf, "GET", "/", "", nil)
	if html.Code != 200 || bytes.Contains(html.Body.Bytes(), []byte(`<img src=x`)) || !bytes.Contains(html.Body.Bytes(), []byte(`&lt;img src=x`)) {
		t.Fatalf("unsafe template: %d %s", html.Code, html.Body)
	}
}

func TestActiveSelectionRestoreArchiveAndRestartReplay(t *testing.T) {
	f, service, p, product, list := setup(t)
	second, err := service.SaveList(t.Context(), p, shopping.SaveListInput{WriteMeta: meta("second-list"), Title: "Another store"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || active.List.ID != list.List.ID || active.SelectionRevision != 1 {
		t.Fatalf("creation implicitly selected: %#v %v", active, err)
	}
	staleSelection := shopping.SaveListInput{WriteMeta: meta("select-stale"), ListID: second.Record.List.ID, Title: "Another store", ExpectedRevision: 1, MakeActive: true, ExpectedSelectionRevision: 0}
	if _, err := service.SaveList(t.Context(), p, staleSelection); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("stale selection: %v", err)
	}
	unchanged, err := service.GetList(t.Context(), p, shopping.ListInput{ListID: second.Record.List.ID})
	if err != nil || unchanged.List.Revision != 1 {
		t.Fatal("selection conflict partially revised list")
	}
	staleSelection.ExpectedSelectionRevision = 1
	selected, err := service.SaveList(t.Context(), p, staleSelection)
	if err != nil || selected.Record.SelectionRevision != 2 {
		t.Fatalf("select second: %#v %v", selected, err)
	}
	archived, err := service.SaveList(t.Context(), p, shopping.SaveListInput{WriteMeta: meta("archive-selected"), ListID: second.Record.List.ID, Title: "Another store", ExpectedRevision: 2, Archived: true})
	if err != nil || archived.Record.ActiveListID != nil || archived.Record.SelectionRevision != 3 {
		t.Fatalf("archive selection: %#v %v", archived, err)
	}
	if _, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("archived-write"), ListID: second.Record.List.ID, ExpectedRevision: 3, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("1"), Merge: "keep"}}}); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("archived list write: %v", err)
	}
	// Replaying an acknowledged request after reopening SQLite returns the original
	// result even though a later write has advanced the current list revision.
	input := shopping.UpdateItemsInput{WriteMeta: meta("restart-increment"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("2"), Merge: "increment"}}}
	result, err := service.UpdateItems(t.Context(), p, input)
	if err != nil {
		t.Fatal(err)
	}
	item := result.Record.Items[0]
	removed, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("restart-remove"), ListID: list.List.ID, ExpectedRevision: result.Record.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "remove", ItemID: item.ID, ExpectedRevision: item.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	// Adding again creates a distinct item; restoring the original must conflict
	// rather than silently duplicating a live product or losing its old evidence.
	readded, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("readd"), ListID: list.List.ID, ExpectedRevision: removed.Record.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("1"), Merge: "keep"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("restore-collision"), ListID: list.List.ID, ExpectedRevision: readded.Record.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "restore", ItemID: item.ID, ExpectedRevision: item.Revision + 1}}}); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("duplicate restore: %v", err)
	}
	if err := f.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(t.Context(), f.Path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := shopping.New(reopened)
	replay, err := restarted.UpdateItems(t.Context(), p, input)
	if err != nil || !replay.Replayed || replay.Record.Items[0].Quantity != "3" || replay.Record.List.Revision != result.Record.List.Revision {
		t.Fatalf("restart replay: %#v %v", replay, err)
	}
	current, err := restarted.GetList(t.Context(), p, shopping.ListInput{ListID: list.List.ID})
	if err != nil || current.List.Revision != readded.Record.List.Revision || current.Items[0].ID == item.ID || current.Items[0].Quantity != "1" {
		t.Fatalf("replay changed latest state: %#v %v", current, err)
	}
}

func resultRecord[T any](t *testing.T, result *sdk.CallToolResult) T {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool execution failed: %#v", result.Content)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAllImplementedToolsThroughSDKAndBoundedSchemas(t *testing.T) {
	f, service, _, product, list := setup(t)
	server := httptest.NewServer(newHandler(t, f, service))
	defer server.Close()
	session := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tools.CacheScope != "private" || tools.TTLMs != 0 {
		t.Fatalf("personal MCP cache policy: %#v", tools.Cacheable)
	}
	context := resultRecord[shopping.Context](t, call(t, session, "get_context", map[string]any{}))
	if context.ActiveListID == nil || *context.ActiveListID != list.List.ID {
		t.Fatal("context selection mismatch")
	}
	createdInput := testfixture.Polar()
	createdInput.Flavor = "Lime"
	createdInput.SourceKey = new("fixture:lime")
	created := resultRecord[shopping.WriteResult[sqlite.Product]](t, call(t, session, "save_product", shopping.SaveProductInput{WriteMeta: meta("sdk-product"), Product: createdInput}))
	archived := resultRecord[shopping.WriteResult[sqlite.Product]](t, call(t, session, "archive_product", shopping.ArchiveProductInput{WriteMeta: meta("sdk-archive"), ProductID: created.Record.ID, ExpectedRevision: 1, Archived: true}))
	if !archived.Record.Archived {
		t.Fatal("archive tool did not persist state")
	}
	catalog := resultRecord[shopping.ProductPage](t, call(t, session, "search_products", shopping.SearchInput{Query: "Polar", Limit: 1}))
	if len(catalog.Products) != 1 || catalog.Products[0].ID != product.ID {
		t.Fatal("catalog archive/search filter mismatch")
	}
	resultRecord[sqlite.Product](t, call(t, session, "get_product", map[string]any{"product_id": product.ID}))
	alternate := resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, session, "save_list", shopping.SaveListInput{WriteMeta: meta("sdk-list"), Title: "Weekend", MakeActive: true, ExpectedSelectionRevision: 1}))
	fetched := resultRecord[sqlite.ListView](t, call(t, session, "get_list", shopping.ListInput{}))
	if fetched.List.ID != alternate.Record.List.ID {
		t.Fatal("save_list selection not visible")
	}
	updated := resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, session, "update_list_items", shopping.UpdateItemsInput{WriteMeta: meta("sdk-add"), ListID: fetched.List.ID, ExpectedRevision: 1, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("0.125"), Merge: "keep"}}}))
	if updated.Record.Items[0].Quantity != "0.125" {
		t.Fatal("SDK decimal quantity changed")
	}
	preferences := context.Preferences
	preferences.PreferredRetailer = "Target"
	changed := resultRecord[shopping.WriteResult[sqlite.Preferences]](t, call(t, session, "update_preferences", shopping.PreferencesInput{WriteMeta: meta("sdk-prefs"), Preferences: preferences, ExpectedRevision: 1}))
	if changed.Record.PreferredRetailer != "Target" || changed.Record.Revision != 2 {
		t.Fatal("preferences tool state mismatch")
	}
	activity := resultRecord[shopping.ActivityPage](t, call(t, session, "get_activity", shopping.ActivityInput{Limit: 1}))
	if len(activity.Events) != 1 || activity.NextCursor == "" {
		t.Fatal("activity cursor missing")
	}
	next := resultRecord[shopping.ActivityPage](t, call(t, session, "get_activity", shopping.ActivityInput{Limit: 1, Cursor: activity.NextCursor}))
	if next.Events[0].ID == activity.Events[0].ID {
		t.Fatal("activity cursor repeated event")
	}
	for _, attempt := range []struct {
		name string
		args any
	}{
		{"get_context", map[string]any{"owner_id": "bob"}},
		{"get_product", map[string]any{"product_id": strings.Repeat("x", 129)}},
		{"search_products", map[string]any{"limit": 51, "include_archived": false}},
		{"get_activity", shopping.ActivityInput{Limit: 11}},
		{"update_list_items", shopping.UpdateItemsInput{WriteMeta: meta("oversized-batch"), ListID: fetched.List.ID, ExpectedRevision: 2, Operations: make([]sqlite.ItemOperation, 51)}},
	} {
		if result := call(t, session, attempt.name, attempt.args); !result.IsError {
			t.Fatalf("schema accepted invalid %s input", attempt.name)
		}
	}
}
