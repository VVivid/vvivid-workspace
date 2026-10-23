package testfixture

import (
	"time"

	"agentshoppinglist/internal/store/sqlite"
)

// Polar preserves the reference's exact package image and source URL. It is
// synthetic test data, not a new retailer check or a real purchase observation.
func Polar() sqlite.Product {
	source := "https://www.walmart.com/ip/187251201"
	observed := time.Now().UnixMilli()
	return sqlite.Product{
		Name: "Polar Black Cherry Seltzer", Brand: "Polar", Flavor: "Black Cherry", Category: "drinks", PackageCount: new(int64(8)), PackageSize: new("12"), PackageUnit: new("fl oz"), Description: "Unsweetened black cherry sparkling seltzer water.",
		Sources:   []sqlite.FieldSource{{Fields: []string{"name", "brand", "flavor", "category", "package", "description"}, URL: source, ObservedAt: observed, Verification: "unverified"}},
		Media:     []sqlite.Media{{ImageURL: "https://i5.walmartimages.com/seo/Polar-Zero-Calorie-Black-Cherry-Sparkling-Seltzer-Water-12-fl-oz-8-Pack-Cans_7763b7ab-8180-4dd8-9397-161757670549.2423cc4bb66c1c83976c4849892e6963.jpeg?odnBg=FFFFFF&odnHeight=573&odnWidth=573", SourceURL: source, ObservedAt: observed}},
		Nutrition: &sqlite.Nutrition{ServingSize: "12", ServingUnit: "fl oz can", Facts: []sqlite.NutritionFact{{Name: "Energy", Value: new("0"), Unit: "kcal"}, {Name: "Sugar", Value: new("0"), Unit: "g"}, {Name: "Protein", Value: nil, Unit: "g"}}, SourceURL: source, ObservedAt: observed, Verification: "unverified"},
		Listings:  []sqlite.Listing{{Retailer: "Walmart", ItemID: new("187251201"), ProductURL: source, Availability: "unknown"}},
	}
}
