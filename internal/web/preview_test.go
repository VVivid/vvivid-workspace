package web_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
)

// This opt-in server exists only in the Go test binary. It injects an actual
// OIDC-created test session into requests before production authentication runs,
// avoiding untrusted HTTPS certificates in a local visual-only browser fixture.
// The production executable neither imports this wrapper nor reads this setting.
func TestMobilePreview(t *testing.T) {
	addr := os.Getenv("SHOPPING_UI_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("opt-in visual preview")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("preview must bind an explicit loopback IP")
	}
	f, service, p, polar, list := setup(t)
	// Synthetic observations exercise independent unknown price and stock labels.
	// They are visual test data, not retailer checks or purchase evidence.
	source := polar.Listings[0].ProductURL
	observed := new(polar.Media[0].ObservedAt)
	store := new("Synthetic preview store")
	polar.Listings = append(polar.Listings,
		sqlite.Listing{Retailer: "Fixture in stock, no price", ProductURL: source, StoreLocation: store, ObservedAt: observed, Availability: "in_stock"},
		sqlite.Listing{Retailer: "Fixture out of stock, no price", ProductURL: source, StoreLocation: store, ObservedAt: observed, Availability: "out_of_stock"},
		sqlite.Listing{Retailer: "Fixture observed price, unknown stock", ProductURL: source, StoreLocation: store, PriceMinor: new(int64(599)), Currency: new("USD"), ObservedAt: observed, Availability: "unknown"},
		sqlite.Listing{Retailer: "Fixture zero price, unknown stock", ProductURL: source, PriceMinor: new(int64(0)), Currency: new("USD"), ObservedAt: observed, Availability: "unknown"},
	)
	if _, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("preview-listings"), Product: polar, ExpectedRevision: polar.Revision}); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"Avocados", "Oat milk", "Free-range eggs", "Sourdough bread", "Strawberries"} {
		product := testfixture.Polar()
		product.Name = name
		product.Brand = ""
		product.Flavor = ""
		product.PackageCount = nil
		product.PackageSize = nil
		product.PackageUnit = nil
		product.Description = ""
		product.Nutrition = nil
		product.Media = nil
		product.Listings = nil
		product.Category = []string{"produce", "drinks", "dairy", "pantry", "produce"}[i]
		result, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta(fmt.Sprintf("preview-product-%d", i)), Product: product})
		if err != nil {
			t.Fatal(err)
		}
		quantity := "1"
		if i == 1 {
			quantity = "2"
		}
		added, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta(fmt.Sprintf("preview-add-%d", i)), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: result.Record.ID, Quantity: new(quantity), Merge: "increment"}}})
		if err != nil {
			t.Fatal(err)
		}
		list = added.Record
	}
	cookie := f.Cookie(t, "alice")
	handler := newHandler(t, f, service)
	var writeFailureStatus atomic.Int64
	var failPageReads atomic.Bool
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	localOrigin := "http://" + listener.Addr().String()
	preview := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Shopping-Test-Fixture", "visual-only")
		if r.Host != listener.Addr().String() {
			http.Error(w, "fixture host mismatch", 421)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != localOrigin {
			http.Error(w, "fixture origin mismatch", 403)
			return
		}
		switch r.URL.Path {
		case "/__fixture/fail-writes":
			writeFailureStatus.Store(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("Fixture writes will fail with 503."))
			return
		case "/__fixture/conflict-writes":
			writeFailureStatus.Store(http.StatusConflict)
			_, _ = w.Write([]byte("Fixture writes will fail with 409."))
			return
		case "/__fixture/restore-writes":
			writeFailureStatus.Store(0)
			_, _ = w.Write([]byte("Fixture writes restored."))
			return
		case "/__fixture/fail-page-reads":
			failPageReads.Store(true)
			_, _ = w.Write([]byte("Fixture page reads will fail with 503."))
			return
		case "/__fixture/restore-page-reads":
			failPageReads.Store(false)
			_, _ = w.Write([]byte("Fixture page reads restored."))
			return
		}
		if failureStatus := int(writeFailureStatus.Load()); failureStatus != 0 && strings.HasPrefix(r.URL.Path, "/api/v1/") && r.Method != "GET" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(failureStatus)
			_, _ = w.Write([]byte(`{"message":"Fixture write failure"}`))
			return
		}
		if failPageReads.Load() && r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/products" || r.URL.Path == "/history" || r.URL.Path == "/insights") {
			http.Error(w, "Fixture page read failure", http.StatusServiceUnavailable)
			return
		}
		cloned := r.Clone(r.Context())
		cloned.Host = "shopping.example"
		cloned.AddCookie(cookie)
		if cloned.Header.Get("Origin") != "" {
			cloned.Header.Set("Origin", testfixture.Origin)
		}
		handler.ServeHTTP(w, cloned)
	})
	server := &http.Server{Handler: preview}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Logf("Visual fixture ready: %s (only synthetic test data; production authentication still executes)", localOrigin)
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-t.Context().Done():
	}
}
