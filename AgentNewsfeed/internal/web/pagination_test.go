package web_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/testfixture"
)

type paginationRecord struct {
	ID string `json:"id"`
}

type paginationList struct {
	Articles       []paginationRecord `json:"articles"`
	PreviousCursor string             `json:"previous_cursor"`
	NextCursor     string             `json:"next_cursor"`
	Total          int                `json:"total"`
}

type paginationShell struct {
	Cursor   string            `json:"cursor"`
	Before   string            `json:"before"`
	PageSize int               `json:"page_size"`
	Query    string            `json:"query"`
	Page     paginationList    `json:"page"`
	Article  *paginationRecord `json:"article"`
}

func TestBrowserSearchExpandingCaseBoundaryNavigation(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	cookie := f.Cookie(t, "alice")
	rawQuery := strings.Repeat("Ⱥ", 100)
	query := strings.Repeat("ⱥ", 100)
	for i := range 2 {
		_, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: fmt.Sprintf("case-boundary-%d", i), Article: feed.Content{
			URL: fmt.Sprintf("https://publisher.example/case-boundary-%d", i), Title: rawQuery, Summary: "Source introduction",
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	first := readBrowserJSON[paginationShell](t, listBrowserRequest(h, "/?q="+url.QueryEscape(rawQuery)+"&page_size=1", cookie, ""))
	if first.Query != query || first.Page.Total != 2 || len(first.Page.Articles) != 1 || first.Page.NextCursor == "" {
		t.Fatalf("raw expanding query did not return its canonical first page: %+v", first)
	}
	canonicalPath := "/?q=" + url.QueryEscape(first.Query) + "&page_size=1"
	canonical := readBrowserJSON[paginationShell](t, listBrowserRequest(h, canonicalPath, cookie, ""))
	if canonical.Query != query || !slices.Equal(canonical.Page.Articles, first.Page.Articles) {
		t.Fatal("canonical boundary query changed the first page")
	}
	nextPath := canonicalPath + "&cursor=" + url.QueryEscape(first.Page.NextCursor)
	next := readBrowserJSON[paginationShell](t, listBrowserRequest(h, nextPath, cookie, ""))
	if next.Query != query || next.Page.Total != 2 || len(next.Page.Articles) != 1 || next.Page.Articles[0].ID == first.Page.Articles[0].ID || next.Page.PreviousCursor == "" || next.Page.NextCursor != "" {
		t.Fatalf("canonical boundary query could not page forward: %+v", next)
	}
	readerPath := "/articles/" + next.Page.Articles[0].ID + "?from=feed&q=" + url.QueryEscape(next.Query) + "&page_size=1&cursor=" + url.QueryEscape(next.Cursor)
	reader := readBrowserJSON[paginationShell](t, listBrowserRequest(h, readerPath, cookie, ""))
	if reader.Query != query || reader.PageSize != 1 || reader.Cursor != next.Cursor || reader.Article == nil || reader.Article.ID != next.Page.Articles[0].ID {
		t.Fatal("reader lost the accepted boundary query or its return cursor")
	}
	backPath := "/?q=" + url.QueryEscape(reader.Query) + "&page_size=1&cursor=" + url.QueryEscape(reader.Cursor)
	back := readBrowserJSON[paginationShell](t, listBrowserRequest(h, backPath, cookie, ""))
	if !slices.Equal(back.Page.Articles, next.Page.Articles) {
		t.Fatal("reader return changed the boundary query's page")
	}
	previous := readBrowserJSON[paginationShell](t, listBrowserRequest(h, canonicalPath+"&before="+url.QueryEscape(next.Page.PreviousCursor), cookie, ""))
	if !slices.Equal(previous.Page.Articles, first.Page.Articles) {
		t.Fatal("canonical boundary query could not page backward")
	}
	for _, invalid := range []string{strings.Repeat("Ⱥ", 101), strings.Repeat("ⱥ", 101)} {
		for _, path := range []string{"/?q=", "/articles/" + reader.Article.ID + "?from=feed&q="} {
			if response := listBrowserRequest(h, path+url.QueryEscape(invalid)+"&page_size=1", cookie, ""); response.Code != 400 {
				t.Fatalf("over-bound raw/canonical query accepted on %s: HTTP %d", path, response.Code)
			}
		}
	}
}

func TestBrowserSearchURLsAndReaderActionsKeepContext(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	out, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "search-context", Article: feed.Content{
		URL: "https://publisher.example/native", Title: "Native's 50%_ [x]", Summary: "Source introduction",
	}})
	if err != nil {
		t.Fatal(err)
	}
	query := "  NATIVE'S  50%_ [x]  "
	path := "/?q=" + url.QueryEscape(query) + "&page_size=100"
	page := readBrowserJSON[paginationShell](t, listBrowserRequest(h, path, cookie, "20"))
	if page.PageSize != 100 || page.Query != "native's 50%_ [x]" || page.Page.Total != 1 || len(page.Page.Articles) != 1 {
		t.Fatalf("URL search did not override header or normalize query: %+v", page)
	}
	native := request(h, "GET", path, cookie, "")
	_, body, _ := strings.Cut(native.Body.String(), `data-bootstrap="`)
	body, _, _ = strings.Cut(body, `"`)
	var shell paginationShell
	if native.Code != 200 || json.Unmarshal([]byte(html.UnescapeString(body)), &shell) != nil || shell.PageSize != 100 || shell.Query != page.Query || shell.Page.Total != 1 {
		t.Fatal("native search and browser JSON selected different data")
	}
	for _, invalid := range []string{
		"/?q=a&q=b", "/?page_size=", "/?page_size=0", "/?page_size=501", "/?page_size=1.5", "/?page_size=%2B10",
		"/?page_size=10&page_size=20", "/?q=%ff", "/?q=%00", "/?cursor=", "/?before=",
	} {
		if response := listBrowserRequest(h, invalid, cookie, "10"); response.Code != 400 {
			t.Fatalf("invalid search URL accepted %s: %d", invalid, response.Code)
		}
	}
	for _, size := range []string{"0", "501", "1.5", "ten"} {
		if response := listBrowserRequest(h, "/", cookie, size); response.Code != 400 {
			t.Fatalf("invalid page-size header accepted %q", size)
		}
	}
	articlePath := "/articles/" + out.Article.ID
	returnTo := articlePath + "?from=feed&q=" + url.QueryEscape(page.Query) + "&page_size=100"
	form := url.Values{"csrf_token": {csrf}}
	for _, invalid := range []string{
		"/articles/other?from=feed", articlePath + "?from=feed&q=a&q=b", articlePath + "?from=feed&page_size=501",
	} {
		response := browserJSONRequest(h, "/api/v1"+articlePath+"/archive?return_to="+url.QueryEscape(invalid), cookie, form, testfixture.Origin)
		if response.Code != 400 {
			t.Fatalf("invalid action return accepted %q: %d", invalid, response.Code)
		}
	}
	if article, err := s.GetArticle(t.Context(), p, out.Article.ID); err != nil || article.Revision != 1 {
		t.Fatal("invalid return mutated article")
	}
	type destination struct {
		Location string `json:"location"`
	}
	archived := readBrowserJSON[destination](t, browserJSONRequest(h, "/api/v1"+articlePath+"/archive?return_to="+url.QueryEscape(returnTo), cookie, form, testfixture.Origin))
	if archived.Location != returnTo {
		t.Fatalf("archive lost reader context: %q", archived.Location)
	}
	reader := readBrowserJSON[paginationShell](t, listBrowserRequest(h, archived.Location, cookie, ""))
	if reader.Query != page.Query || reader.PageSize != 100 || reader.Article == nil {
		t.Fatal("reader could not reopen with preserved search")
	}
	trashForm := url.Values{"csrf_token": {csrf}, "expected_revision": {"2"}, "idempotency_key": {"search-trash"}}
	trashed := readBrowserJSON[destination](t, browserJSONRequest(h, "/api/v1"+articlePath+"/trash?return_to="+url.QueryEscape(returnTo), cookie, trashForm, testfixture.Origin))
	trashURL, _ := url.ParseRequestURI(trashed.Location)
	if trashURL.Query().Get("from") != "trash" || trashURL.Query().Get("q") != page.Query || trashURL.Query().Get("page_size") != "100" {
		t.Fatal("Trash lost query or page size")
	}
	readBrowserJSON[paginationShell](t, listBrowserRequest(h, trashed.Location, cookie, ""))
	trashForm.Set("expected_revision", "3")
	trashForm.Set("idempotency_key", "search-restore")
	restored := readBrowserJSON[destination](t, browserJSONRequest(h, "/api/v1"+articlePath+"/restore-trash?return_to="+url.QueryEscape(trashed.Location), cookie, trashForm, testfixture.Origin))
	restoreURL, _ := url.ParseRequestURI(restored.Location)
	if restoreURL.Query().Get("from") != "archive" || restoreURL.Query().Get("q") != page.Query || restoreURL.Query().Get("page_size") != "100" {
		t.Fatal("Restore lost original shelf, query or page size")
	}
	readBrowserJSON[paginationShell](t, listBrowserRequest(h, restored.Location, cookie, ""))
}

func listBrowserRequest(h http.Handler, path string, cookie *http.Cookie, pageSize string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", testfixture.Origin+path, nil)
	r.AddCookie(cookie)
	r.Header.Set("Accept", browserMediaType)
	r.Header.Set("X-Newsfeed-Page-Size", pageSize)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}

func seedPaginationView(t *testing.T, s *feed.Service, p identity.Principal, view string) {
	t.Helper()
	for i := range 8 {
		key := fmt.Sprintf("%s-%d", view, i)
		article, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: key, Article: feed.Content{
			URL: "https://publisher.example/" + key, Title: "Source story " + key, Summary: "Source introduction",
		}})
		if err != nil {
			t.Fatal(err)
		}
		switch view {
		case "archive":
			if _, err := s.SetArchived(t.Context(), p, article.Article.ID, true); err != nil {
				t.Fatal(err)
			}
		case "trash":
			if _, err := s.TrashArticle(t.Context(), p, feed.StateInput{
				ArticleID: article.Article.ID, ExpectedRevision: article.Article.Revision, IdempotencyKey: "trash-" + key,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestBrowserKeysetPagination(t *testing.T) {
	f, s, h := app(t)
	alice := f.Principal(t, "alice", allScopes)
	cookie, bob := f.Cookie(t, "alice"), f.Cookie(t, "bob")
	for _, view := range []string{"feed", "archive", "trash"} {
		seedPaginationView(t, s, alice, view)
	}
	// Force tied timestamps in the fixture so paging must use IDs in both directions.
	db, err := sql.Open("sqlite", f.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `UPDATE articles SET created_at=1,
		archived_at=CASE WHEN archived_at IS NOT NULL THEN 2 END,
		trashed_at=CASE WHEN trashed_at IS NOT NULL THEN 3 END`); err != nil {
		t.Fatal(err)
	}

	var stalePath string
	var staleRecords []paginationRecord
	for _, tc := range []struct{ path, api, otherView string }{
		{"/", "/api/v1/articles", "/archive"},
		{"/archive", "/api/v1/articles?archived=true", "/trash"},
		{"/trash", "/api/v1/articles?trashed=true", "/archive"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var api paginationList
			response := listBrowserRequest(h, tc.api, cookie, "5")
			if err := json.Unmarshal(response.Body.Bytes(), &api); err != nil || response.Code != 200 || len(api.Articles) != 8 || api.PreviousCursor != "" || api.NextCursor != "" {
				t.Fatalf("browser page size affected the API: %d, %v", response.Code, err)
			}
			want := api.Articles
			check := func(path, header string, size int, expected []paginationRecord, previous, next bool) paginationList {
				t.Helper()
				page := readBrowserJSON[paginationShell](t, listBrowserRequest(h, path, cookie, header))
				list := page.Page
				if page.PageSize != size || !slices.Equal(list.Articles, expected) || (list.PreviousCursor != "") != previous || (list.NextCursor != "") != next {
					t.Fatalf("unexpected page at %s with size %s: %+v", path, header, page)
				}
				return list
			}

			check(tc.path, "", 10, want, false, false)
			first := check(tc.path, "3", 3, want[:3], false, true)
			check(tc.path, "5", 5, want[:5], false, true)
			secondPath := tc.path + "?cursor=" + url.QueryEscape(first.NextCursor)
			second := check(secondPath, "3", 3, want[3:6], true, true)
			if response := listBrowserRequest(h, secondPath, cookie, "5"); response.Code != 400 {
				t.Fatal("cursor accepted a different page size")
			}
			lastPath := tc.path + "?cursor=" + url.QueryEscape(second.NextCursor)
			last := check(lastPath, "3", 3, want[6:], true, false)
			previousPath := tc.path + "?before=" + url.QueryEscape(last.PreviousCursor)
			previous := check(previousPath, "3", 3, want[3:6], true, true)
			check(tc.path+"?before="+url.QueryEscape(previous.PreviousCursor), "3", 3, want[:3], false, true)

			beforeQuery := "?before=" + url.QueryEscape(last.PreviousCursor)
			for _, invalid := range []struct {
				path   string
				cookie *http.Cookie
			}{
				{previousPath, bob}, {tc.otherView + beforeQuery, cookie},
				{previousPath + "&cursor=" + url.QueryEscape(first.NextCursor), cookie},
			} {
				if response := listBrowserRequest(h, invalid.path, invalid.cookie, "3"); response.Code != 400 {
					t.Fatalf("invalid reverse cursor accepted on %s: HTTP %d", invalid.path, response.Code)
				}
			}
			if tc.path == "/" {
				stalePath, staleRecords = lastPath, want[6:]
				readerPath := "/articles/" + want[3].ID + "?from=feed&page_size=3&before=" + url.QueryEscape(last.PreviousCursor)
				reader := readBrowserJSON[paginationShell](t, listBrowserRequest(h, readerPath, cookie, "5"))
				if reader.Article == nil || reader.Article.ID != want[3].ID || reader.Before != last.PreviousCursor || reader.PageSize != 3 {
					t.Fatal("reader did not keep its return cursor and page size")
				}
			}
		})
	}
	for _, row := range staleRecords {
		if _, err := s.SetArchived(t.Context(), alice, row.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	stale := readBrowserJSON[paginationShell](t, listBrowserRequest(h, stalePath, cookie, "3"))
	if stale.Cursor == "" || len(stale.Page.Articles) != 0 || stale.Page.PreviousCursor != "" || stale.Page.NextCursor != "" {
		t.Fatal("stale cursor did not return a recoverable empty page")
	}
}
