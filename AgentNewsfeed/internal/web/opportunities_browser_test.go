package web_test

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
	"agentnewsfeed/internal/thumbnail"
	"agentnewsfeed/internal/web"
)

type jobsShell struct {
	Configured        bool                    `json:"configured"`
	Authenticated     bool                    `json:"authenticated"`
	CanWrite          bool                    `json:"can_write"`
	CSRF              string                  `json:"csrf"`
	Section           string                  `json:"section"`
	View              string                  `json:"view"`
	Cursor            string                  `json:"cursor"`
	PageSize          int                     `json:"page_size"`
	DetailSelected    bool                    `json:"detail_selected"`
	MutationKey       string                  `json:"mutation_key"`
	SignInURL         string                  `json:"sign_in_url"`
	OpportunitiesPage *feed.OpportunitiesPage `json:"opportunities_page"`
	Opportunity       *sqlite.Opportunity     `json:"opportunity"`
}

func readJobsShell(t *testing.T, response *httptest.ResponseRecorder) jobsShell {
	t.Helper()
	_, rest, found := strings.Cut(response.Body.String(), `data-bootstrap="`)
	encoded, _, closed := strings.Cut(rest, `"`)
	if !found || !closed {
		t.Fatalf("missing bootstrap in HTTP %d", response.Code)
	}
	var shell jobsShell
	if err := json.Unmarshal([]byte(html.UnescapeString(encoded)), &shell); err != nil {
		t.Fatal(err)
	}
	return shell
}

func browserOpportunity(key string) feed.OpportunityContent {
	return feed.OpportunityContent{
		Kind: "internship", URL: "https://careers.example/" + key, Title: "Source role " + key, Company: "Synthetic Company",
		Location: "Boston, MA", Workplace: "hybrid", EmploymentType: "full_time", EmploymentPeriod: "Summer 2027 · 12 weeks",
		Compensation: "$42 per hour", Deadline: "2026-12-15", Tags: []string{"Go", "Systems"},
		SourceDescription: "# Source description\n\nOriginal wording.\n\n<script>untrusted()</script>\n\n![Source image](https://images.example/source.png)",
		AgentNote:         "A distinct agent assessment.",
	}
}

func saveBrowserOpportunity(t *testing.T, s *feed.Service, p identity.Principal, key string) sqlite.Opportunity {
	t.Helper()
	out, err := s.AddOpportunity(t.Context(), p, feed.AddOpportunityInput{IdempotencyKey: key, Opportunity: browserOpportunity(key)})
	if err != nil {
		t.Fatal(err)
	}
	return out.Opportunity
}

func TestJobsBrowserSharesPersistedListingsWithoutChangingNews(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	for i := range 22 {
		saveBrowserOpportunity(t, s, p, fmt.Sprintf("job-%02d", i))
	}
	news, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "news", Article: feed.Content{
		URL: "https://publisher.example/story", Title: "An unchanged News title", Summary: "A source introduction", ContentMarkdown: "# Original News body",
	}})
	if err != nil {
		t.Fatal(err)
	}
	api := request(h, "GET", "/api/v1/opportunities", cookie, "")
	var page feed.OpportunitiesPage
	if err := json.Unmarshal(api.Body.Bytes(), &page); err != nil || api.Code != 200 || len(page.Opportunities) != 20 || page.NextCursor == "" {
		t.Fatalf("persisted opportunity page failed: %d %v", api.Code, err)
	}
	for _, o := range page.Opportunities {
		if o.SourceDescription != "" || o.AgentNote != "" {
			t.Fatal("list included source description or note")
		}
	}
	response := request(h, "GET", "/jobs", cookie, "")
	initial := readJobsShell(t, response)
	if response.Code != 200 || initial.Section != "jobs" || initial.View != "feed" || !initial.Authenticated || !initial.CanWrite || initial.CSRF == "" || initial.DetailSelected ||
		initial.Opportunity == nil || initial.Opportunity.ID != page.Opportunities[0].ID || initial.PageSize != 3 || initial.OpportunitiesPage == nil ||
		!reflect.DeepEqual(initial.OpportunitiesPage.Opportunities, page.Opportunities[:3]) || initial.OpportunitiesPage.NextCursor == "" || len(initial.MutationKey) < 26 {
		t.Fatal("Jobs bootstrap did not select first saved listing or preserve page/session")
	}
	if initial.Opportunity.SourceDescription != browserOpportunity("unused").SourceDescription || initial.Opportunity.AgentNote == "" || strings.Contains(response.Body.String(), "<script>untrusted()") {
		t.Fatal("selected full source/note missing or bootstrap escaping lost")
	}
	secondPath := "/jobs?cursor=" + url.QueryEscape(page.NextCursor)
	second := readJobsShell(t, request(h, "GET", secondPath, cookie, ""))
	if second.Cursor != page.NextCursor || len(second.OpportunitiesPage.Opportunities) != 2 || second.OpportunitiesPage.NextCursor != "" {
		t.Fatal("browser list lost page cursor")
	}
	selectedID := second.OpportunitiesPage.Opportunities[1].ID
	deepPath := "/jobs/" + selectedID + "?from=feed&cursor=" + url.QueryEscape(page.NextCursor)
	deep := readJobsShell(t, request(h, "GET", deepPath, cookie, ""))
	if !deep.DetailSelected || deep.Cursor != page.NextCursor || deep.Opportunity.ID != selectedID || !reflect.DeepEqual(deep.OpportunitiesPage, second.OpportunitiesPage) {
		t.Fatal("selected detail changed list context")
	}
	detail := request(h, "GET", "/api/v1/opportunities/"+selectedID, cookie, "")
	var got sqlite.Opportunity
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil || detail.Code != 200 || !reflect.DeepEqual(got, *deep.Opportunity) {
		t.Fatal("browser full detail did not retain every persisted field")
	}
	readOnly := readJobsShell(t, request(h, "GET", deepPath, f.Cookie(t, "alice"), ""))
	if readOnly.CanWrite || !readOnly.Authenticated || readOnly.Opportunity.ID != selectedID {
		t.Fatal("read-only sign-in lost detail or gained actions")
	}
	for _, path := range []string{"/", "/archive", "/trash", "/articles/" + news.Article.ID} {
		response := request(h, "GET", path, cookie, "")
		shell := readJobsShell(t, response)
		if response.Code != 200 || shell.Section != "news" || shell.OpportunitiesPage != nil || shell.Opportunity != nil || strings.Contains(response.Body.String(), "Synthetic Company") {
			t.Fatalf("Jobs changed News bootstrap on %s", path)
		}
	}
	newsDetail := request(h, "GET", "/api/v1/articles/"+news.Article.ID, cookie, "")
	if newsDetail.Code != 200 || !strings.Contains(newsDetail.Body.String(), "Original News body") {
		t.Fatal("News reader body changed")
	}
	for _, path := range []string{"/jobs/archive?cursor=" + url.QueryEscape(page.NextCursor), "/jobs/trash?cursor=" + url.QueryEscape(page.NextCursor)} {
		if response := request(h, "GET", path, cookie, ""); response.Code != 400 {
			t.Fatalf("cursor crossed Jobs views: %s", path)
		}
	}
	if response := request(h, "GET", secondPath, f.Cookie(t, "bob"), ""); response.Code != 400 {
		t.Fatal("cursor crossed owners")
	}
	if response := request(h, "GET", "/api/v1/articles?cursor="+url.QueryEscape(page.NextCursor), cookie, ""); response.Code != 400 {
		t.Fatal("Jobs cursor crossed into News")
	}
}

func TestJobsBrowserBoundariesAndSafeRequestErrors(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	o := saveBrowserOpportunity(t, s, p, "private-job")
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	bob := f.Cookie(t, "bob")
	for _, path := range []string{"/jobs/" + o.ID, "/api/v1/opportunities/" + o.ID, "/jobs/unknown-listing", "/api/v1/opportunities/unknown-listing"} {
		response := request(h, "GET", path, bob, "")
		if response.Code != 404 || response.Body.String() != "Listing not found.\n" {
			t.Fatalf("unknown and foreign IDs differ on %s: %d", path, response.Code)
		}
	}
	for _, path := range []string{"/jobs", "/jobs/archive", "/jobs/trash", "/jobs/" + o.ID + "?from=trash&cursor=opaque"} {
		response := request(h, "GET", path, nil, "")
		shell := readJobsShell(t, response)
		if response.Code != 200 || !shell.Configured || shell.Authenticated || shell.Opportunity != nil || shell.OpportunitiesPage != nil || shell.SignInURL != "/auth/login?return_to="+url.QueryEscape(path) || strings.Contains(response.Body.String(), o.Title) {
			t.Fatal("anonymous gate exposed a listing or lost return path")
		}
	}
	for _, path := range []string{"/api/v1/opportunities", "/api/v1/opportunities/" + o.ID} {
		for _, token := range []string{"", f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.ArticlesWrite)} {
			if response := request(h, "GET", path, nil, token); response.Code != 401 {
				t.Fatal("opportunity browser read accepted no session or a write-only bearer")
			}
		}
	}
	for _, path := range []string{
		"/jobs?cursor=invalid", "/jobs?cursor=a&cursor=b", "/jobs?cursor=%zz", "/jobs?from=trash", "/jobs/" + o.ID + "?from=invalid", "/jobs/" + o.ID + "?from=feed&from=trash",
		"/api/v1/opportunities?limit=0", "/api/v1/opportunities?limit=51", "/api/v1/opportunities?limit=bad", "/api/v1/opportunities?limit=1&limit=2",
		"/api/v1/opportunities?archived=true&trashed=true", "/api/v1/opportunities?archived=yes", "/api/v1/opportunities?trashed=1", "/api/v1/opportunities?owner=someone", "/api/v1/opportunities?cursor=%zz",
		"/api/v1/opportunities/" + o.ID + "?trashed=invalid", "/api/v1/opportunities/" + o.ID + "?trashed=true&trashed=false", "/api/v1/opportunities/" + o.ID + "?trashed=%zz",
	} {
		response := request(h, "GET", path, cookie, "")
		if response.Code != 400 || response.Body.String() != "Invalid listing request.\n" {
			t.Fatalf("invalid listing request did not fail safely: %s %d %s", path, response.Code, response.Body.String())
		}
	}
	if response := request(h, "POST", "/api/v1/opportunities", cookie, ""); response.Code != 405 {
		t.Fatal("browser could create listings")
	}
	policy, _ := thumbnail.NewPolicy("")
	unconfigured, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, feed.New(f.Store, policy), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/jobs", "/jobs/archive", "/jobs/trash", "/jobs/owned", "/api/v1/opportunities"} {
		if response := request(unconfigured, "GET", path, nil, ""); response.Code != 503 {
			t.Fatalf("unconfigured Jobs path did not fail closed: %s", path)
		}
	}
}

func TestJobsBrowserFormsPreserveStateRevisionAndDurableRetries(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	o := saveBrowserOpportunity(t, s, p, "state-job")
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	bob := f.CookieWithScopes(t, "bob", allScopes)
	_, bobCSRF := f.BrowserPrincipal(t, bob)
	readOnly := f.Cookie(t, "alice")
	_, readCSRF := f.BrowserPrincipal(t, readOnly)
	base := "/api/v1/opportunities/" + o.ID
	for _, action := range []string{"archive", "unarchive", "trash", "restore"} {
		for i, tc := range []struct {
			cookie       *http.Cookie
			csrf, origin string
			status       int
		}{{nil, csrf, testfixture.Origin, 401}, {bob, bobCSRF, testfixture.Origin, 404}, {readOnly, readCSRF, testfixture.Origin, 403},
			{cookie, "wrong", testfixture.Origin, 403}, {cookie, csrf, "", 403}, {cookie, csrf, "https://attacker.example", 403}} {
			response := stateForm(h, base+"/"+action, tc.cookie, tc.csrf, fmt.Sprintf("rejected-%s-%d", action, i), 1, tc.origin)
			if response.Code != tc.status {
				t.Fatalf("%s auth/CSRF boundary: %d, want %d", action, response.Code, tc.status)
			}
		}
		if response := request(h, "GET", base+"/"+action, cookie, ""); response.Code != 405 {
			t.Fatalf("GET could mutate %s", action)
		}
	}
	post := func(action, key string, revision int64, view string) {
		t.Helper()
		response := stateForm(h, base+"/"+action, cookie, csrf, key, revision, testfixture.Origin)
		location := "/jobs/" + o.ID + "?from=" + view + "#opportunity-detail"
		if response.Code != 303 || response.Header().Get("Location") != location {
			t.Fatalf("%s failed or redirected incorrectly: %d %q", action, response.Code, response.Header().Get("Location"))
		}
	}
	current := func(trashed bool) sqlite.Opportunity {
		t.Helper()
		got, err := s.GetOpportunity(t.Context(), p, feed.GetOpportunityInput{OpportunityID: o.ID, Trashed: trashed})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	post("archive", "archive", 1, "archive")
	archived := current(false)
	if archived.Revision != 2 || archived.ArchivedAt == 0 || archived.SourceDescription != o.SourceDescription {
		t.Fatal("archive lost content or revision")
	}
	post("archive", "archive", 1, "archive")
	if !reflect.DeepEqual(current(false), archived) {
		t.Fatal("exact archive retry changed stored state")
	}
	for _, path := range []string{"/jobs/archive", "/jobs/" + o.ID + "?from=archive", "/api/v1/opportunities?archived=true"} {
		if response := request(h, "GET", path, cookie, ""); response.Code != 200 || !strings.Contains(html.UnescapeString(response.Body.String()), o.ID) {
			t.Fatalf("archived listing unavailable on %s", path)
		}
	}
	if response := stateForm(h, base+"/unarchive", cookie, csrf, "unarchive", 1, testfixture.Origin); response.Code != 409 || !reflect.DeepEqual(current(false), archived) {
		t.Fatal("stale form changed archived listing")
	}
	post("unarchive", "unarchive", 2, "feed")
	post("archive", "archive-again", 3, "archive")
	archived = current(false)
	post("trash", "trash-archive", 4, "trash")
	trashed := current(true)
	if trashed.Revision != 5 || trashed.TrashedAt == 0 || trashed.ArchivedAt != archived.ArchivedAt || trashed.CreatedAt != o.CreatedAt {
		t.Fatal("Trash lost original archive/saved state")
	}
	for _, path := range []string{"/jobs/" + o.ID, base} {
		if response := request(h, "GET", path, cookie, ""); response.Code != 404 {
			t.Fatal("ordinary read exposed Trash")
		}
	}
	for _, path := range []string{"/jobs/trash", "/jobs/" + o.ID + "?from=trash", base + "?trashed=true"} {
		if response := request(h, "GET", path, cookie, ""); response.Code != 200 || !strings.Contains(html.UnescapeString(response.Body.String()), o.ID) {
			t.Fatalf("explicit Trash read failed: %s", path)
		}
	}
	if response := stateForm(h, base+"/restore", cookie, csrf, "restore", 4, testfixture.Origin); response.Code != 409 {
		t.Fatal("stale Trash restore succeeded")
	}
	post("restore", "restore", 5, "archive")
	restored := current(false)
	if restored.Revision != 6 || restored.TrashedAt != 0 || restored.ArchivedAt != archived.ArchivedAt {
		t.Fatal("restore did not preserve Archive")
	}
	post("trash", "trash-archive", 4, "archive")
	if !reflect.DeepEqual(current(false), restored) {
		t.Fatal("old Trash retry reapplied stale state")
	}
	post("unarchive", "active-again", 6, "feed")
	post("trash", "trash-active", 7, "trash")
	post("restore", "restore-active", 8, "feed")
	active := current(false)
	if active.Revision != 9 || active.ArchivedAt != 0 || active.TrashedAt != 0 {
		t.Fatal("active restore changed previous state")
	}
	for _, revision := range []int64{0, -1} {
		if response := stateForm(h, base+"/trash", cookie, csrf, "invalid-revision", revision, testfixture.Origin); response.Code != 400 {
			t.Fatal("invalid form revision did not map to 400")
		}
	}
	post("trash", "invalid-revision", 9, "trash")
	post("restore", "final-restore", 10, "feed")
	beforeRestart := current(false)
	for _, values := range []url.Values{
		{"csrf_token": {csrf}, "expected_revision": {"11"}},
		{"csrf_token": {csrf}, "expected_revision": {"11", "11"}, "idempotency_key": {"invalid-fields"}},
		{"csrf_token": {csrf}, "expected_revision": {"11"}, "idempotency_key": {"first", "second"}},
		{"csrf_token": {csrf}, "expected_revision": {"11"}, "idempotency_key": {"invalid-fields"}, "extra": {"unknown"}},
	} {
		req := httptest.NewRequest("POST", testfixture.Origin+base+"/trash", strings.NewReader(values.Encode()))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", testfixture.Origin)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != 400 || !reflect.DeepEqual(current(false), beforeRestart) {
			t.Fatal("ambiguous/incomplete form changed a listing")
		}
	}
	if response := stateForm(h, base+"/trash", cookie, csrf, "archive", 11, testfixture.Origin); response.Code != 409 || !reflect.DeepEqual(current(false), beforeRestart) {
		t.Fatal("reused archive key accepted a different browser intent")
	}
	f.Reopen(t)
	policy, _ := thumbnail.NewPolicy("")
	s = feed.New(f.Store, policy)
	var err error
	h, err = web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.Manager, s, thumbnail.NewProxy(policy))
	if err != nil {
		t.Fatal(err)
	}
	p = f.Principal(t, "alice", allScopes)
	post("trash", "trash-archive", 4, "feed")
	post("restore", "final-restore", 10, "feed")
	if !reflect.DeepEqual(current(false), beforeRestart) {
		t.Fatal("browser retry changed state after database reopen")
	}
}

func TestJobsSignInReturnPathAndRequiredReadScope(t *testing.T) {
	_, _, h := app(t)
	for _, scopes := range []string{allScopes, identity.ArticlesWrite} {
		start := request(h, "GET", "/auth/login?return_to=%2Fjobs%2Fowned%3Ffrom%3Darchive", nil, "")
		authorize, err := url.Parse(start.Header().Get("Location"))
		if err != nil || start.Code != 302 {
			t.Fatal("could not start local synthetic login")
		}
		q := authorize.Query()
		q.Set("test_subject", "alice")
		q.Set("test_scopes", scopes)
		authorize.RawQuery = q.Encode()
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Get(authorize.String())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		callback, err := url.Parse(response.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		finish := request(h, "GET", callback.RequestURI(), start.Result().Cookies()[0], "")
		if scopes == allScopes {
			if finish.Code != 303 || finish.Header().Get("Location") != "/jobs/owned?from=archive" {
				t.Fatal("Jobs sign-in lost the selected return path")
			}
			continue
		}
		if finish.Code != 401 {
			t.Fatalf("write-only browser grant was not rejected: HTTP %d", finish.Code)
		}
		for _, cookie := range finish.Result().Cookies() {
			if cookie.Name == "__Host-newsfeed-session" && cookie.Value != "" {
				t.Fatal("missing-read login created a session")
			}
		}
	}
}

const browserMediaType = "application/vnd.agentnewsfeed.browser+json"

func browserJSONRequest(h http.Handler, path string, cookie *http.Cookie, form url.Values, origin string) *httptest.ResponseRecorder {
	method := http.MethodGet
	if form != nil {
		method = http.MethodPost
	}
	r := httptest.NewRequest(method, testfixture.Origin+path, strings.NewReader(form.Encode()))
	r.Header.Set("Accept", browserMediaType)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, r)
	return response
}

func readBrowserJSON[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != browserMediaType ||
		response.Header().Get("Vary") != "Accept" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid browser representation: status %d, headers %v", response.Code, response.Header())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBrowserJSONPageAndStateForms(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	o := saveBrowserOpportunity(t, s, p, "json-job")
	a, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "json-article", Article: feed.Content{
		URL: "https://example.com/story", Title: "Saved article", Summary: "Introduction",
		ContentMarkdown: "# Source\n\n<script>untrusted()</script>\n\n![Figure](https://images.example.com/figure.png)",
	}})
	if err != nil {
		t.Fatal(err)
	}
	listingPath := "/jobs/" + o.ID + "?from=feed"
	native := readJobsShell(t, request(h, http.MethodGet, listingPath, cookie, ""))
	negotiated := readBrowserJSON[jobsShell](t, browserJSONRequest(h, listingPath, cookie, nil, ""))
	if negotiated.MutationKey == "" || negotiated.CSRF != csrf {
		t.Fatal("JSON page omitted the state form credentials")
	}
	native.MutationKey, negotiated.MutationKey = "", ""
	if !reflect.DeepEqual(native, negotiated) {
		t.Fatal("HTML and JSON pages selected different data")
	}
	gate := readBrowserJSON[jobsShell](t, browserJSONRequest(h, listingPath, nil, nil, ""))
	if gate.Authenticated || gate.Opportunity != nil || gate.OpportunitiesPage != nil || gate.CSRF != "" ||
		gate.SignInURL != "/auth/login?return_to="+url.QueryEscape(listingPath) {
		t.Fatal("JSON sign-in gate lost the return path or exposed private data")
	}
	if response := browserJSONRequest(h, listingPath, f.Cookie(t, "bob"), nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("foreign detail HTTP %d", response.Code)
	}

	type destination struct {
		Location string `json:"location"`
	}
	articlePath := "/articles/" + a.Article.ID
	articleForm := url.Values{"csrf_token": {csrf}}
	archive := browserJSONRequest(h, "/api/v1"+articlePath+"/archive", cookie, articleForm, testfixture.Origin)
	if got := readBrowserJSON[destination](t, archive).Location; got != articlePath+"?from=archive" {
		t.Fatalf("article archive destination %q", got)
	}
	type newsPage struct {
		Article       sqlite.Article    `json:"article"`
		FromTrash     bool              `json:"from_trash"`
		ContentImages map[string]string `json:"content_images"`
	}
	trashedArticle := url.Values{"csrf_token": {csrf}, "idempotency_key": {"json-trash"}, "expected_revision": {"2"}}
	trash := browserJSONRequest(h, "/api/v1"+articlePath+"/trash", cookie, trashedArticle, testfixture.Origin)
	trashPath := readBrowserJSON[destination](t, trash).Location
	if trashPath != articlePath+"?from=trash" {
		t.Fatalf("article Trash destination %q", trashPath)
	}
	pageResponse := browserJSONRequest(h, trashPath, cookie, nil, "")
	page := readBrowserJSON[newsPage](t, pageResponse)
	image := page.ContentImages["https://images.example.com/figure.png"]
	if !page.FromTrash || page.Article.ArchivedAt == 0 || page.Article.TrashedAt == 0 || page.Article.ContentMarkdown != a.Article.ContentMarkdown ||
		!strings.HasPrefix(image, "/thumbnails/"+a.Article.ID+"/content/") || !strings.HasSuffix(image, "?trashed=true") ||
		strings.Contains(pageResponse.Body.String(), "<script>") {
		t.Fatal("JSON Trash reader lost state, protected images, or escaping")
	}

	jobsArchive := "/api/v1/opportunities/" + o.ID + "/archive"
	listingForm := url.Values{"csrf_token": {csrf}, "idempotency_key": {"json-archive"}, "expected_revision": {"1"}}
	for _, origin := range []string{"", "https://attacker.example"} {
		if response := browserJSONRequest(h, jobsArchive, cookie, listingForm, origin); response.Code != http.StatusForbidden {
			t.Fatalf("JSON mutation accepted unsafe origin %q", origin)
		}
	}
	for range 2 {
		archived := browserJSONRequest(h, jobsArchive, cookie, listingForm, testfixture.Origin)
		if got := readBrowserJSON[destination](t, archived).Location; got != "/jobs/"+o.ID+"?from=archive#opportunity-detail" {
			t.Fatalf("listing archive destination %q", got)
		}
	}
	listingForm.Set("idempotency_key", "stale")
	if response := browserJSONRequest(h, "/api/v1/opportunities/"+o.ID+"/unarchive", cookie, listingForm, testfixture.Origin); response.Code != http.StatusConflict {
		t.Fatalf("stale JSON mutation HTTP %d", response.Code)
	}
	current, err := s.GetOpportunity(t.Context(), p, feed.GetOpportunityInput{OpportunityID: o.ID})
	if err != nil || current.Revision != 2 || current.ArchivedAt == 0 {
		t.Fatalf("JSON archive replay or stale write changed state: revision %d, error %v", current.Revision, err)
	}
}
