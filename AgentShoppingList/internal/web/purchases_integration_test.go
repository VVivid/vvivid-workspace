package web_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
)

func purchaseDraft(productID, source string) sqlite.PurchaseDraft {
	return sqlite.PurchaseDraft{
		Retailer: "Walmart", PurchasedAt: time.Now().Add(-time.Minute).UnixMilli(), Currency: "USD",
		SourceKind: "user_confirmation", SourceReference: source, SourceEvidence: "Synthetic test: user explicitly confirmed one purchased pack and its receipt price.", SourceObservedAt: time.Now().UnixMilli(),
		Lines: []sqlite.PurchaseLineDraft{{ProductID: productID, ExpectedProductRevision: 1, Quantity: "1", QuantityUnit: "pack", UnitPriceMinor: new(int64(599)), DiscountMinor: new(int64(0)), LineTotalMinor: new(int64(599)), TaxMinor: new(int64(0)), TaxTreatment: "excluded"}},
	}
}

func saveAndConfirm(t *testing.T, s *shopping.Service, p identity.Principal, key string, draft sqlite.PurchaseDraft) sqlite.Purchase {
	t.Helper()
	saved, err := s.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta(key + "-draft"), Purchase: draft})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.ConfirmPurchase(t.Context(), p, shopping.ConfirmPurchaseInput{WriteMeta: meta(key + "-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision})
	if err != nil {
		t.Fatal(err)
	}
	purchase, err := s.GetPurchase(t.Context(), p, confirmed.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	return purchase
}

func TestAgentBrowserPurchaseHistoryInsightsExactlyOnce(t *testing.T) {
	f := testfixture.New(t)
	f.Enroll(t, "alice")
	f.Enroll(t, "bob")
	service := shopping.New(f.Store)
	handler := newHandler(t, f, service)
	server := httptest.NewServer(handler)
	defer server.Close()
	agent := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
	product := resultRecord[shopping.WriteResult[sqlite.Product]](t, call(t, agent, "save_product", shopping.SaveProductInput{WriteMeta: meta("journey-product"), Product: testfixture.Polar()})).Record
	list := resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, agent, "save_list", shopping.SaveListInput{WriteMeta: meta("journey-list"), Title: "Walmart", Retailer: new("Walmart"), MakeActive: true})).Record
	list = resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, agent, "update_list_items", shopping.UpdateItemsInput{WriteMeta: meta("journey-add"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("1"), Merge: "increment"}}})).Record
	cookie := f.Cookie(t, "alice")
	browser, csrf := f.BrowserPrincipal(t, cookie)
	if response := apiRequest(handler, cookie, csrf, "GET", "/api/v1/products/"+product.ID, "", nil); response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte("walmartimages.com")) {
		t.Fatal("browser could not see sourced Polar photo")
	}
	response := apiRequest(handler, cookie, csrf, "PATCH", "/api/v1/lists/"+list.List.ID+"/items/"+list.Items[0].ID, "journey-check", map[string]any{"picked_up": true, "expected_list_revision": list.List.Revision, "expected_revision": list.Items[0].Revision})
	if response.Code != 200 {
		t.Fatalf("browser check: %d %s", response.Code, response.Body)
	}
	list = resultRecord[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{}))
	before := resultRecord[shopping.Insights](t, call(t, agent, "get_insights", shopping.InsightsInput{}))
	if len(before.Spend) != 0 {
		t.Fatal("checking invented spending")
	}
	draft := purchaseDraft(product.ID, "synthetic-confirmation-journey")
	draft.Lines[0].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: list.Items[0].ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: list.Items[0].Revision}
	saved := resultRecord[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "save_purchase", shopping.SavePurchaseInput{WriteMeta: meta("journey-draft"), Purchase: draft})).Record
	confirmation := shopping.ConfirmPurchaseInput{WriteMeta: meta("journey-confirm"), PurchaseID: saved.ID, ExpectedRevision: saved.Revision}
	confirmed := resultRecord[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "confirm_purchase", confirmation)).Record
	retry := resultRecord[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "confirm_purchase", confirmation))
	if !retry.Replayed || retry.Record.ID != confirmed.ID || !confirmed.ReconciliationApplied || len(confirmed.Reconciliations) != 1 || !confirmed.Reconciliations[0].Before.PickedUp || !confirmed.Reconciliations[0].After.PickedUp || confirmed.Reconciliations[0].After.RemovedAt == nil {
		t.Fatalf("confirmation/replay lost evidence: %#v", retry)
	}
	if duplicate := call(t, agent, "save_purchase", shopping.SavePurchaseInput{WriteMeta: meta("journey-duplicate-source"), Purchase: draft}); !duplicate.IsError {
		t.Fatal("duplicate source became another purchase")
	}
	page := resultRecord[shopping.PurchasePage](t, call(t, agent, "search_purchases", shopping.SearchPurchasesInput{Status: "confirmed", SourceReference: draft.SourceReference}))
	if len(page.Purchases) != 1 {
		t.Fatal("purchase counted twice")
	}
	fetched := resultRecord[sqlite.Purchase](t, call(t, agent, "get_purchase", map[string]any{"purchase_id": confirmed.ID}))
	if fetched.Lines[0].Name != product.Name || len(fetched.Lines[0].Sources) == 0 {
		t.Fatal("snapshot lost product identity or sources")
	}
	insights := resultRecord[shopping.Insights](t, call(t, agent, "get_insights", shopping.InsightsInput{}))
	if len(insights.Spend) != 1 || *insights.Spend[0].MerchandiseMinor != 599 || insights.Quantities[0].Quantity != "2.839058838" || insights.Quantities[0].Unit != "L" {
		t.Fatalf("derived amounts/pack conversion: %#v", insights)
	}
	for _, path := range []string{"/history", "/history?purchase=" + confirmed.ID, "/insights", "/api/v1/purchases", "/api/v1/purchases/" + confirmed.ID, "/api/v1/insights"} {
		response := apiRequest(handler, cookie, csrf, "GET", path, "", nil)
		if response.Code != 200 || response.Body.Len() == 0 {
			t.Fatalf("browser read %s: %d %s", path, response.Code, response.Body)
		}
	}
	history := apiRequest(handler, cookie, csrf, "GET", "/history", "", nil)
	if !bytes.Contains(history.Body.Bytes(), []byte("Buy again")) || !bytes.Contains(history.Body.Bytes(), []byte("USD 5.99")) || !bytes.Contains(history.Body.Bytes(), []byte("Tax: USD 0.00")) {
		t.Fatalf("history omitted known zero or buy again: %s", history.Body)
	}
	list = resultRecord[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{}))
	buy := map[string]any{"product_id": product.ID, "quantity": "1", "merge": "increment", "retailer": "Walmart", "expected_list_revision": list.List.Revision}
	for range 2 {
		if response := apiRequest(handler, cookie, csrf, "POST", "/api/v1/lists/"+list.List.ID+"/items", "journey-buy-again", buy); response.Code != 200 {
			t.Fatalf("Buy again: %d %s", response.Code, response.Body)
		}
	}
	list = resultRecord[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{}))
	if len(list.Items) != 1 || list.Items[0].Quantity != "1" {
		t.Fatal("Buy again retry double-added")
	}
	if _, err := service.SavePurchase(t.Context(), browser, shopping.SavePurchaseInput{WriteMeta: meta("browser-purchase"), Purchase: draft}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("browser granted purchase write")
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		if response := apiRequest(handler, cookie, csrf, method, "/api/v1/purchases", "forbidden-write", draft); response.Code != 405 {
			t.Fatalf("browser purchase write route: %d", response.Code)
		}
	}
	bob := mcpClient(t, server.URL, f.Bearer("bob", "shopping-chatgpt", f.Manager.MCPAudience(), allScopes))
	if result := call(t, bob, "get_purchase", map[string]any{"purchase_id": confirmed.ID}); !result.IsError {
		t.Fatal("foreign MCP purchase read")
	}
	bobCookie := f.Cookie(t, "bob")
	if response := apiRequest(handler, bobCookie, "", "GET", "/api/v1/purchases/"+confirmed.ID, "", nil); response.Code != 404 {
		t.Fatalf("foreign browser purchase read: %d", response.Code)
	}
	if readOnly := mcpClient(t, server.URL, f.Bearer("alice", "shopping-chatgpt", f.Manager.MCPAudience(), identity.Read)); !call(t, readOnly, "confirm_purchase", confirmation).IsError {
		t.Fatal("read scope granted confirmation")
	}
	voided := resultRecord[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "void_purchase", shopping.VoidPurchaseInput{WriteMeta: meta("journey-void"), PurchaseID: confirmed.ID, ExpectedRevision: confirmed.Revision, Reason: "Synthetic full void for SDK acceptance"}))
	if voided.Record.Status != "void" {
		t.Fatal("SDK void did not persist")
	}
	activity := resultRecord[shopping.ActivityPage](t, call(t, agent, "get_activity", shopping.ActivityInput{}))
	raw, _ := json.Marshal(activity)
	if !bytes.Contains(raw, []byte(`"status":"confirmed"`)) || !bytes.Contains(raw, []byte(`"status":"void"`)) {
		t.Fatal("SDK activity lost purchase audit alternatives")
	}
}

func TestPurchasePartialReconciliationRollbackReplacementVoidAndRestart(t *testing.T) {
	f, service, p, product, list := setup(t)
	resized, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("purchase-size"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "set_quantity", ItemID: list.Items[0].ID, ExpectedRevision: list.Items[0].Revision, Quantity: new("3")}, {Kind: "set_picked_up", ItemID: list.Items[0].ID, ExpectedRevision: list.Items[0].Revision + 1, PickedUp: new(true)}}})
	if err != nil {
		t.Fatal(err)
	}
	list = resized.Record
	draft := purchaseDraft(product.ID, "partial-receipt")
	draft.SourceKind = "receipt"
	draft.Lines[0].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: list.Items[0].ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: list.Items[0].Revision}
	confirmed := saveAndConfirm(t, service, p, "partial", draft)
	after, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || len(after.Items) != 1 || after.Items[0].Quantity != "2" || !after.Items[0].PickedUp {
		t.Fatalf("partial remainder/evidence: %#v %v", after, err)
	}
	// The first linked item is valid and changed before the second fails. The
	// transaction must undo that first change and leave the purchase a draft.
	variant := testfixture.Polar()
	variant.Name, variant.Flavor = "Second drink", "Lime"
	second, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("purchase-second"), Product: variant})
	if err != nil {
		t.Fatal(err)
	}
	added, err := service.UpdateItems(t.Context(), p, shopping.UpdateItemsInput{WriteMeta: meta("purchase-second-add"), ListID: list.List.ID, ExpectedRevision: after.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: second.Record.ID, Quantity: new("1"), Merge: "keep"}}})
	if err != nil {
		t.Fatal(err)
	}
	list = added.Record
	var firstItem, secondItem sqlite.ListEntry
	for _, item := range list.Items {
		if item.ProductID == product.ID {
			firstItem = item
		} else {
			secondItem = item
		}
	}
	bad := purchaseDraft(product.ID, "atomic-receipt")
	bad.Lines[0].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: firstItem.ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: firstItem.Revision}
	secondLine := bad.Lines[0]
	secondLine.ProductID = second.Record.ID
	secondLine.ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: secondItem.ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: secondItem.Revision + 1}
	bad.Lines = append(bad.Lines, secondLine)
	saved, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta("atomic-draft"), Purchase: bad})
	if err != nil {
		t.Fatal(err)
	}
	failed := shopping.ConfirmPurchaseInput{WriteMeta: meta("atomic-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision}
	if _, err := service.ConfirmPurchase(t.Context(), p, failed); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("stale linked item accepted: %v", err)
	}
	unchanged, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || unchanged.List.Revision != list.List.Revision || unchanged.Items[0].Revision != list.Items[0].Revision {
		t.Fatal("confirmation partially committed")
	}
	stillDraft, err := service.GetPurchase(t.Context(), p, saved.Record.ID)
	if err != nil || stillDraft.Status != "draft" || len(stillDraft.Reconciliations) != 0 {
		t.Fatal("failed confirmation changed purchase")
	}
	// A fresh draft revision repairs the expected item revision; the failed key
	// was not reserved, but its changed semantics appropriately use a new key.
	bad.ID = saved.Record.ID
	bad.Lines[1].ListLink.ExpectedItemRevision = secondItem.Revision
	repaired, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta("atomic-repair"), Purchase: bad, ExpectedRevision: saved.Record.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Record.Revision != 2 {
		t.Fatal("draft revision did not advance")
	}
	// Change the current catalog before replacement confirmation. Old confirmed
	// snapshots remain frozen; replacement captures these new facts once.
	product.Name, product.PackageCount = "Revised Polar label", new(int64(12))
	if _, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("purchase-catalog-edit"), Product: product, ExpectedRevision: product.Revision}); err != nil {
		t.Fatal(err)
	}
	replacement := purchaseDraft(product.ID, draft.SourceReference)
	replacement.SourceKind, replacement.ReplacesID = "receipt", new(confirmed.ID)
	replacement.Lines[0].Quantity = "4"
	replacement.Lines[0].ExpectedProductRevision = product.Revision + 1
	replacement.Lines[0].UnitPriceMinor, replacement.Lines[0].LineTotalMinor = new(int64(200)), new(int64(800))
	newDraft, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta("replacement-draft"), Purchase: replacement})
	if err != nil {
		t.Fatal(err)
	}
	replace := shopping.ConfirmPurchaseInput{WriteMeta: meta("replacement-confirm"), PurchaseID: newDraft.Record.ID, ExpectedRevision: newDraft.Record.Revision, ExpectedReplacedRevision: confirmed.Revision}
	corrected, err := service.ConfirmPurchase(t.Context(), p, replace)
	if err != nil {
		t.Fatal(err)
	}
	if corrected.Record.ReconciliationApplied || corrected.Record.OriginalReconciliationPurchase == nil || *corrected.Record.OriginalReconciliationPurchase != confirmed.ID || len(corrected.Record.Reconciliations) != 1 || corrected.Record.Reconciliations[0].PurchasedQuantity != "1" {
		t.Fatal("replacement reapplied or lost reconciliation")
	}
	currentList, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || currentList.List.Revision != list.List.Revision || currentList.Items[0].Quantity != list.Items[0].Quantity {
		t.Fatal("replacement altered list quantities")
	}
	old, err := service.GetPurchase(t.Context(), p, confirmed.ID)
	if err != nil || old.Status != "superseded" || old.Lines[0].Name != "Polar Black Cherry Seltzer" || *old.Lines[0].PackageCount != 8 || corrected.Record.Lines[0].Name != product.Name || *corrected.Record.Lines[0].PackageCount != 12 {
		t.Fatal("correction rewrote old snapshots or missed current snapshot")
	}
	insights, err := service.GetInsights(t.Context(), p, shopping.InsightsInput{})
	if err != nil || len(insights.Spend) != 1 || *insights.Spend[0].MerchandiseMinor != 800 {
		t.Fatalf("replacement double count: %#v %v", insights, err)
	}
	void := shopping.VoidPurchaseInput{WriteMeta: meta("replacement-void"), PurchaseID: corrected.Record.ID, ExpectedRevision: corrected.Record.Revision, Reason: "Synthetic receipt cancelled in full"}
	voided, err := service.VoidPurchase(t.Context(), p, void)
	if err != nil || voided.Record.Status != "void" {
		t.Fatalf("void: %v", err)
	}
	if _, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta("void-source-duplicate"), Purchase: draft}); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("void released source identity")
	}
	insights, err = service.GetInsights(t.Context(), p, shopping.InsightsInput{})
	if err != nil || len(insights.Spend) != 0 {
		t.Fatal("void still counted")
	}
	server := httptest.NewServer(newHandler(t, f, service))
	agent := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
	var auditJSON []byte
	for cursor := ""; ; {
		page := resultRecord[shopping.ActivityPage](t, call(t, agent, "get_activity", shopping.ActivityInput{Cursor: cursor}))
		raw, _ := json.Marshal(page)
		auditJSON = append(auditJSON, raw...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	server.Close()
	if !bytes.Contains(auditJSON, []byte(`"status":"superseded"`)) || !bytes.Contains(auditJSON, []byte(`"reconciliation_applied":false`)) || !bytes.Contains(auditJSON, []byte(`"void_reason"`)) {
		t.Fatal("SDK audit schema rejected or omitted replacement/void state")
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
	replayed, err := restarted.ConfirmPurchase(t.Context(), p, replace)
	if err != nil || !replayed.Replayed || replayed.Record.Status != "confirmed" {
		t.Fatalf("restart replay lost original result: %#v %v", replayed, err)
	}
	latest, err := restarted.GetPurchase(t.Context(), p, corrected.Record.ID)
	if err != nil || latest.Status != "void" {
		t.Fatal("replay rewrote later void")
	}
	finalList, err := restarted.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || finalList.List.Revision != list.List.Revision {
		t.Fatal("restart/void changed list")
	}
}

func TestPurchaseConcurrentConfirmAndSourceClaim(t *testing.T) {
	_, service, p, product, list := setup(t)
	draft := purchaseDraft(product.ID, "concurrent-receipt")
	draft.Lines[0].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: list.Items[0].ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: list.Items[0].Revision}
	saved, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta("race-draft"), Purchase: draft})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		group.Go(func() {
			_, err := service.ConfirmPurchase(t.Context(), p, shopping.ConfirmPurchaseInput{WriteMeta: meta(fmt.Sprintf("confirm-race-%d", i)), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision})
			results <- err
		})
	}
	group.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, sqlite.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("concurrent confirmations: %d wins, %d conflicts", wins, conflicts)
	}
	page, err := service.SearchPurchases(t.Context(), p, shopping.SearchPurchasesInput{Status: "confirmed"})
	if err != nil || len(page.Purchases) != 1 || len(page.Purchases[0].Reconciliations) != 1 {
		t.Fatal("concurrent confirmation duplicated evidence")
	}
	// A different request key still cannot claim the same receipt identity.
	results = make(chan error, 2)
	for i := range 2 {
		group.Go(func() {
			_, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta(fmt.Sprintf("source-race-%d", i)), Purchase: draft})
			results <- err
		})
	}
	group.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, sqlite.ErrConflict) {
			t.Fatalf("duplicate source race: %v", err)
		}
	}
}

func TestPurchaseNullZeroDiscountTaxCurrenciesAndCategoryBasis(t *testing.T) {
	_, service, p, product, _ := setup(t)
	pantry := testfixture.Polar()
	pantry.Name, pantry.Category, pantry.PackageCount, pantry.PackageSize, pantry.PackageUnit = "Rice", "pantry", new(int64(1)), new("1"), new("kg")
	saved, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("rice"), Product: pantry})
	if err != nil {
		t.Fatal(err)
	}
	draft := purchaseDraft(product.ID, "basis-receipt")
	draft.Lines[0].Quantity, draft.Lines[0].UnitPriceMinor, draft.Lines[0].DiscountMinor, draft.Lines[0].LineTotalMinor, draft.Lines[0].TaxMinor = "1.25", new(int64(200)), new(int64(50)), new(int64(200)), new(int64(20))
	unknown := sqlite.PurchaseLineDraft{ProductID: saved.Record.ID, ExpectedProductRevision: saved.Record.Revision, Quantity: "2", QuantityUnit: "pack", TaxTreatment: "unknown"}
	free := sqlite.PurchaseLineDraft{ProductID: saved.Record.ID, ExpectedProductRevision: saved.Record.Revision, Quantity: "1", QuantityUnit: "pack", UnitPriceMinor: new(int64(0)), LineTotalMinor: new(int64(0)), DiscountMinor: new(int64(0)), TaxMinor: new(int64(0)), TaxTreatment: "excluded"}
	draft.Lines = append(draft.Lines, unknown, free)
	saveAndConfirm(t, service, p, "basis", draft)
	eur := purchaseDraft(product.ID, "eur-receipt")
	eur.Currency = "EUR"
	saveAndConfirm(t, service, p, "eur", eur)
	insights, err := service.GetInsights(t.Context(), p, shopping.InsightsInput{})
	if err != nil {
		t.Fatal(err)
	}
	var usd shopping.Spend
	for _, spend := range insights.Spend {
		if spend.Currency == "USD" {
			usd = spend
		}
	}
	if len(insights.Spend) != 2 || usd.MerchandiseMinor == nil || *usd.MerchandiseMinor != 200 || *usd.TaxMinor != 20 || usd.MissingMerchandiseLines != 1 || usd.MissingTaxLines != 1 || usd.RecordedLines != 3 || usd.MerchandiseCompleteForRecordedLines {
		t.Fatalf("nullable spend/tax/currency basis: %#v", insights.Spend)
	}
	var categories int64
	missing := 0
	for _, category := range insights.Categories {
		if category.Currency == "USD" {
			if category.MerchandiseMinor != nil {
				categories += *category.MerchandiseMinor
			}
			missing += category.MissingMerchandiseLines
		}
	}
	if categories != *usd.MerchandiseMinor || missing != usd.MissingMerchandiseLines {
		t.Fatal("categories use a different merchandise basis")
	}
	for _, attempt := range []struct {
		name   string
		change func(*sqlite.PurchaseDraft)
	}{
		{"arithmetic", func(d *sqlite.PurchaseDraft) { d.Lines[0].LineTotalMinor = new(int64(1)) }},
		{"negative", func(d *sqlite.PurchaseDraft) { d.Lines[0].TaxMinor = new(int64(-1)) }},
		{"included-tax", func(d *sqlite.PurchaseDraft) { d.Lines[0].TaxTreatment = "included" }},
		{"overflow", func(d *sqlite.PurchaseDraft) { d.Lines[0].UnitPriceMinor = new(int64(9_999_999_999)) }},
		{"no-evidence", func(d *sqlite.PurchaseDraft) { d.SourceEvidence = "" }},
		{"invalid-currency", func(d *sqlite.PurchaseDraft) { d.Currency = "ZZZ" }},
		{"unknown-unit", func(d *sqlite.PurchaseDraft) { d.Lines[0].QuantityUnit = "can?" }},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			bad := purchaseDraft(product.ID, attempt.name)
			attempt.change(&bad)
			if _, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta(attempt.name), Purchase: bad}); err == nil {
				t.Fatal("invalid purchase accepted")
			}
		})
	}
	page, err := service.SearchPurchases(t.Context(), p, shopping.SearchPurchasesInput{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatal("purchase cursor missing")
	}
	next, err := service.SearchPurchases(t.Context(), p, shopping.SearchPurchasesInput{Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Purchases) != 1 || next.Purchases[0].ID == page.Purchases[0].ID {
		t.Fatal("purchase cursor repeated record")
	}
	raw, err := json.Marshal(insights)
	if err != nil || !bytes.Contains(raw, []byte("historical completeness is unknown")) {
		t.Fatal("insights lost recorded-only coverage")
	}
}

func TestProductRevisionConflictAndSafeHistoryReordering(t *testing.T) {
	f, service, p, product, list := setup(t)
	draft := purchaseDraft(product.ID, "revision-pinned-receipt")
	draft.Lines[0].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: list.Items[0].ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: list.Items[0].Revision}
	saved, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta("pinned-draft"), Purchase: draft})
	if err != nil {
		t.Fatal(err)
	}
	product.PackageCount = new(int64(12))
	changed, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("pinned-edit"), Product: product, ExpectedRevision: product.Revision})
	if err != nil {
		t.Fatal(err)
	}
	confirmation := shopping.ConfirmPurchaseInput{WriteMeta: meta("pinned-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision}
	if _, err := service.ConfirmPurchase(t.Context(), p, confirmation); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("draft silently captured changed pack: %v", err)
	}
	unchanged, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil || unchanged.List.Revision != list.List.Revision || len(unchanged.Items) != 1 || unchanged.Items[0].RemovedAt != nil {
		t.Fatal("product conflict partially reconciled list")
	}
	draft.ID = saved.Record.ID
	draft.Lines[0].ExpectedProductRevision = changed.Record.Revision
	revised, err := service.SavePurchase(t.Context(), p, shopping.SavePurchaseInput{WriteMeta: meta("pinned-draft-revise"), Purchase: draft, ExpectedRevision: saved.Record.Revision})
	if err != nil {
		t.Fatal(err)
	}
	confirmation.ExpectedRevision = revised.Record.Revision
	confirmed, err := service.ConfirmPurchase(t.Context(), p, confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if *confirmed.Record.Lines[0].PackageCount != 12 {
		t.Fatal("explicitly corrected pack not captured")
	}
	lineID := confirmed.Record.Lines[0].ID
	// A description-only catalog edit leaves the variant suitable for Buy again.
	product = changed.Record
	product.Description = "Updated description with the same exact package."
	metadata, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("reorder-description"), Product: product, ExpectedRevision: product.Revision})
	if err != nil {
		t.Fatal(err)
	}
	options, err := service.ReorderableLines(t.Context(), p, []string{confirmed.Record.ID})
	if err != nil || options[lineID] != metadata.Record.Revision {
		t.Fatal("harmless metadata edit disabled reorder")
	}
	handler := newHandler(t, f, service)
	cookie := f.Cookie(t, "alice")
	_, csrf := f.BrowserPrincipal(t, cookie)
	page := apiRequest(handler, cookie, csrf, "GET", "/history", "", nil)
	if page.Code != 200 || !bytes.Contains(page.Body.Bytes(), []byte("Buy again")) || !bytes.Contains(page.Body.Bytes(), []byte(`data-add-product-revision="3"`)) {
		t.Fatalf("current revision not supplied for reorder: %s", page.Body)
	}
	// Another package change after rendering is caught by the same list-add
	// service; the stale history button cannot reinterpret its old quantity.
	product = metadata.Record
	product.PackageCount = new(int64(24))
	if _, err := service.SaveProduct(t.Context(), p, shopping.SaveProductInput{WriteMeta: meta("reorder-pack"), Product: product, ExpectedRevision: product.Revision}); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetList(t.Context(), p, shopping.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	response := apiRequest(handler, cookie, csrf, "POST", "/api/v1/lists/"+current.List.ID+"/items", "stale-reorder", map[string]any{"product_id": product.ID, "quantity": "1", "merge": "increment", "expected_list_revision": current.List.Revision, "expected_product_revision": metadata.Record.Revision})
	if response.Code != 409 {
		t.Fatalf("stale pack reorder accepted: %d %s", response.Code, response.Body)
	}
	page = apiRequest(handler, cookie, csrf, "GET", "/history", "", nil)
	if bytes.Contains(page.Body.Bytes(), []byte("Buy again")) {
		t.Fatal("changed package still offered snapshot reorder")
	}
	// Measured lines remain valid history, but 500g cannot become 500 variants.
	measured := purchaseDraft(product.ID, "measured-receipt")
	measured.Lines[0].ExpectedProductRevision = product.Revision + 1
	measured.Lines[0].Quantity, measured.Lines[0].QuantityUnit = "500", "g"
	measured.Lines[0].UnitPriceMinor = nil
	measuredPurchase := saveAndConfirm(t, service, p, "measured", measured)
	page = apiRequest(handler, cookie, csrf, "GET", "/history?purchase="+measuredPurchase.ID, "", nil)
	if page.Code != 200 || bytes.Contains(page.Body.Bytes(), []byte("Buy again")) {
		t.Fatal("measured quantity offered as variant count")
	}
}
