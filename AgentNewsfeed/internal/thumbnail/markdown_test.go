package thumbnail

import (
	"encoding/json"
	"maps"
	"os"
	"testing"
)

func TestMarkdownImageDestinationsResolveOnlyPersistedImageNodes(t *testing.T) {
	data, err := os.ReadFile("testdata/markdown-images.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceURL string            `json:"source_url"`
		Markdown  string            `json:"markdown"`
		Images    map[string]string `json:"images"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if got := MarkdownImages(fixture.SourceURL, fixture.Markdown); !maps.Equal(got, fixture.Images) {
		t.Fatalf("Markdown image destinations differ from the shared reader contract: got %v want %v", got, fixture.Images)
	}
}

func TestMarkdownImagesIgnoreNestedAltImagesAndInvalidSource(t *testing.T) {
	content := "![Outer ![Nested](https://nested.example/photo.png)](https://outer.example/photo.png)"
	images := MarkdownImages("https://publisher.example/original", content)
	if len(images) != 1 || images["https://outer.example/photo.png"] != "https://outer.example/photo.png" {
		t.Fatalf("nested alt text became a fetchable image: %v", images)
	}
	for _, source := range []string{"https://10.0.0.1/original", "http://publisher.example/original", "::bad-url"} {
		if images := MarkdownImages(source, "![relative](/photo.png)"); len(images) != 0 {
			t.Fatalf("invalid source created image routes: %s %v", source, images)
		}
	}
}
