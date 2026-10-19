package shopping

import (
	"net"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"agentshoppinglist/internal/store/sqlite"
)

func pageLimit(n, maximum int) (int, error) {
	if n == 0 {
		return min(20, maximum), nil
	}
	if n < 1 || n > maximum {
		return 0, invalid("limit must be between 1 and %d", maximum)
	}
	return n, nil
}

func validID(value string) bool {
	return plain(value, 128) && !strings.ContainsAny(value, " /\\?#")
}

func hasControl(value string) bool { return strings.ContainsFunc(value, unicode.IsControl) }

func plain(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && len(value) <= maximum && !hasControl(value)
}

func optionalText(value *string, maximum int) bool { return value == nil || plain(*value, maximum) }

// URLs are stored and returned to clients; the application never fetches them.
// Reject executable schemes, credential-bearing links and literal local targets.
// Any future server-side media fetch needs DNS and redirect checks of its own.
func sourceURL(raw string) bool {
	if len(raw) > 2048 || hasControl(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast()
	}
	return true
}

func observed(timestamp int64) bool {
	return timestamp > 0 && timestamp <= time.Now().Add(5*time.Minute).UnixMilli()
}

func verification(value string) bool { return value == "verified" || value == "unverified" }

func validateProduct(product sqlite.Product) error {
	if !plain(product.Name, 300) || !slices.Contains(categories, product.Category) {
		return invalid("product requires a bounded plain-text name and predefined category")
	}
	if len(product.Brand) > 150 || hasControl(product.Brand) || len(product.Flavor) > 150 || hasControl(product.Flavor) {
		return invalid("brand and flavor must be at most 150 plain-text bytes each")
	}
	if !optionalText(product.SourceKey, 200) {
		return invalid("source_key must identify one exact variant and contain at most 200 bytes")
	}
	if len(product.Description) > 4000 || strings.ContainsFunc(product.Description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return invalid("description must contain at most 4000 plain-text bytes")
	}
	if product.PackageCount != nil && (*product.PackageCount < 1 || *product.PackageCount > 10000) {
		return invalid("package count must be between 1 and 10000")
	}
	if (product.PackageSize == nil) != (product.PackageUnit == nil) {
		return invalid("package size and unit must be supplied together")
	}
	if product.PackageSize != nil && (!sqlite.PositiveDecimal(*product.PackageSize) || !plain(*product.PackageUnit, 32)) {
		return invalid("package size requires a positive decimal and bounded unit")
	}
	if err := validateFieldSources(product); err != nil {
		return err
	}
	if len(product.Media) > 4 {
		return invalid("at most 4 product images are allowed")
	}
	for _, media := range product.Media {
		if !sourceURL(media.ImageURL) || !sourceURL(media.SourceURL) || !observed(media.ObservedAt) {
			return invalid("media requires HTTPS image/source and an observation time")
		}
	}
	if product.Nutrition != nil {
		if err := validateNutrition(*product.Nutrition); err != nil {
			return err
		}
	}
	if len(product.Listings) > 8 {
		return invalid("at most 8 retailer listings are allowed")
	}
	for _, listing := range product.Listings {
		if err := validateListing(listing); err != nil {
			return err
		}
	}
	return nil
}

func validateFieldSources(product sqlite.Product) error {
	if len(product.Sources) < 1 || len(product.Sources) > 12 {
		return invalid("product needs 1–12 field sources")
	}
	allowed := []string{"name", "brand", "flavor", "category", "package", "description"}
	covered := map[string]bool{}
	for _, source := range product.Sources {
		if !sourceURL(source.URL) || !observed(source.ObservedAt) || !verification(source.Verification) || len(source.Fields) < 1 || len(source.Fields) > 6 {
			return invalid("field sources require HTTPS URL, observation, verification and field names")
		}
		for _, field := range source.Fields {
			if !slices.Contains(allowed, field) {
				return invalid("unknown provenance field")
			}
			covered[field] = true
		}
	}
	required := []string{"name", "category"}
	if product.Brand != "" {
		required = append(required, "brand")
	}
	if product.Flavor != "" {
		required = append(required, "flavor")
	}
	if product.PackageCount != nil || product.PackageSize != nil {
		required = append(required, "package")
	}
	if product.Description != "" {
		required = append(required, "description")
	}
	for _, field := range required {
		if !covered[field] {
			return invalid("missing source for %s", field)
		}
	}
	return nil
}

func validateNutrition(nutrition sqlite.Nutrition) error {
	if !sqlite.PositiveDecimal(nutrition.ServingSize) || !plain(nutrition.ServingUnit, 64) {
		return invalid("nutrition requires an explicit serving size and unit")
	}
	if !sourceURL(nutrition.SourceURL) || !observed(nutrition.ObservedAt) || !verification(nutrition.Verification) {
		return invalid("nutrition requires HTTPS source, observation and verification status")
	}
	if len(nutrition.Facts) < 1 || len(nutrition.Facts) > 32 {
		return invalid("nutrition requires 1–32 facts")
	}
	names := map[string]bool{}
	for _, fact := range nutrition.Facts {
		if !plain(fact.Name, 80) || !plain(fact.Unit, 32) || names[fact.Name] {
			return invalid("nutrition facts require unique names and bounded units")
		}
		if fact.Value != nil && !sqlite.NonnegativeDecimal(*fact.Value) {
			return invalid("nutrition values must be nonnegative decimals or null")
		}
		names[fact.Name] = true
	}
	return nil
}

func validateListing(listing sqlite.Listing) error {
	if !plain(listing.Retailer, 128) || !sourceURL(listing.ProductURL) || !optionalText(listing.ItemID, 128) || !optionalText(listing.StoreLocation, 200) {
		return invalid("listing requires a retailer, HTTPS product URL and bounded location/item identifiers")
	}
	if listing.PriceMinor != nil && (*listing.PriceMinor < 0 || *listing.PriceMinor > 999_999_999) {
		return invalid("observed price is outside supported minor-unit range")
	}
	if listing.Currency != nil && !sqlite.ValidCurrency(*listing.Currency) {
		return invalid("listing currency must be a supported ISO 4217 code")
	}
	if listing.ObservedAt != nil && !observed(*listing.ObservedAt) {
		return invalid("listing observation time is invalid")
	}
	if !slices.Contains([]string{"unknown", "in_stock", "out_of_stock"}, listing.Availability) {
		return invalid("availability must be unknown, in_stock or out_of_stock")
	}
	if listing.PriceMinor != nil && (listing.Currency == nil || listing.ObservedAt == nil) {
		return invalid("observed price requires currency and observation time")
	}
	if listing.Availability != "unknown" && (listing.ObservedAt == nil || listing.StoreLocation == nil) {
		return invalid("availability requires a checked store location and time")
	}
	return nil
}

func validateListInput(input SaveListInput) error {
	if !plain(input.Title, 200) || !optionalText(input.Retailer, 128) {
		return invalid("list requires a bounded title and optional retailer")
	}
	if input.ExpectedRevision < 0 || input.ExpectedSelectionRevision < 0 {
		return invalid("expected revisions cannot be negative")
	}
	if input.MakeActive && input.Archived {
		return invalid("an archived list cannot be selected")
	}
	if input.ExpectedRevision == 0 && input.ListID != "" || input.ExpectedRevision > 0 && !validID(input.ListID) {
		return invalid("create with an empty list_id and revision 0, or revise an existing id and positive revision")
	}
	return nil
}

func validateOperations(input UpdateItemsInput) error {
	if !validID(input.ListID) || input.ExpectedRevision < 1 {
		return invalid("a list and positive expected_revision are required")
	}
	if len(input.Operations) < 1 || len(input.Operations) > 50 {
		return invalid("an atomic batch must contain 1–50 operations")
	}
	for _, operation := range input.Operations {
		if !optionalText(operation.Retailer, 128) {
			return invalid("invalid retailer restriction")
		}
		if operation.Kind == "add" {
			if !validID(operation.ProductID) || operation.ItemID != "" || operation.ExpectedRevision != 0 || operation.PickedUp != nil {
				return invalid("add requires product_id and no item revision or picked_up field")
			}
			if operation.Quantity == nil || !sqlite.PositiveDecimal(*operation.Quantity) {
				return invalid("add requires a positive decimal quantity")
			}
			if operation.Merge != "increment" && operation.Merge != "keep" {
				return invalid("add requires explicit merge increment or keep")
			}
			continue
		}
		if !slices.Contains([]string{"set_quantity", "set_picked_up", "remove", "restore"}, operation.Kind) {
			return invalid("unknown operation kind")
		}
		if !validID(operation.ItemID) || operation.ExpectedRevision < 1 || operation.ProductID != "" || operation.Merge != "" || operation.Retailer != nil {
			return invalid("item operation requires item_id and expected_revision")
		}
		if operation.Kind == "set_quantity" {
			if operation.Quantity == nil || !sqlite.PositiveDecimal(*operation.Quantity) {
				return invalid("set_quantity requires a positive decimal")
			}
		} else if operation.Quantity != nil {
			return invalid("quantity is only accepted for add or set_quantity")
		}
		if operation.Kind == "set_picked_up" {
			if operation.PickedUp == nil {
				return invalid("set_picked_up requires an explicit boolean")
			}
		} else if operation.PickedUp != nil {
			return invalid("picked_up is only accepted for set_picked_up")
		}
	}
	return nil
}
