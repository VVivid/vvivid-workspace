package web_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func purchaseResult(t *testing.T, session *sdk.ClientSession, tool string, input any) shopping.WriteResult[shopping.PurchaseOutcome] {
	t.Helper()
	return resultRecord[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, session, tool, input))
}

func assertPurchaseReplay(t *testing.T, session *sdk.ClientSession, tool string, input any, want shopping.PurchaseOutcome) {
	t.Helper()
	got := purchaseResult(t, session, tool, input)
	if !got.Replayed || !reflect.DeepEqual(got.Record, want) {
		t.Fatalf("%s replay changed its original outcome", tool)
	}
}

func TestPurchaseResultMaximumSourcesAndDurableReplay(t *testing.T) {
	for _, test := range []struct{ name, pattern string }{{"ascii_sdk_products", "a"}, {"escaped_service_products", "&"}, {"unicode_sdk_products", "茶"}} {
		t.Run(test.name, func(t *testing.T) {
			f := testfixture.New(t)
			f.Enroll(t, "alice")
			f.Enroll(t, "bob")
			service := shopping.New(f.Store)
			principal := f.Principal(t, "alice", allScopes)
			server := httptest.NewServer(newHandler(t, f, service))
			t.Cleanup(server.Close)
			agent := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
			products := make(map[string]sqlite.Product, 50)
			operations := make([]sqlite.ItemOperation, 50)
			pattern := test.pattern
			retailer := auditBoundaryText(pattern, 128)
			for i := range 50 {
				product := testfixture.Polar()
				product.Name, product.Brand, product.Flavor = auditBoundaryText(pattern, 300), auditBoundaryText(pattern, 150), auditBoundaryText(pattern, 150)
				product.PackageCount, product.PackageSize, product.PackageUnit = new(int64(10000)), new("999999999.999999"), new(auditBoundaryText(pattern, 32))
				source := product.Sources[0]
				product.Sources = make([]sqlite.FieldSource, 12)
				for j := range 12 {
					prefix := fmt.Sprintf("https://example.com/%02d/%02d?evidence=", i, j)
					product.Sources[j] = source
					product.Sources[j].URL = prefix + auditBoundaryText(pattern, 2048-len(prefix))
				}
				input := shopping.SaveProductInput{WriteMeta: meta(fmt.Sprintf("result-product-%02d", i)), Product: product}
				var saved shopping.WriteResult[sqlite.Product]
				if pattern == "&" {
					// JSON escaping of twelve maximum URLs exceeds the MCP request
					// transport's 128KiB cap. Stage this service-valid extreme directly;
					// every purchase write and read below still uses the real SDK.
					var err error
					saved, err = service.SaveProduct(t.Context(), principal, input)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					saved = resultRecord[shopping.WriteResult[sqlite.Product]](t, call(t, agent, "save_product", input))
				}
				products[saved.Record.ID] = saved.Record
				quantity := "1"
				if i%2 == 1 {
					quantity = "2"
				}
				operations[i] = sqlite.ItemOperation{Kind: "add", ProductID: saved.Record.ID, Quantity: new(quantity), Retailer: new(retailer), Merge: "keep"}
			}
			list := resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, agent, "save_list", shopping.SaveListInput{WriteMeta: meta("result-list"), Title: "Source boundary", Retailer: new(retailer), MakeActive: true})).Record
			list = resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, agent, "update_list_items", shopping.UpdateItemsInput{WriteMeta: meta("result-items"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: operations})).Record
			for i, item := range list.Items {
				operations[i] = sqlite.ItemOperation{Kind: "set_picked_up", ItemID: item.ID, ExpectedRevision: item.Revision, PickedUp: new(true)}
			}
			list = resultRecord[shopping.WriteResult[sqlite.ListView]](t, call(t, agent, "update_list_items", shopping.UpdateItemsInput{WriteMeta: meta("result-picked-up"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: operations})).Record
			draft := purchaseDraft("", auditBoundaryText(pattern, 200))
			draft.Retailer, draft.StoreLocation = retailer, new(auditBoundaryText(pattern, 200))
			draft.SourceEvidence = auditBoundaryText(pattern, 2000)
			prefix := "https://example.com/receipt?evidence="
			draft.SourceURL = new(prefix + auditBoundaryText(pattern, 2048-len(prefix)))
			line := draft.Lines[0]
			line.SourceReference = new(auditBoundaryText(pattern, 200))
			line.UnitPriceMinor, line.LineTotalMinor, line.TaxMinor = new(int64(999_999_999)), new(int64(999_999_999)), new(int64(999_999_999))
			draft.Lines = make([]sqlite.PurchaseLineDraft, 50)
			for i, item := range list.Items {
				draft.Lines[i] = line
				draft.Lines[i].ProductID = item.ProductID
				draft.Lines[i].ExpectedProductRevision = item.Product.Revision
				draft.Lines[i].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: item.ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: item.Revision}
			}
			save := shopping.SavePurchaseInput{WriteMeta: meta("result-save"), Purchase: draft}
			saved := purchaseResult(t, agent, "save_purchase", save)
			if saved.Replayed || len(saved.Record.Lines) != 50 || saved.Record.Status != "draft" {
				t.Fatal("supported source-rich draft did not save")
			}
			revised := draft
			revised.ID = saved.Record.ID
			revised.Lines = slices.Clone(draft.Lines)
			revised.SourceEvidence = "Revised: " + auditBoundaryText(pattern, 1991)
			revised.Lines[0].UnitPriceMinor, revised.Lines[0].LineTotalMinor = new(int64(599)), new(int64(599))
			revise := shopping.SavePurchaseInput{WriteMeta: meta("result-revise"), Purchase: revised, ExpectedRevision: saved.Record.Revision}
			revisedResult := purchaseResult(t, agent, "save_purchase", revise)
			assertPurchaseReplay(t, agent, "save_purchase", save, saved.Record)
			confirm := shopping.ConfirmPurchaseInput{WriteMeta: meta("result-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: revisedResult.Record.Revision}
			confirmed := purchaseResult(t, agent, "confirm_purchase", confirm)
			if !confirmed.Record.ReconciliationApplied || len(confirmed.Record.Reconciliations) != 50 {
				t.Fatal("confirmation lost linked reconciliation evidence")
			}
			original := resultRecord[sqlite.Purchase](t, call(t, agent, "get_purchase", map[string]any{"purchase_id": saved.Record.ID}))
			fullJSON, err := json.Marshal(original)
			if err != nil || len(fullJSON) <= 1<<20 || original.SourceEvidence != revised.SourceEvidence || *original.SourceURL != *draft.SourceURL {
				t.Fatalf("full purchase did not retain its large receipt/source evidence: %d bytes, %v", len(fullJSON), err)
			}
			for i, line := range original.Lines {
				if !reflect.DeepEqual(line.Sources, products[line.ProductID].Sources) || line.ID != confirmed.Record.Lines[i].ID || line.Name != products[line.ProductID].Name {
					t.Fatal("bounded outcome changed the authoritative source or line snapshots")
				}
			}
			afterList := resultRecord[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{IncludeRemoved: true}))
			if afterList.List.Revision != list.List.Revision+1 {
				t.Fatal("confirmation did not advance linked list once")
			}
			// Change catalog facts before a replacement. Confirmation retries must
			// still return the prior outcome, while full reads retain old sources.
			changedProduct := products[original.Lines[0].ProductID]
			changedProduct.Name = "Changed catalog label"
			changedProduct.Sources = []sqlite.FieldSource{testfixture.Polar().Sources[0]}
			changed, err := service.SaveProduct(t.Context(), principal, shopping.SaveProductInput{WriteMeta: meta("result-catalog-edit"), Product: changedProduct, ExpectedRevision: changedProduct.Revision})
			if err != nil {
				t.Fatal(err)
			}
			afterList = resultRecord[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{IncludeRemoved: true}))
			beforeCorrection, _ := json.Marshal(afterList)
			replacement := revised
			replacement.ID, replacement.ReplacesID = "", new(original.ID)
			replacement.Lines = slices.Clone(revised.Lines)
			for i := range replacement.Lines {
				replacement.Lines[i].ListLink = nil
				replacement.Lines[i].Quantity = "2"
				replacement.Lines[i].UnitPriceMinor, replacement.Lines[i].LineTotalMinor = new(int64(350)), new(int64(700))
				if replacement.Lines[i].ProductID == changed.Record.ID {
					replacement.Lines[i].ExpectedProductRevision = changed.Record.Revision
				}
			}
			replacementSave := shopping.SavePurchaseInput{WriteMeta: meta("result-replacement-save"), Purchase: replacement}
			replacementSaved := purchaseResult(t, agent, "save_purchase", replacementSave)
			replace := shopping.ConfirmPurchaseInput{WriteMeta: meta("result-replace"), PurchaseID: replacementSaved.Record.ID, ExpectedRevision: replacementSaved.Record.Revision, ExpectedReplacedRevision: original.Revision + 1}
			if !call(t, agent, "confirm_purchase", replace).IsError {
				t.Fatal("stale replacement accepted")
			}
			replace.ExpectedReplacedRevision = original.Revision
			corrected := purchaseResult(t, agent, "confirm_purchase", replace)
			if corrected.Record.ReconciliationApplied || !reflect.DeepEqual(corrected.Record.Reconciliations, original.Reconciliations) || corrected.Record.OriginalReconciliationPurchase == nil || *corrected.Record.OriginalReconciliationPurchase != original.ID {
				t.Fatal("replacement lost original reconciliation or applied it again")
			}
			void := shopping.VoidPurchaseInput{WriteMeta: meta("result-void"), PurchaseID: corrected.Record.ID, ExpectedRevision: corrected.Record.Revision, Reason: auditBoundaryText(pattern, 1000)}
			voided := purchaseResult(t, agent, "void_purchase", void)
			if voided.Record.Status != "void" || *voided.Record.VoidReason != void.Reason {
				t.Fatal("void lost outcome or reason")
			}
			bob := mcpClient(t, server.URL, f.Bearer("bob", "shopping-chatgpt", f.Manager.MCPAudience(), allScopes))
			if !call(t, bob, "get_purchase", map[string]any{"purchase_id": original.ID}).IsError || !call(t, bob, "confirm_purchase", confirm).IsError {
				t.Fatal("foreign owner read or replay was authorized")
			}
			writeOnly := mcpClient(t, server.URL, f.Bearer("alice", "shopping-chatgpt", f.Manager.MCPAudience(), identity.PurchasesWrite))
			assertPurchaseReplay(t, writeOnly, "save_purchase", save, saved.Record)
			if !call(t, writeOnly, "get_purchase", map[string]any{"purchase_id": original.ID}).IsError {
				t.Fatal("write scope granted full evidence reads")
			}
			_ = writeOnly.Close()
			_ = bob.Close()
			_ = agent.Close()
			server.Close()
			f.Reopen(t)
			service = shopping.New(f.Store)
			server = httptest.NewServer(newHandler(t, f, service))
			t.Cleanup(server.Close)
			agent = mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
			// Every saved attempt is durable, including original drafts whose rows
			// were replaced and confirmations whose current headers later changed.
			for _, attempt := range []struct {
				tool  string
				input any
				want  shopping.PurchaseOutcome
			}{
				{"save_purchase", save, saved.Record}, {"save_purchase", revise, revisedResult.Record},
				{"confirm_purchase", confirm, confirmed.Record}, {"save_purchase", replacementSave, replacementSaved.Record},
				{"confirm_purchase", replace, corrected.Record}, {"void_purchase", void, voided.Record},
			} {
				assertPurchaseReplay(t, agent, attempt.tool, attempt.input, attempt.want)
			}
			current := resultRecord[sqlite.Purchase](t, call(t, agent, "get_purchase", map[string]any{"purchase_id": corrected.Record.ID}))
			old := resultRecord[sqlite.Purchase](t, call(t, agent, "get_purchase", map[string]any{"purchase_id": original.ID}))
			if current.Status != "void" || old.Status != "superseded" || !reflect.DeepEqual(old.Lines, original.Lines) || current.Lines[0].Name != changed.Record.Name || !reflect.DeepEqual(current.Lines[0].Sources, changed.Record.Sources) {
				t.Fatal("restart/replay altered current state or immutable source evidence")
			}
			finalList := resultRecord[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{IncludeRemoved: true}))
			afterCorrection, _ := json.Marshal(finalList)
			if !bytes.Equal(beforeCorrection, afterCorrection) {
				t.Fatal("replacement/void/replay changed reconciled list state")
			}
			assertPurchaseResultStorage(t, f.Path, principal.Owner().ID())
			t.Logf("full immutable purchase: %d JSON bytes", len(fullJSON))
		})
	}
}

func assertPurchaseResultStorage(t *testing.T, path, owner string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), `SELECT key,result FROM idempotency_results WHERE owner_id=? AND operation IN ('save_purchase','confirm_purchase','void_purchase')`, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count, largest := 0, 0
	for rows.Next() {
		var key string
		var result []byte
		if err := rows.Scan(&key, &result); err != nil {
			t.Fatal(err)
		}
		if len(result) >= 1<<20 || bytes.Contains(result, []byte(`"sources_snapshot"`)) {
			t.Fatalf("%s retained an oversized or full-source replay result: %d bytes", key, len(result))
		}
		largest = max(largest, len(result))
		count++
	}
	if err := rows.Err(); err != nil || count != 6 {
		t.Fatalf("expected six durable purchase outcomes, got %d: %v", count, err)
	}
	t.Logf("largest durable purchase outcome: %d JSON bytes", largest)
}

func TestPurchaseOutcomeReplaysLegacyFullResults(t *testing.T) {
	f, service, principal, product, _ := setup(t)
	draft := purchaseDraft(product.ID, "legacy-replay-source")
	save := shopping.SavePurchaseInput{WriteMeta: meta("legacy-original-save"), Purchase: draft}
	saved, err := service.SavePurchase(t.Context(), principal, save)
	if err != nil {
		t.Fatal(err)
	}
	fullDraft, err := service.GetPurchase(t.Context(), principal, saved.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	confirm := shopping.ConfirmPurchaseInput{WriteMeta: meta("legacy-original-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision}
	confirmed, err := service.ConfirmPurchase(t.Context(), principal, confirm)
	if err != nil {
		t.Fatal(err)
	}
	fullConfirmed, err := service.GetPurchase(t.Context(), principal, saved.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	void := shopping.VoidPurchaseInput{WriteMeta: meta("legacy-original-void"), PurchaseID: saved.Record.ID, ExpectedRevision: confirmed.Record.Revision, Reason: "Synthetic legacy void"}
	voided, err := service.VoidPurchase(t.Context(), principal, void)
	if err != nil {
		t.Fatal(err)
	}
	fullVoid, err := service.GetPurchase(t.Context(), principal, saved.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the prior persisted full-record format through the same atomic store
	// boundary, without updating it or resolving any record from mutable state.
	save.WriteMeta, confirm.WriteMeta, void.WriteMeta = meta("legacy-save"), meta("legacy-confirm"), meta("legacy-void")
	for _, attempt := range []struct {
		operation string
		meta      shopping.WriteMeta
		input     any
		record    sqlite.Purchase
	}{
		{"save_purchase", save.WriteMeta, save, fullDraft}, {"confirm_purchase", confirm.WriteMeta, confirm, fullConfirmed}, {"void_purchase", void.WriteMeta, void, fullVoid},
	} {
		raw, _ := json.Marshal(attempt.input)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, "request_id")
		delete(fields, "idempotency_key")
		payload, _ := json.Marshal(fields)
		result, _ := json.Marshal(attempt.record)
		_, err := f.Store.Write(t.Context(), principal.Owner(), sqlite.Mutation{Key: attempt.meta.IdempotencyKey, RequestID: attempt.meta.RequestID, ClientID: principal.ClientID(), Operation: attempt.operation, Payload: payload}, func(*sqlite.Tx) ([]byte, error) { return result, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	f.Reopen(t)
	service = shopping.New(f.Store)
	server := httptest.NewServer(newHandler(t, f, service))
	defer server.Close()
	agent := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
	assertPurchaseReplay(t, agent, "save_purchase", save, saved.Record)
	assertPurchaseReplay(t, agent, "confirm_purchase", confirm, confirmed.Record)
	assertPurchaseReplay(t, agent, "void_purchase", void, voided.Record)
	current := resultRecord[sqlite.Purchase](t, call(t, agent, "get_purchase", map[string]any{"purchase_id": saved.Record.ID}))
	if !reflect.DeepEqual(current, fullVoid) {
		t.Fatal("legacy replay changed the current immutable record")
	}
	tools, err := agent.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if !slices.Contains([]string{"save_purchase", "confirm_purchase", "void_purchase", "get_purchase"}, tool.Name) {
			continue
		}
		schema, _ := json.Marshal(tool.OutputSchema)
		if bytes.Contains(schema, []byte(`"sources_snapshot"`)) != (tool.Name == "get_purchase") || !strings.Contains(tool.Description, "get_purchase") && tool.Name != "get_purchase" {
			t.Fatalf("%s schema/description hides the mutation outcome/full evidence contract", tool.Name)
		}
	}
}
