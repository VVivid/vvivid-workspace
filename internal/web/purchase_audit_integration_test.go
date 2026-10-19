package web_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
)

func TestReviewFiftyLinkedLineReplacement(t *testing.T) {
	_, service, principal, product, list := setup(t)
	operations := make([]sqlite.ItemOperation, 0, 49)
	for i := range 49 {
		variant := testfixture.Polar()
		variant.Name = fmt.Sprintf("%03d ", i) + strings.Repeat("n", 296)
		variant.Brand = strings.Repeat("b", 150)
		variant.Flavor = strings.Repeat("f", 150)
		saved, err := service.SaveProduct(t.Context(), principal, shopping.SaveProductInput{WriteMeta: meta(fmt.Sprintf("review-product-%d", i)), Product: variant})
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations, sqlite.ItemOperation{Kind: "add", ProductID: saved.Record.ID, Quantity: new("1"), Merge: "increment"})
	}
	added, err := service.UpdateItems(t.Context(), principal, shopping.UpdateItemsInput{WriteMeta: meta("review-items"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	list = added.Record
	originalDraft := purchaseDraft(product.ID, "review-50-lines")
	line := originalDraft.Lines[0]
	originalDraft.Lines = make([]sqlite.PurchaseLineDraft, len(list.Items))
	for i, item := range list.Items {
		originalDraft.Lines[i] = line
		originalDraft.Lines[i].ProductID = item.ProductID
		originalDraft.Lines[i].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: item.ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: item.Revision}
	}
	original := saveAndConfirm(t, service, principal, "review-original", originalDraft)
	b, _ := json.Marshal(original)
	t.Logf("original confirmed: %d lines, %d reconciliations, %d JSON bytes", len(original.Lines), len(original.Reconciliations), len(b))
	replacement := originalDraft
	replacement.ReplacesID = new(original.ID)
	for i := range replacement.Lines {
		replacement.Lines[i].ListLink = nil
	}
	saved, err := service.SavePurchase(t.Context(), principal, shopping.SavePurchaseInput{WriteMeta: meta("review-replacement-save"), Purchase: replacement})
	if err != nil {
		t.Fatal(err)
	}
	input := shopping.ConfirmPurchaseInput{WriteMeta: meta("review-replacement-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision, ExpectedReplacedRevision: original.Revision}
	_, err = service.ConfirmPurchase(t.Context(), principal, input)
	if err != nil {
		t.Fatalf("supported replacement confirmation failed: %v", err)
	}
}

func auditBoundaryText(pattern string, size int) string {
	return strings.Repeat(pattern, size/len(pattern)) + strings.Repeat("x", size%len(pattern))
}

func TestPurchaseAuditSupportedTextBoundary(t *testing.T) {
	for _, test := range []struct{ name, pattern string }{{"ascii", "n"}, {"html_escaped", "<"}, {"unicode", "茶"}} {
		t.Run(test.name, func(t *testing.T) {
			pattern := test.pattern
			f, service, principal, product, list := setup(t)
			// Every product reaches the documented name/brand/flavor byte limits.
			// The '<' case also exercises JSON's six-byte HTML escaping, including
			// maximum source URLs and receipt/line evidence, rather than ASCII only.
			sourcePrefix := "https://example.com/?evidence="
			sourceURL := sourcePrefix + auditBoundaryText(pattern, 2048-len(sourcePrefix))
			retailer := auditBoundaryText(pattern, 128)
			changedList, err := service.SaveList(t.Context(), principal, shopping.SaveListInput{WriteMeta: meta("boundary-retailer"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Title: "Boundary", Retailer: new(retailer)})
			if err != nil {
				t.Fatal(err)
			}
			list = changedList.Record
			operations := make([]sqlite.ItemOperation, 0, 49)
			for i := range 50 {
				variant := testfixture.Polar()
				variant.Name = auditBoundaryText(pattern, 300)
				variant.Brand = auditBoundaryText(pattern, 150)
				variant.Flavor = auditBoundaryText(pattern, 150)
				variant.Sources[0].URL = sourceURL
				input := shopping.SaveProductInput{WriteMeta: meta(fmt.Sprintf("boundary-product-%d", i)), Product: variant}
				if i == 0 {
					input.Product.ID, input.ExpectedRevision = product.ID, product.Revision
				}
				saved, err := service.SaveProduct(t.Context(), principal, input)
				if err != nil {
					t.Fatal(err)
				}
				if i > 0 {
					operations = append(operations, sqlite.ItemOperation{Kind: "add", ProductID: saved.Record.ID, Quantity: new("1"), Merge: "increment"})
				}
			}
			added, err := service.UpdateItems(t.Context(), principal, shopping.UpdateItemsInput{WriteMeta: meta("boundary-items"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: operations})
			if err != nil {
				t.Fatal(err)
			}
			list = added.Record
			draft := purchaseDraft(product.ID, auditBoundaryText(pattern, 200))
			draft.Retailer, draft.StoreLocation = retailer, new(auditBoundaryText(pattern, 200))
			draft.SourceEvidence, draft.SourceURL = auditBoundaryText(pattern, 2000), new(sourceURL)
			line := draft.Lines[0]
			line.SourceReference = new(auditBoundaryText(pattern, 200))
			line.UnitPriceMinor, line.LineTotalMinor, line.TaxMinor = new(int64(999_999_999)), new(int64(999_999_999)), new(int64(999_999_999))
			draft.Lines = make([]sqlite.PurchaseLineDraft, len(list.Items))
			for i, item := range list.Items {
				draft.Lines[i] = line
				draft.Lines[i].ProductID = item.ProductID
				draft.Lines[i].ExpectedProductRevision = item.Product.Revision
				draft.Lines[i].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: item.ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: item.Revision}
			}
			original := saveAndConfirm(t, service, principal, "boundary-original", draft)
			if len(original.Lines) != 50 || len(original.Reconciliations) != 50 || !original.ReconciliationApplied {
				t.Fatal("supported linked receipt lost lines or reconciliation")
			}
			for _, line := range original.Lines {
				if line.Name != auditBoundaryText(pattern, 300) || line.Brand != auditBoundaryText(pattern, 150) || line.Flavor != auditBoundaryText(pattern, 150) || len(line.Sources) != 1 || line.Sources[0].URL != sourceURL {
					t.Fatal("compact audit altered authoritative product/source snapshots")
				}
			}
			reconciledList, err := service.GetList(t.Context(), principal, shopping.ListInput{IncludeRemoved: true})
			if err != nil || reconciledList.List.Revision != list.List.Revision+1 || len(reconciledList.Items) != 50 {
				t.Fatalf("original reconciliation did not advance the list once: %v", err)
			}
			for _, item := range reconciledList.Items {
				if item.RemovedAt == nil || item.Revision != 2 {
					t.Fatal("original confirmation did not reconcile an item exactly once")
				}
			}
			replacement := draft
			replacement.ReplacesID = new(original.ID)
			replacement.Lines = slices.Clone(draft.Lines)
			for i := range replacement.Lines {
				replacement.Lines[i].ListLink = nil
				replacement.Lines[i].Quantity = "2"
				replacement.Lines[i].UnitPriceMinor, replacement.Lines[i].LineTotalMinor = new(int64(499_999_999)), new(int64(999_999_998))
			}
			saved, err := service.SavePurchase(t.Context(), principal, shopping.SavePurchaseInput{WriteMeta: meta("boundary-replacement-save"), Purchase: replacement})
			if err != nil {
				t.Fatal(err)
			}
			// A maximum-size draft update retains both mutable input versions.
			replacement.ID = saved.Record.ID
			replacement.SourceEvidence = "Revised " + auditBoundaryText(pattern, 2000-len("Revised "))
			saved, err = service.SavePurchase(t.Context(), principal, shopping.SavePurchaseInput{WriteMeta: meta("boundary-replacement-revise"), Purchase: replacement, ExpectedRevision: saved.Record.Revision})
			if err != nil {
				t.Fatal(err)
			}
			confirm := shopping.ConfirmPurchaseInput{WriteMeta: meta("boundary-replacement-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision, ExpectedReplacedRevision: original.Revision + 1}
			if _, err := service.ConfirmPurchase(t.Context(), principal, confirm); !errors.Is(err, sqlite.ErrConflict) {
				t.Fatalf("stale replacement revision accepted: %v", err)
			}
			confirm.ExpectedReplacedRevision = original.Revision
			corrected, err := service.ConfirmPurchase(t.Context(), principal, confirm)
			if err != nil {
				t.Fatalf("supported replacement confirmation failed: %v", err)
			}
			if corrected.Record.ReconciliationApplied || corrected.Record.OriginalReconciliationPurchase == nil || *corrected.Record.OriginalReconciliationPurchase != original.ID || !reflect.DeepEqual(corrected.Record.Reconciliations, original.Reconciliations) {
				t.Fatal("replacement lost inherited reconciliation evidence")
			}
			old, err := service.GetPurchase(t.Context(), principal, original.ID)
			if err != nil || old.Status != "superseded" || !reflect.DeepEqual(old.Lines, original.Lines) || old.SourceEvidence != draft.SourceEvidence {
				t.Fatal("replacement altered the original immutable receipt")
			}
			void := shopping.VoidPurchaseInput{WriteMeta: meta("boundary-void"), PurchaseID: corrected.Record.ID, ExpectedRevision: corrected.Record.Revision, Reason: auditBoundaryText(pattern, 1000)}
			voided, err := service.VoidPurchase(t.Context(), principal, void)
			if err != nil || voided.Record.Status != "void" || !reflect.DeepEqual(voided.Record.Lines, corrected.Record.Lines) {
				t.Fatalf("supported void failed or changed snapshots: %v", err)
			}
			replayed, err := service.ConfirmPurchase(t.Context(), principal, confirm)
			if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Record, corrected.Record) {
				t.Fatalf("replacement replay lost its original result: %v", err)
			}
			current, err := service.GetPurchase(t.Context(), principal, corrected.Record.ID)
			if err != nil || current.Status != "void" {
				t.Fatal("confirmation replay overwrote the later void")
			}
			currentList, err := service.GetList(t.Context(), principal, shopping.ListInput{IncludeRemoved: true})
			if err != nil || !reflect.DeepEqual(currentList, reconciledList) {
				t.Fatal("replacement, void or replay repeated list reconciliation")
			}
			server := httptest.NewServer(newHandler(t, f, service))
			defer server.Close()
			agent := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
			maxSummary, confirmations := 0, 0
			for cursor := ""; ; {
				page := resultRecord[shopping.ActivityPage](t, call(t, agent, "get_activity", shopping.ActivityInput{Cursor: cursor}))
				for _, event := range page.Events {
					raw, err := json.Marshal(event.Changes)
					if err != nil || len(raw) > 256<<10 {
						t.Fatalf("audit escaped its unchanged 256KiB bound: %d %v", len(raw), err)
					}
					maxSummary = max(maxSummary, len(raw))
					if event.RequestID == confirm.RequestID {
						confirmations++
						if len(event.Changes) != 2 {
							t.Fatal("replacement audit should contain only the two purchase state changes")
						}
						for _, change := range event.Changes {
							var after sqlite.PurchaseAudit
							if err := json.Unmarshal(change.After, &after); err != nil || change.Kind != "purchase" || after.Draft != nil || after.LineCount != 50 || after.ReconciliationCount != 50 {
								t.Fatalf("replacement audit lost immutable evidence references: %v", err)
							}
						}
						if strings.Contains(string(raw), "name_snapshot") || strings.Contains(string(raw), `"reconciliations"`) {
							t.Fatal("replacement audit duplicated immutable catalog/reconciliation snapshots")
						}
					}
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
			if confirmations != 1 {
				t.Fatalf("failed/replayed confirmations created extra audit events: %d", confirmations)
			}
			t.Logf("50 linked lines, maximum text, draft revision/replacement/void/replay: largest audit %d bytes", maxSummary)
		})
	}
}

func TestPurchaseAuditPreservesDraftInputsAndLegacyActivity(t *testing.T) {
	f, service, principal, product, _ := setup(t)
	initial := purchaseDraft(product.ID, "draft-audit-evidence")
	initial.SourceEvidence = `Original receipt text <data> with "quotes".`
	initial.SourceURL = new("https://example.com/?receipt=original&part=1")
	initial.Lines[0].SourceReference = new("original-line")
	saved, err := service.SavePurchase(t.Context(), principal, shopping.SavePurchaseInput{WriteMeta: meta("audit-draft-initial"), Purchase: initial})
	if err != nil {
		t.Fatal(err)
	}
	initial.ID = saved.Record.ID
	revised := initial
	revised.Lines = slices.Clone(initial.Lines)
	revised.Lines[0].Quantity, revised.Lines[0].LineTotalMinor = "2", new(int64(1198))
	revised.Lines[0].SourceReference = new("revised-line")
	revised.SourceEvidence, revised.SourceURL = "Corrected receipt evidence", new("https://example.com/?receipt=revised")
	saved, err = service.SavePurchase(t.Context(), principal, shopping.SavePurchaseInput{WriteMeta: meta("audit-draft-revise"), Purchase: revised, ExpectedRevision: saved.Record.Revision})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := service.ConfirmPurchase(t.Context(), principal, shopping.ConfirmPurchaseInput{WriteMeta: meta("audit-draft-confirm"), PurchaseID: saved.Record.ID, ExpectedRevision: saved.Record.Revision})
	if err != nil {
		t.Fatal(err)
	}
	legacyBefore, err := service.GetPurchase(t.Context(), principal, confirmed.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	voided, err := service.VoidPurchase(t.Context(), principal, shopping.VoidPurchaseInput{WriteMeta: meta("audit-draft-void"), PurchaseID: confirmed.Record.ID, ExpectedRevision: confirmed.Record.Revision, Reason: "Synthetic cancellation"})
	if err != nil {
		t.Fatal(err)
	}
	// Persist the exact pre-correction shape in a synthetic historical event.
	// Production Activity must preserve that JSON and the SDK must still accept it.
	legacyAfter, err := service.GetPurchase(t.Context(), principal, voided.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyBefore.Lines, legacyAfter.Lines = slices.Clone(legacyBefore.Lines), slices.Clone(legacyAfter.Lines)
	for i := range legacyBefore.Lines {
		legacyBefore.Lines[i].Sources, legacyAfter.Lines[i].Sources = nil, nil
	}
	legacyJSON, err := json.Marshal(legacyBefore)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Store.Write(t.Context(), principal.Owner(), sqlite.Mutation{Key: "legacy-purchase-audit", Operation: "void_purchase", Payload: []byte(`{}`), RequestID: "request-legacy-purchase-audit", ClientID: principal.ClientID()}, func(tx *sqlite.Tx) ([]byte, error) {
		return []byte(`{}`), tx.RecordChange("purchase", legacyAfter.ID, legacyBefore, legacyAfter)
	})
	if err != nil {
		t.Fatal(err)
	}
	activity, err := service.GetActivity(t.Context(), principal, shopping.ActivityInput{})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, event := range activity.Events {
		if len(event.Changes) != 1 || event.Changes[0].Kind != "purchase" {
			continue
		}
		change := event.Changes[0]
		if event.RequestID == "request-legacy-purchase-audit" {
			if !bytes.Equal(change.Before, legacyJSON) {
				t.Fatal("reading activity rewrote an existing audit snapshot")
			}
			found["legacy"] = true
			continue
		}
		var before, after *sqlite.PurchaseAudit
		if err := json.Unmarshal(change.Before, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(change.After, &after); err != nil || after == nil || after.ID != initial.ID || after.LineCount != 1 {
			t.Fatalf("audit lost the purchase reference: %v", err)
		}
		switch event.RequestID {
		case "request-audit-draft-initial":
			if before != nil || !reflect.DeepEqual(after.Draft, &initial) {
				t.Fatal("initial audit lost submitted draft evidence")
			}
			found["initial"] = true
		case "request-audit-draft-revise":
			if before == nil || !reflect.DeepEqual(before.Draft, &initial) || !reflect.DeepEqual(after.Draft, &revised) || before.Revision != 1 || after.Revision != 2 {
				t.Fatal("draft revision audit lost original or corrected input")
			}
			found["revised"] = true
		case "request-audit-draft-confirm":
			if before == nil || !reflect.DeepEqual(before.Draft, &revised) || after.Draft != nil || after.Status != "confirmed" || after.Revision != confirmed.Record.Revision {
				t.Fatal("confirmation audit lost accepted inputs or immutable snapshot reference")
			}
			found["confirmed"] = true
		case "request-audit-draft-void":
			if before == nil || before.Draft != nil || before.Status != "confirmed" || after.Draft != nil || after.Status != "void" || after.VoidReason == nil || *after.VoidReason != "Synthetic cancellation" {
				t.Fatal("void audit lost state transition or reason")
			}
			found["void"] = true
		}
	}
	if len(found) != 5 {
		t.Fatalf("missing purchase audit evidence: %v", found)
	}
	server := httptest.NewServer(newHandler(t, f, service))
	defer server.Close()
	agent := mcpClient(t, server.URL, f.Bearer("alice", "shopping-codex", f.Manager.MCPAudience(), allScopes))
	sdkActivity := resultRecord[shopping.ActivityPage](t, call(t, agent, "get_activity", shopping.ActivityInput{}))
	if len(sdkActivity.Events) != len(activity.Events) {
		t.Fatal("SDK activity omitted persisted events")
	}
	foundLegacy := false
	for _, event := range sdkActivity.Events {
		if event.RequestID != "request-legacy-purchase-audit" {
			continue
		}
		var before, after sqlite.Purchase
		if err := json.Unmarshal(event.Changes[0].Before, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(event.Changes[0].After, &after); err != nil || !reflect.DeepEqual(before, legacyBefore) || !reflect.DeepEqual(after, legacyAfter) {
			t.Fatalf("SDK rejected or changed legacy purchase audit snapshots: %v", err)
		}
		foundLegacy = true
	}
	if !foundLegacy {
		t.Fatal("SDK activity omitted the historical full-snapshot shape")
	}
	bobActivity, err := service.GetActivity(t.Context(), f.Principal(t, "bob", allScopes), shopping.ActivityInput{})
	if err != nil || len(bobActivity.Events) != 0 {
		t.Fatal("purchase audit crossed owner boundaries")
	}
}
