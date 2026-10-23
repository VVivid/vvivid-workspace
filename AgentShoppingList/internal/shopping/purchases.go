package shopping

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
)

type SavePurchaseInput struct {
	WriteMeta
	Purchase         sqlite.PurchaseDraft `json:"purchase"`
	ExpectedRevision int64                `json:"expected_revision"`
}

type ConfirmPurchaseInput struct {
	WriteMeta
	PurchaseID               string `json:"purchase_id"`
	ExpectedRevision         int64  `json:"expected_revision"`
	ExpectedReplacedRevision int64  `json:"expected_replaced_revision"`
}

type VoidPurchaseInput struct {
	WriteMeta
	PurchaseID       string `json:"purchase_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

type SearchPurchasesInput struct {
	From            int64  `json:"from,omitzero"`
	To              int64  `json:"to,omitzero"`
	Retailer        string `json:"retailer,omitempty"`
	ProductID       string `json:"product_id,omitempty"`
	SourceReference string `json:"source_reference,omitempty"`
	Status          string `json:"status,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit,omitzero"`
}

type PurchasePage struct {
	Purchases  []sqlite.Purchase `json:"purchases"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (s *Service) ReorderableLines(ctx context.Context, p identity.Principal, purchaseIDs []string) (map[string]int64, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return nil, err
	}
	if len(purchaseIDs) > 50 {
		return nil, invalid("at most 50 purchase records can be checked for reordering")
	}
	for _, id := range purchaseIDs {
		if !validID(id) {
			return nil, invalid("invalid purchase_id")
		}
	}
	return s.store.ReorderableLines(ctx, p.Owner(), purchaseIDs)
}

func (s *Service) GetPurchase(ctx context.Context, p identity.Principal, id string) (sqlite.Purchase, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return sqlite.Purchase{}, err
	}
	if !validID(id) {
		return sqlite.Purchase{}, invalid("purchase_id is required")
	}
	return s.store.Purchase(ctx, p.Owner(), id)
}

func (s *Service) SearchPurchases(ctx context.Context, p identity.Principal, in SearchPurchasesInput) (PurchasePage, error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return PurchasePage{}, err
	}
	if in.From < 0 || in.To < 0 || in.To > 0 && in.To <= in.From {
		return PurchasePage{}, invalid("use nonnegative Unix milliseconds with from before exclusive to")
	}
	if in.Retailer != "" && !plain(in.Retailer, 128) || in.ProductID != "" && !validID(in.ProductID) || in.SourceReference != "" && !plain(in.SourceReference, 200) {
		return PurchasePage{}, invalid("invalid purchase search filter")
	}
	if !slices.Contains([]string{"", "draft", "confirmed", "void", "superseded"}, in.Status) {
		return PurchasePage{}, invalid("unknown purchase status")
	}
	limit, err := pageLimit(in.Limit, 50)
	if err != nil {
		return PurchasePage{}, err
	}
	f := sqlite.PurchaseFilter{
		From:            in.From,
		To:              in.To,
		Retailer:        in.Retailer,
		ProductID:       in.ProductID,
		SourceReference: in.SourceReference,
		Status:          in.Status,
		Limit:           limit + 1,
	}
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil || len(raw) > 160 {
			return PurchasePage{}, invalid("invalid purchase cursor")
		}
		stamp, id, ok := strings.Cut(string(raw), ":")
		f.BeforeTime, err = strconv.ParseInt(stamp, 10, 64)
		if !ok || err != nil || f.BeforeTime < 0 || !validID(id) {
			return PurchasePage{}, invalid("invalid purchase cursor")
		}
		f.BeforeID = id
	}
	records, err := s.store.SearchPurchases(ctx, p.Owner(), f)
	if err != nil {
		return PurchasePage{}, err
	}
	page := PurchasePage{Purchases: records}
	if len(records) > limit {
		page.Purchases = records[:limit]
		last := records[limit-1]
		var timestamp int64
		if last.PurchasedAt != nil {
			timestamp = *last.PurchasedAt
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d:%s", timestamp, last.ID))
	}
	return page, nil
}

func (s *Service) SavePurchase(ctx context.Context, p identity.Principal, in SavePurchaseInput) (WriteResult[PurchaseOutcome], error) {
	if err := s.require(ctx, p, identity.PurchasesWrite); err != nil {
		return WriteResult[PurchaseOutcome]{}, err
	}
	if in.ExpectedRevision < 0 || in.ExpectedRevision == 0 && in.Purchase.ID != "" || in.ExpectedRevision > 0 && !validID(in.Purchase.ID) {
		return WriteResult[PurchaseOutcome]{}, invalid("create with empty purchase.id and revision 0; revise a draft using its ID and current revision")
	}
	if err := validatePurchase(in.Purchase); err != nil {
		return WriteResult[PurchaseOutcome]{}, err
	}
	return writePurchase(s, ctx, p, in.WriteMeta, "save_purchase", in, func(tx *sqlite.Tx) (sqlite.Purchase, error) {
		draft := in.Purchase
		if draft.ID == "" {
			draft.ID = rand.Text()
		}
		return tx.SavePurchase(draft, in.ExpectedRevision)
	})
}

func (s *Service) ConfirmPurchase(ctx context.Context, p identity.Principal, in ConfirmPurchaseInput) (WriteResult[PurchaseOutcome], error) {
	if err := s.require(ctx, p, identity.PurchasesWrite); err != nil {
		return WriteResult[PurchaseOutcome]{}, err
	}
	if !validID(in.PurchaseID) || in.ExpectedRevision < 1 || in.ExpectedReplacedRevision < 0 {
		return WriteResult[PurchaseOutcome]{}, invalid("purchase_id and positive expected_revision required")
	}
	return writePurchase(s, ctx, p, in.WriteMeta, "confirm_purchase", in, func(tx *sqlite.Tx) (sqlite.Purchase, error) {
		// Validate persisted evidence again: older databases can contain foundation
		// fixtures, and no confirmation may manufacture their missing facts.
		purchase, err := tx.Purchase(in.PurchaseID)
		if err != nil {
			return sqlite.Purchase{}, err
		}
		if err := validatePurchase(draftFromPurchase(purchase)); err != nil {
			return sqlite.Purchase{}, err
		}
		if purchase.ReplacesID == nil && in.ExpectedReplacedRevision != 0 || purchase.ReplacesID != nil && in.ExpectedReplacedRevision < 1 {
			return sqlite.Purchase{}, invalid("replacement confirmation requires the current expected_replaced_revision; ordinary confirmation uses 0")
		}
		return tx.ConfirmPurchase(in.PurchaseID, in.ExpectedRevision, in.ExpectedReplacedRevision)
	})
}

func (s *Service) VoidPurchase(ctx context.Context, p identity.Principal, in VoidPurchaseInput) (WriteResult[PurchaseOutcome], error) {
	if err := s.require(ctx, p, identity.PurchasesWrite); err != nil {
		return WriteResult[PurchaseOutcome]{}, err
	}
	if !validID(in.PurchaseID) || in.ExpectedRevision < 1 || !plain(in.Reason, 1000) {
		return WriteResult[PurchaseOutcome]{}, invalid("purchase_id, positive expected_revision and a bounded void reason required")
	}
	return writePurchase(s, ctx, p, in.WriteMeta, "void_purchase", in, func(tx *sqlite.Tx) (sqlite.Purchase, error) {
		return tx.VoidPurchase(in.PurchaseID, in.ExpectedRevision, in.Reason)
	})
}

func draftFromPurchase(p sqlite.Purchase) sqlite.PurchaseDraft {
	draft := sqlite.PurchaseDraft{
		ID:             p.ID,
		Retailer:       p.Retailer,
		StoreLocation:  p.StoreLocation,
		SourceKind:     p.SourceKind,
		SourceEvidence: p.SourceEvidence,
		SourceURL:      p.SourceURL,
		ReplacesID:     p.ReplacesID,
		Lines:          make([]sqlite.PurchaseLineDraft, len(p.Lines)),
	}
	if p.PurchasedAt != nil {
		draft.PurchasedAt = *p.PurchasedAt
	}
	if p.Currency != nil {
		draft.Currency = *p.Currency
	}
	if p.SourceReference != nil {
		draft.SourceReference = *p.SourceReference
	}
	if p.SourceObservedAt != nil {
		draft.SourceObservedAt = *p.SourceObservedAt
	}
	for i, line := range p.Lines {
		draft.Lines[i] = line.PurchaseLineDraft
	}
	return draft
}

func validatePurchase(p sqlite.PurchaseDraft) error {
	if !plain(p.Retailer, 128) || !optionalText(p.StoreLocation, 200) || !sqlite.ValidCurrency(p.Currency) || !observed(p.PurchasedAt) {
		return invalid("purchase requires retailer, supported currency and a known purchase time in Unix milliseconds")
	}
	if !slices.Contains([]string{"receipt", "user_confirmation"}, p.SourceKind) || !plain(p.SourceReference, 200) || !plain(p.SourceEvidence, 2000) || !observed(p.SourceObservedAt) {
		return invalid("receipt or explicit user_confirmation requires a stable source_reference, evidence and observed time; checkoff alone is insufficient")
	}
	if p.SourceURL != nil && !sourceURL(*p.SourceURL) {
		return invalid("source_url must be a safe HTTPS URL or null")
	}
	if p.ReplacesID != nil && !validID(*p.ReplacesID) {
		return invalid("invalid replaces_id")
	}
	if len(p.Lines) < 1 || len(p.Lines) > 50 {
		return invalid("purchase requires 1–50 product lines")
	}
	linkedItems := map[string]bool{}
	linkedListRevisions := map[string]int64{}
	for _, line := range p.Lines {
		if !validID(line.ProductID) || line.ExpectedProductRevision < 1 || !sqlite.PositiveDecimal(line.Quantity) || !slices.Contains([]string{"pack", "each", "g", "kg", "oz", "lb", "ml", "l", "us_fl_oz"}, line.QuantityUnit) || !optionalText(line.SourceReference, 200) {
			return invalid("each line needs an owned product and expected_product_revision, exact positive quantity and an explicit supported quantity_unit")
		}
		if err := validatePurchaseMoney(line); err != nil {
			return err
		}
		if link := line.ListLink; link != nil {
			if p.ReplacesID != nil {
				return invalid("replacement does not reconcile lists again; omit list_link and use a separate explicit list mutation for any adjustment")
			}
			if line.QuantityUnit != "pack" || !validID(link.ListID) || !validID(link.ItemID) || link.ExpectedListRevision < 1 || link.ExpectedItemRevision < 1 || linkedItems[link.ItemID] {
				return invalid("list_link requires pack quantities, unique item IDs and current list/item revisions")
			}
			if revision, exists := linkedListRevisions[link.ListID]; exists && revision != link.ExpectedListRevision {
				return invalid("all links to one list must use the same expected_list_revision")
			}
			linkedItems[link.ItemID] = true
			linkedListRevisions[link.ListID] = link.ExpectedListRevision
		}
	}
	return nil
}

func validatePurchaseMoney(line sqlite.PurchaseLineDraft) error {
	for _, amount := range []*int64{line.UnitPriceMinor, line.LineTotalMinor, line.DiscountMinor, line.TaxMinor} {
		if amount != nil && (*amount < 0 || *amount > 999_999_999) {
			return invalid("line money must be null or 0–999999999 integer minor units")
		}
	}
	taxUnknown := line.TaxMinor == nil && line.TaxTreatment == "unknown"
	taxRecordedSeparately := line.TaxMinor != nil && line.TaxTreatment == "excluded"
	if !taxUnknown && !taxRecordedSeparately {
		return invalid("line_total_minor excludes tax; use tax_treatment excluded with a known separate tax_minor (including zero), or unknown with null tax")
	}
	if line.UnitPriceMinor != nil {
		if line.LineTotalMinor != nil && line.DiscountMinor == nil {
			return invalid("unit and line prices together require explicit allocated discount_minor, including zero")
		}
		quantity, _ := new(big.Rat).SetString(line.Quantity)
		gross := new(big.Rat).Mul(quantity, new(big.Rat).SetInt64(*line.UnitPriceMinor))
		whole, remainder := new(big.Int), new(big.Int)
		whole.QuoRem(gross.Num(), gross.Denom(), remainder)
		if new(big.Int).Mul(remainder, big.NewInt(2)).Cmp(gross.Denom()) >= 0 {
			whole.Add(whole, big.NewInt(1))
		}
		if line.DiscountMinor != nil {
			net := new(big.Int).Sub(whole, big.NewInt(*line.DiscountMinor))
			if net.Sign() < 0 {
				return invalid("allocated discount cannot exceed rounded unit price × quantity")
			}
			if line.LineTotalMinor != nil && (!net.IsInt64() || net.Int64() != *line.LineTotalMinor) {
				return invalid("line_total_minor must equal rounded unit price × quantity minus allocated discount; round fractional minor units half up")
			}
		}
	}
	return nil
}
