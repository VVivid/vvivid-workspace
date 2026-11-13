package feed_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

func TestNormalizeArticleQueryBoundsAndIdempotence(t *testing.T) {
	for _, tc := range []struct {
		name, query, want string
	}{
		{"ASCII boundary", strings.Repeat("A", 300), strings.Repeat("a", 300)},
		{"multibyte boundary", strings.Repeat("É", 150), strings.Repeat("é", 150)},
		{"expanding case boundary", strings.Repeat("Ⱥ", 100), strings.Repeat("ⱥ", 100)},
		{"case and whitespace", "\t  Ⱥ\u00a0KERNEL\n", "ⱥ kernel"},
		{"empty", " \t\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, err := feed.NormalizeArticleQuery(tc.query)
			if err != nil || query != tc.want {
				t.Fatalf("normalize: got %q, %v; want %q", query, err, tc.want)
			}
			again, err := feed.NormalizeArticleQuery(query)
			if err != nil || again != query {
				t.Fatalf("accepted query changed on revalidation: got %q, %v; want %q", again, err, query)
			}
		})
	}
	for _, query := range []string{
		strings.Repeat("Ⱥ", 101), strings.Repeat("ⱥ", 101),
		strings.Repeat("Ⱥ", 100) + "a", strings.Repeat("a", 301),
		strings.Repeat(" ", 301),
	} {
		if normalized, err := feed.NormalizeArticleQuery(query); !errors.Is(err, feed.ErrInvalid) || normalized != "" {
			t.Fatalf("accepted query beyond raw or normalized bound: %d raw bytes, %d normalized bytes, %v", len(query), len(normalized), err)
		}
	}
}

func TestBrowserSearchBeforePagingAndCursorIsolation(t *testing.T) {
	f, s, alice, bob := setup(t)
	add := func(p identity.Principal, key, title string) sqlite.Article {
		t.Helper()
		out, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: key, Article: feed.Content{
			URL: "https://publisher.example/" + key, Title: title, Summary: "A CAFÉ résumé from the source", ContentMarkdown: "bodyonlyneedle",
		}})
		if err != nil {
			t.Fatal(err)
		}
		return out.Article
	}
	for i := range 123 {
		title := "Other source story"
		if i%2 == 0 {
			title = "Kernel 50%_ [C++] Editor's source story"
		}
		add(alice, fmt.Sprintf("story-%03d", i), title)
	}
	add(bob, "foreign", "Kernel 50%_ [C++]")
	archived := add(alice, "archived", "Kernel 50%_ [C++]")
	if _, err := s.SetArchived(t.Context(), alice, archived.ID, true); err != nil {
		t.Fatal(err)
	}
	trashed := add(alice, "trashed", "Kernel 50%_ [C++]")
	if _, err := s.TrashArticle(t.Context(), alice, feed.StateInput{ArticleID: trashed.ID, ExpectedRevision: 1, IdempotencyKey: "trash-match"}); err != nil {
		t.Fatal(err)
	}
	query := "  kErNeL\t CAFÉ  "
	input := feed.BrowserListInput{ListInput: feed.ListInput{Limit: 10}, Query: query}
	first, err := s.BrowseArticles(t.Context(), alice, input, false)
	if err != nil || first.Total != 62 || first.Start != 1 || first.End != 10 || len(first.Articles) != 10 || first.PreviousCursor != "" || first.NextCursor == "" {
		t.Fatalf("first search page: %+v, %v", first, err)
	}
	for _, article := range first.Articles {
		if article.ContentMarkdown != "" || article.ArchivedAt != 0 || article.TrashedAt != 0 {
			t.Fatal("browser search returned body or another shelf")
		}
	}
	input.Cursor = first.NextCursor
	input.Query = "kernel café"
	second, err := s.BrowseArticles(t.Context(), alice, input, false)
	if err != nil || second.Total != 62 || second.Start != 11 || second.End != 20 || second.PreviousCursor == "" {
		t.Fatalf("next search page: %+v, %v", second, err)
	}
	input.Cursor = second.PreviousCursor
	previous, err := s.BrowseArticles(t.Context(), alice, input, true)
	if err != nil || !slices.Equal(previous.Articles, first.Articles) || previous.Start != 1 || previous.End != 10 {
		t.Fatalf("reverse search page: %+v, %v", previous, err)
	}
	for _, tc := range []struct {
		p  identity.Principal
		in feed.BrowserListInput
	}{
		{bob, feed.BrowserListInput{ListInput: feed.ListInput{Limit: 10, Cursor: first.NextCursor}, Query: query}},
		{alice, feed.BrowserListInput{ListInput: feed.ListInput{Limit: 10, Cursor: first.NextCursor, Archived: true}, Query: query}},
		{alice, feed.BrowserListInput{ListInput: feed.ListInput{Limit: 10, Cursor: first.NextCursor, Trashed: true}, Query: query}},
		{alice, feed.BrowserListInput{ListInput: feed.ListInput{Limit: 20, Cursor: first.NextCursor}, Query: query}},
		{alice, feed.BrowserListInput{ListInput: feed.ListInput{Limit: 10, Cursor: first.NextCursor}, Query: "kernel"}},
	} {
		if _, err := s.BrowseArticles(t.Context(), tc.p, tc.in, false); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("cursor escaped its query/owner/shelf/size: %v", err)
		}
	}
	if _, err := s.ListArticles(t.Context(), alice, feed.ListInput{Cursor: first.NextCursor}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatal("browser cursor accepted by MCP/API list")
	}
	for _, tc := range []struct {
		query             string
		archived, trashed bool
		want              int
	}{
		{"50%_ [C++] editor's", false, false, 62},
		{"publisher.example/story-001", false, false, 1},
		{"bodyonlyneedle", false, false, 0},
		{"' OR 1=1 --", false, false, 0},
		{"kernel café", true, false, 1},
		{"kernel café", false, true, 1},
	} {
		page, err := s.BrowseArticles(t.Context(), alice, feed.BrowserListInput{ListInput: feed.ListInput{Archived: tc.archived, Trashed: tc.trashed}, Query: tc.query}, false)
		if err != nil || page.Total != tc.want || len(page.Articles) != min(tc.want, 10) {
			t.Fatalf("search %q: total %d, rows %d, %v", tc.query, page.Total, len(page.Articles), err)
		}
	}
	if _, err := s.BrowseArticles(t.Context(), f.Principal(t, "alice", identity.ArticlesWrite), feed.BrowserListInput{}, false); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("write-only grant read search results")
	}
}

func TestBrowserArticlePageSizesAndValidation(t *testing.T) {
	_, s, alice, _ := setup(t)
	for i := range 505 {
		_, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: fmt.Sprintf("large-%d", i), Article: feed.Content{
			URL: fmt.Sprintf("https://publisher.example/large-%d", i), Title: "Synthetic size fixture", Summary: "Source introduction",
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []int{10, 20, 50, 100, 500} {
		page, err := s.BrowseArticles(t.Context(), alice, feed.BrowserListInput{ListInput: feed.ListInput{Limit: size}}, false)
		if err != nil || len(page.Articles) != size || page.Total != 505 || page.End != size || page.NextCursor == "" {
			t.Fatalf("page size %d: %+v, %v", size, page, err)
		}
	}
	for _, size := range []int{-1, 501} {
		if _, err := s.BrowseArticles(t.Context(), alice, feed.BrowserListInput{ListInput: feed.ListInput{Limit: size}}, false); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("invalid size %d accepted: %v", size, err)
		}
	}
	for _, query := range []string{
		strings.Repeat("a", 301), strings.Repeat("é", 151), strings.Repeat("Ⱥ", 101), strings.Repeat("ⱥ", 101),
		"invalid\x00query", string([]byte{0xff}),
	} {
		if _, err := s.BrowseArticles(t.Context(), alice, feed.BrowserListInput{Query: query}, false); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("invalid query accepted: %v", err)
		}
	}
	if _, err := s.ListArticles(t.Context(), alice, feed.ListInput{Limit: 100}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatal("browser page-size change widened MCP/API contract")
	}
}

func TestBrowserSearchStalePageKeepsCurrentCount(t *testing.T) {
	_, s, alice, _ := setup(t)
	for i := range 3 {
		if _, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: fmt.Sprintf("stale-%d", i), Article: feed.Content{
			URL: fmt.Sprintf("https://publisher.example/stale-%d", i), Title: "Stale fixture", Summary: "Source introduction",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	in := feed.BrowserListInput{ListInput: feed.ListInput{Limit: 2}, Query: "stale"}
	first, _ := s.BrowseArticles(t.Context(), alice, in, false)
	in.Cursor = first.NextCursor
	last, err := s.BrowseArticles(t.Context(), alice, in, false)
	if err != nil || len(last.Articles) != 1 {
		t.Fatalf("last page: %+v %v", last, err)
	}
	if _, err := s.SetArchived(t.Context(), alice, last.Articles[0].ID, true); err != nil {
		t.Fatal(err)
	}
	stale, err := s.BrowseArticles(t.Context(), alice, in, false)
	if err != nil || len(stale.Articles) != 0 || stale.Total != 2 || stale.Start != 0 || stale.End != 0 || stale.NextCursor != "" || stale.PreviousCursor != "" {
		t.Fatalf("stale page is not recoverable: %+v, %v", stale, err)
	}
}
