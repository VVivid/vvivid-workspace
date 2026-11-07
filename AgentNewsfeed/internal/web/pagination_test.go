package web_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
	Opportunities  []paginationRecord `json:"opportunities"`
	PreviousCursor string             `json:"previous_cursor"`
	NextCursor     string             `json:"next_cursor"`
}

func (p paginationList) records() []paginationRecord {
	if p.Articles != nil {
		return p.Articles
	}
	return p.Opportunities
}

type paginationShell struct {
	Cursor            string            `json:"cursor"`
	Before            string            `json:"before"`
	PageSize          int               `json:"page_size"`
	Section           string            `json:"section"`
	Page              paginationList    `json:"page"`
	OpportunitiesPage paginationList    `json:"opportunities_page"`
	Article           *paginationRecord `json:"article"`
}

func (p paginationShell) list() paginationList {
	if p.Section == "jobs" {
		return p.OpportunitiesPage
	}
	return p.Page
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
		job := saveBrowserOpportunity(t, s, p, key)
		switch view {
		case "archive":
			if _, err := s.SetArchived(t.Context(), p, article.Article.ID, true); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetOpportunityArchived(t.Context(), p, feed.ArchiveOpportunityInput{
				OpportunityID: job.ID, ExpectedRevision: job.Revision, Archived: true, IdempotencyKey: "archive-" + key,
			}); err != nil {
				t.Fatal(err)
			}
		case "trash":
			if _, err := s.TrashArticle(t.Context(), p, feed.StateInput{
				ArticleID: article.Article.ID, ExpectedRevision: article.Article.Revision, IdempotencyKey: "trash-" + key,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.TrashOpportunity(t.Context(), p, feed.OpportunityStateInput{
				OpportunityID: job.ID, ExpectedRevision: job.Revision, IdempotencyKey: "trash-" + key,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResponsiveBrowserPagination(t *testing.T) {
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
	for _, table := range []string{"articles", "opportunities"} {
		if _, err := db.ExecContext(t.Context(), "UPDATE "+table+` SET created_at=1,
			archived_at=CASE WHEN archived_at IS NOT NULL THEN 2 END,
			trashed_at=CASE WHEN trashed_at IS NOT NULL THEN 3 END`); err != nil {
			t.Fatal(err)
		}
	}

	var stalePath string
	var staleRecords []paginationRecord
	for _, tc := range []struct{ path, api, otherView, otherCollection string }{
		{"/", "/api/v1/articles", "/archive", "/jobs"},
		{"/archive", "/api/v1/articles?archived=true", "/trash", "/jobs/archive"},
		{"/trash", "/api/v1/articles?trashed=true", "/archive", "/jobs/trash"},
		{"/jobs", "/api/v1/opportunities", "/jobs/archive", "/"},
		{"/jobs/archive", "/api/v1/opportunities?archived=true", "/jobs/trash", "/archive"},
		{"/jobs/trash", "/api/v1/opportunities?trashed=true", "/jobs/archive", "/trash"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var api paginationList
			response := listBrowserRequest(h, tc.api, cookie, "5")
			if err := json.Unmarshal(response.Body.Bytes(), &api); err != nil || response.Code != 200 || len(api.records()) != 8 || api.PreviousCursor != "" || api.NextCursor != "" {
				t.Fatalf("browser page size affected the API: %d, %v", response.Code, err)
			}
			want := api.records()
			check := func(path, header string, size int, expected []paginationRecord, previous, next bool) paginationList {
				t.Helper()
				page := readBrowserJSON[paginationShell](t, listBrowserRequest(h, path, cookie, header))
				list := page.list()
				if page.PageSize != size || !slices.Equal(list.records(), expected) || (list.PreviousCursor != "") != previous || (list.NextCursor != "") != next {
					t.Fatalf("unexpected page at %s with size %s: %+v", path, header, page)
				}
				return list
			}

			first := check(tc.path, "50", 3, want[:3], false, true)
			check(tc.path, "5", 5, want[:5], false, true)
			secondPath := tc.path + "?cursor=" + url.QueryEscape(first.NextCursor)
			second := check(secondPath, "3", 3, want[3:6], true, true)
			check(secondPath, "5", 5, want[3:], true, false)
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
				{previousPath, bob}, {tc.otherView + beforeQuery, cookie}, {tc.otherCollection + beforeQuery, cookie},
				{previousPath + "&cursor=" + url.QueryEscape(first.NextCursor), cookie},
			} {
				if response := listBrowserRequest(h, invalid.path, invalid.cookie, "3"); response.Code != 400 {
					t.Fatalf("invalid reverse cursor accepted on %s: HTTP %d", invalid.path, response.Code)
				}
			}
			if tc.path == "/" {
				stalePath, staleRecords = lastPath, want[6:]
				readerPath := "/articles/" + want[3].ID + "?from=feed&before=" + url.QueryEscape(last.PreviousCursor)
				reader := readBrowserJSON[paginationShell](t, listBrowserRequest(h, readerPath, cookie, "5"))
				if reader.Article == nil || reader.Article.ID != want[3].ID || reader.Before != last.PreviousCursor || reader.PageSize != 0 {
					t.Fatal("reader did not keep its return cursor independently of the viewport")
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
	if stale.Cursor == "" || len(stale.Page.records()) != 0 || stale.Page.PreviousCursor != "" || stale.Page.NextCursor != "" {
		t.Fatal("stale cursor did not return a recoverable empty page")
	}
}
