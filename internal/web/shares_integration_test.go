package web_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
	"agentshoppinglist/internal/web"
)

func shareInput(list sqlite.ListView, key string) shopping.CreateShareInput {
	items := []sqlite.ShareSelection{}
	for _, item := range list.Items {
		items = append(items, sqlite.ShareSelection{ItemID: item.ID, ExpectedRevision: item.Revision, ExpectedProductRevision: item.Product.Revision})
	}
	return shopping.CreateShareInput{WriteMeta: meta(key), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Items: items}
}

func publicRequest(handler http.Handler, method, path, token, key string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(method, testfixture.Origin+path, bytes.NewReader(raw))
	request.Header.Set("Origin", testfixture.Origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Share-CSRF", token)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func shareDB(t *testing.T, f *testfixture.Identity) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", f.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countShares(t *testing.T, f *testfixture.Identity) int {
	t.Helper()
	var count int
	if err := shareDB(t, f).QueryRowContext(t.Context(), `SELECT count(*) FROM temporary_shares`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestShareSelectedSnapshotOwnerIsolationAndDurableGuestState(t *testing.T) {
	f, service, p, product, list := setup(t)
	otherProduct := testfixture.Polar()
	otherProduct.Name, otherProduct.SourceKey = "Private unselected product", new("private-unselected")
	saved, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("unselected"), Product: otherProduct})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("unselected-add"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: saved.Record.ID, Quantity: new("4"), Merge: "keep"}}})
	if err != nil {
		t.Fatal(err)
	}
	list = added.Record
	input := shareInput(list, "selected-share")
	input.Items = input.Items[:1]
	// Pin the known selected product independently of creation-time row sorting.
	for _, item := range list.Items {
		if item.ProductID == product.ID {
			input.Items[0] = sqlite.ShareSelection{ItemID: item.ID, ExpectedRevision: item.Revision, ExpectedProductRevision: item.Product.Revision}
		}
	}
	cookie := f.Cookie(t, "alice")
	_, csrf := f.BrowserPrincipal(t, cookie)
	var logs bytes.Buffer
	handler, err := web.NewHandler(slog.New(slog.NewTextHandler(&logs, nil)), f.Manager, service)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/lists/" + list.List.ID + "/shares"
	body := map[string]any{"expected_revision": input.ExpectedRevision, "items": input.Items}
	response := apiRequest(handler, cookie, csrf, "POST", path, input.IdempotencyKey, body)
	var created shopping.WriteResult[sqlite.CreatedShare]
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || response.Code != 200 || !sqlite.ValidShareCode(created.Record.Code) {
		t.Fatalf("create: %d %s %v", response.Code, response.Body, err)
	}
	share, err := service.PublicShare(t.Context(), created.Record.Code)
	if err != nil || len(share.Items) != 1 || share.Items[0].Name != product.Name || share.Items[0].PickedUp || *share.Retailer != "Walmart" {
		t.Fatalf("selected snapshot: %#v %v", share, err)
	}
	guestURL := "/s/" + created.Record.Code
	guest := publicRequest(handler, "GET", guestURL, "", "", nil)
	if guest.Code != 200 || !strings.Contains(guest.Body.String(), "A little shopping help") || !strings.Contains(guest.Body.String(), product.Name) {
		t.Fatalf("unauthenticated guest: %d %s", guest.Code, guest.Body)
	}
	for _, private := range []string{otherProduct.Name, "Sign in", "Sign out", "Shopping views", product.ID, list.List.ID, input.Items[0].ItemID, p.Owner().ID(), product.Description, product.Media[0].ImageURL} {
		if private != "" && strings.Contains(guest.Body.String(), private) {
			t.Fatalf("public page leaked %q", private)
		}
	}
	if guest.Header().Get("Cache-Control") != "no-store" || guest.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(guest.Header().Get("X-Robots-Tag"), "noindex") || len(guest.Result().Cookies()) != 0 {
		t.Fatal("public privacy headers/cookies are wrong")
	}
	guestInput := map[string]any{"picked_up": true, "expected_revision": 1}
	checked := publicRequest(handler, "PATCH", guestURL+"/items/1", share.WriteToken, "guest-check", guestInput)
	if checked.Code != 200 || !strings.Contains(checked.Body.String(), `"picked_up":true`) {
		t.Fatalf("guest check: %d %s", checked.Code, checked.Body)
	}
	original, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || original.List.Revision != list.List.Revision {
		t.Fatalf("guest altered owner list: %#v %v", original, err)
	}
	for _, item := range original.Items {
		if item.PickedUp || item.Quantity != map[string]string{product.ID: "1", saved.Record.ID: "4"}[item.ProductID] {
			t.Fatal("guest altered owner checkoff/quantity")
		}
	}
	product.Name, product.PackageCount = "Private later product name", new(int64(12))
	if _, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("later-catalog"), Product: product, ExpectedRevision: product.Revision}); err != nil {
		t.Fatal(err)
	}
	f.Reopen(t)
	service = shopping.New(f.Store)
	handler = newHandler(t, f, service)
	reopened, err := service.PublicShare(t.Context(), created.Record.Code)
	if err != nil || len(reopened.Items) != 1 || !reopened.Items[0].PickedUp || reopened.Items[0].Name != "Polar Black Cherry Seltzer" || *reopened.Items[0].PackageCount != 8 || reopened.ExpiresAt != created.Record.ExpiresAt {
		t.Fatalf("reopened snapshot changed: %#v %v", reopened, err)
	}
	replay := apiRequest(handler, cookie, csrf, "POST", path, input.IdempotencyKey, body)
	if replay.Code != 200 || !strings.Contains(replay.Body.String(), `"replayed":true`) || !strings.Contains(replay.Body.String(), created.Record.Code) || countShares(t, f) != 1 {
		t.Fatalf("creation replay not durable: %d %s", replay.Code, replay.Body)
	}
	replayedCheck := publicRequest(handler, "PATCH", guestURL+"/items/1", share.WriteToken, "guest-check", guestInput)
	if replayedCheck.Code != 200 || !strings.Contains(replayedCheck.Body.String(), `"replayed":true`) {
		t.Fatalf("guest replay not durable: %d %s", replayedCheck.Code, replayedCheck.Body)
	}
	var purchases int
	if err := shareDB(t, f).QueryRowContext(t.Context(), `SELECT count(*) FROM purchases`).Scan(&purchases); err != nil || purchases != 0 {
		t.Fatalf("guest manufactured purchase: %d %v", purchases, err)
	}
	if strings.Contains(logs.String(), created.Record.Code) || strings.Contains(logs.String(), share.WriteToken) || strings.Contains(logs.String(), guestURL) {
		t.Fatalf("public capability logged: %s", &logs)
	}
	activity, err := service.GetActivity(t.Context(), p, shopping.ActivityInput{Limit: 10})
	raw, _ := json.Marshal(activity)
	if err != nil || !bytes.Contains(raw, []byte("create_temporary_share")) || bytes.Contains(raw, []byte(created.Record.Code)) || bytes.Contains(raw, []byte(share.WriteToken)) {
		t.Fatalf("share audit privacy: %s %v", raw, err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	session := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), identity.Read))
	if result := call(t, session, "get_activity", shopping.ActivityInput{Limit: 10}); result.IsError {
		t.Fatalf("share activity schema: %#v", result)
	}
}

func TestShareCreationAuthScopeCSRFAndAtomicSelection(t *testing.T) {
	f, service, p, _, list := setup(t)
	input := shareInput(list, "share-auth")
	for _, scopes := range []string{identity.Read, identity.ListsWrite, identity.ProductsWrite} {
		if _, err := service.CreateShare(t.Context(), f.Principal(t, "alice", scopes), input); !errors.Is(err, identity.ErrForbidden) {
			t.Fatalf("inadequate scope %q: %v", scopes, err)
		}
	}
	bob := f.Principal(t, "bob", allScopes)
	if _, err := service.CreateShare(t.Context(), bob, input); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign list shared: %v", err)
	}
	cookie := f.Cookie(t, "alice")
	_, csrf := f.BrowserPrincipal(t, cookie)
	handler := newHandler(t, f, service)
	path := "/api/v1/lists/" + list.List.ID + "/shares"
	body := map[string]any{"expected_revision": input.ExpectedRevision, "items": input.Items}
	if response := apiRequest(handler, nil, csrf, "POST", path, "no-session", body); response.Code != 401 {
		t.Fatalf("unauthenticated share: %d", response.Code)
	}
	if response := apiRequest(handler, cookie, "", "POST", path, "no-csrf", body); response.Code != 403 {
		t.Fatalf("share without CSRF: %d", response.Code)
	}
	for name, mutate := range map[string]func(*shopping.CreateShareInput){
		"empty":     func(in *shopping.CreateShareInput) { in.Items = nil },
		"duplicate": func(in *shopping.CreateShareInput) { in.Items = append(in.Items, in.Items[0]) },
		"foreign-after-valid": func(in *shopping.CreateShareInput) {
			in.Items = append(in.Items, sqlite.ShareSelection{ItemID: "foreign-item", ExpectedRevision: 1, ExpectedProductRevision: 1})
		},
		"stale-item":            func(in *shopping.CreateShareInput) { in.Items[0].ExpectedRevision++ },
		"stale-product":         func(in *shopping.CreateShareInput) { in.Items[0].ExpectedProductRevision++ },
		"stale-list":            func(in *shopping.CreateShareInput) { in.ExpectedRevision++ },
		"zero-product-revision": func(in *shopping.CreateShareInput) { in.Items[0].ExpectedProductRevision = 0 },
	} {
		bad := input
		bad.Items = append([]sqlite.ShareSelection(nil), input.Items...)
		mutate(&bad)
		if _, err := service.CreateShare(t.Context(), p, bad); err == nil || countShares(t, f) != 0 {
			t.Fatalf("invalid selection %s partially shared: %v", name, err)
		}
	}
	foreignList, err := service.SaveList(t.Context(), bob, shopping.SaveListInput{WriteMeta: meta("foreign-list"), Title: "Bob list"})
	if err != nil {
		t.Fatal(err)
	}
	foreignProduct, err := service.SaveProduct(t.Context(), bob, shopping.SaveProductInput{WriteMeta: meta("foreign-product"), Product: testfixture.Polar()})
	if err != nil {
		t.Fatal(err)
	}
	foreignItems, err := service.UpdateItems(t.Context(), bob, shopping.UpdateItemsInput{WriteMeta: meta("foreign-add"), ListID: foreignList.Record.List.ID, ExpectedRevision: 1, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: foreignProduct.Record.ID, Quantity: new("1"), Merge: "keep"}}})
	if err != nil {
		t.Fatal(err)
	}
	mixed := input
	mixed.Items = append(append([]sqlite.ShareSelection(nil), input.Items...), shareInput(foreignItems.Record, "ignored").Items[0])
	if _, err := service.CreateShare(t.Context(), p, mixed); !errors.Is(err, sqlite.ErrNotFound) || countShares(t, f) != 0 {
		t.Fatalf("real foreign item partially shared: %v", err)
	}
	if _, err := service.CreateShare(t.Context(), p, input); err != nil || countShares(t, f) != 1 {
		t.Fatalf("rejected attempts reserved key: %v", err)
	}
	input.Items[0].ExpectedRevision++
	if _, err := service.CreateShare(t.Context(), p, input); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatalf("different create replay: %v", err)
	}
	if err := f.Store.DisableAccount(t.Context(), p.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateShare(t.Context(), p, shareInput(list, "disabled-share")); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("disabled owner shared: %v", err)
	}
}

func TestShareGuestOriginSchemaInvalidExpiryAndDisabledOwner(t *testing.T) {
	f, service, p, _, list := setup(t)
	created, err := service.CreateShare(t.Context(), p, shareInput(list, "share"))
	if err != nil {
		t.Fatal(err)
	}
	share, _ := service.PublicShare(t.Context(), created.Record.Code)
	handler := newHandler(t, f, service)
	path := "/s/" + created.Record.Code
	good := `{"picked_up":true,"expected_revision":1}`
	for _, tc := range []struct {
		name, origin, token, body string
		want                      int
	}{
		{"missing-origin", "", share.WriteToken, good, 403},
		{"foreign-origin", "https://evil.example", share.WriteToken, good, 403},
		{"no-token", testfixture.Origin, "", good, 403},
		{"wrong-token", testfixture.Origin, strings.Repeat("x", 26), good, 404},
		{"missing-bool", testfixture.Origin, share.WriteToken, `{"expected_revision":1}`, 400},
		{"null-bool", testfixture.Origin, share.WriteToken, `{"picked_up":null,"expected_revision":1}`, 400},
		{"unknown-field", testfixture.Origin, share.WriteToken, `{"picked_up":true,"expected_revision":1,"owner_id":"alice"}`, 400},
		{"toggle", testfixture.Origin, share.WriteToken, `{"toggle":true,"expected_revision":1}`, 400},
		{"revision", testfixture.Origin, share.WriteToken, `{"picked_up":true,"expected_revision":0}`, 400},
		{"extra-json", testfixture.Origin, share.WriteToken, good + `{}`, 400},
		{"oversized", testfixture.Origin, share.WriteToken, strings.Repeat(" ", 65536) + good, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("PATCH", testfixture.Origin+path+"/items/1", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			request.Header.Set("X-Share-CSRF", tc.token)
			request.Header.Set("Idempotency-Key", "bad-"+tc.name)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("%d want %d: %s", response.Code, tc.want, response.Body)
			}
		})
	}
	for _, path := range []string{"/s/abc", "/s/12345", "/s/abcdef", "/s/abcde/extra", "/s", "/s/"} {
		response := publicRequest(handler, "GET", path, "", "", nil)
		if response.Code != 404 || !strings.Contains(response.Body.String(), "This link is unavailable") || strings.Contains(response.Body.String(), "Polar") {
			t.Fatalf("invalid route %s: %d %s", path, response.Code, response.Body)
		}
	}
	valid := publicRequest(handler, "PATCH", path+"/items/1", share.WriteToken, "guest", map[string]any{"picked_up": true, "expected_revision": 1})
	if valid.Code != 200 {
		t.Fatalf("valid after failures: %d %s", valid.Code, valid.Body)
	}
	stale := publicRequest(handler, "PATCH", path+"/items/1", share.WriteToken, "stale", map[string]any{"picked_up": false, "expected_revision": 1})
	if stale.Code != 409 {
		t.Fatalf("stale guest: %d %s", stale.Code, stale.Body)
	}
	// Only the synthetic fixture DB is altered to test actual HTTP expiry.
	expires := time.Now().Add(-time.Second).UnixMilli()
	if _, err := shareDB(t, f).ExecContext(t.Context(), `UPDATE temporary_shares SET created_at=?,expires_at=?`, expires-sqlite.ShareLifetime.Milliseconds(), expires); err != nil {
		t.Fatal(err)
	}
	expired := publicRequest(handler, "GET", path, "", "", nil)
	invalid := publicRequest(handler, "GET", "/s/12345", "", "", nil)
	if expired.Code != 404 || expired.Body.String() != invalid.Body.String() || strings.Contains(expired.Body.String(), "Polar") {
		t.Fatal("expired/invalid differ or leak items")
	}
	for _, key := range []string{"guest", "new-attempt"} {
		response := publicRequest(handler, "PATCH", path+"/items/1", share.WriteToken, key, map[string]any{"picked_up": true, "expected_revision": 1})
		if response.Code != 404 || strings.Contains(response.Body.String(), "Polar") {
			t.Fatalf("expired write/replay: %d %s", response.Code, response.Body)
		}
	}
	// A second live link becomes unavailable as soon as its owner is disabled.
	second, err := service.CreateShare(t.Context(), p, shareInput(list, "second-share"))
	if err != nil {
		t.Fatal(err)
	}
	secondShare, _ := service.PublicShare(t.Context(), second.Record.Code)
	if err := f.Store.DisableAccount(t.Context(), p.Identity()); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PATCH"} {
		url := "/s/" + second.Record.Code
		if method == "PATCH" {
			url += "/items/1"
		}
		response := publicRequest(handler, method, url, secondShare.WriteToken, "disabled", map[string]any{"picked_up": true, "expected_revision": 1})
		if response.Code != 404 || strings.Contains(response.Body.String(), "Polar") {
			t.Fatalf("disabled share %s: %d %s", method, response.Code, response.Body)
		}
	}
}

func TestShareGlobalBudgetCannotBeBypassedWithForwardedHeaders(t *testing.T) {
	f, service, _, _, _ := setup(t)
	handler := newHandler(t, f, service)
	for i := range 61 {
		request := httptest.NewRequest("GET", testfixture.Origin+"/s/12345", nil)
		request.Header.Set("X-Forwarded-For", strings.Repeat("1", i+1))
		request.Header.Set("Forwarded", "for=192.0.2.1")
		request.RemoteAddr = "192.0.2.1:4000"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if i < 60 && response.Code != 404 || i == 60 && (response.Code != 429 || response.Header().Get("Retry-After") == "") {
			t.Fatalf("budget request %d: %d", i, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", testfixture.Origin+"/health/live", nil))
	if response.Code != 200 {
		t.Fatal("public budget affected ordinary application")
	}
}
