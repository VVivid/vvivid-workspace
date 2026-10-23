// Package acceptance executes the actual production commands. OIDC and transport
// faults exist only in this test executable, never in cmd/shopping.
package acceptance_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
	"agentshoppinglist/internal/testfixture"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const scopes = identity.Read + " " + identity.ProductsWrite + " " + identity.ListsWrite + " " + identity.PurchasesWrite + " " + identity.PreferencesWrite

func meta(key string) shopping.WriteMeta {
	return shopping.WriteMeta{RequestID: "process-" + key, IdempotencyKey: "process-" + key}
}

func TestProductionProcessJourneyRestartAndInterruptedRetries(t *testing.T) {
	if os.Getenv("SHOPPING_PROCESS_TEST") != "1" {
		t.Skip("opt in with make process-test; starts only test-owned loopback processes")
	}
	app, admin := binaries(t)
	f := testfixture.New(t)
	f.Enroll(t, "alice")
	f.Enroll(t, "bob")
	environment := f.Environment()
	environment["SHOPPING_DB_PATH"] = f.Path
	environment["SHOPPING_HTTP_ADDR"] = reserveAddress(t)
	environment["SHOPPING_SHUTDOWN_TIMEOUT"] = "5s"
	base := "http://" + environment["SHOPPING_HTTP_ADDR"]
	process := startProcess(t, app, environment, base)
	t.Cleanup(func() { process.stop(t, false) })
	if version := runAdmin(t, app, "version"); !bytes.Contains(version, []byte(`"go":"go1.26.9"`)) {
		t.Fatalf("wrong release toolchain: %s", version)
	}
	f.SetUnavailable(true)
	for _, path := range []string{"/health/live", "/health/ready"} {
		response, _ := browser(t, base, nil, "", "GET", path, "", nil)
		if response.StatusCode != 200 {
			t.Fatalf("transient identity outage changed operational health: %s %d", path, response.StatusCode)
		}
	}
	f.SetUnavailable(false)
	agent := connect(t, base, f.Bearer("alice", "shopping-codex", testfixture.Origin+"/mcp", scopes), nil)
	bob := connect(t, base, f.Bearer("bob", "shopping-chatgpt", testfixture.Origin+"/mcp", scopes), nil)
	product := decoded[shopping.WriteResult[sqlite.Product]](t, call(t, agent, "save_product", shopping.SaveProductInput{WriteMeta: meta("polar"), Product: testfixture.Polar()})).Record
	bobProduct := decoded[shopping.WriteResult[sqlite.Product]](t, call(t, bob, "save_product", shopping.SaveProductInput{WriteMeta: meta("polar"), Product: testfixture.Polar()})).Record
	if product.ID == bobProduct.ID {
		t.Fatal("two owners merged the same product name")
	}
	list := decoded[shopping.WriteResult[sqlite.ListView]](t, call(t, agent, "save_list", shopping.SaveListInput{WriteMeta: meta("list"), Title: "Walmart", Retailer: new("Walmart"), MakeActive: true})).Record
	list = decoded[shopping.WriteResult[sqlite.ListView]](t, call(t, agent, "update_list_items", shopping.UpdateItemsInput{WriteMeta: meta("add"), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "add", ProductID: product.ID, Quantity: new("1"), Merge: "increment"}}})).Record
	cookie, csrf := login(t, base, "alice")
	bobCookie, _ := login(t, base, "bob")
	response, body := browser(t, base, cookie, csrf, "GET", "/api/v1/products/"+product.ID, "", nil)
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("walmartimages.com")) || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("actual browser response omitted sourced photo or private cache policy")
	}
	response, _ = browser(t, base, cookie, csrf, "PATCH", "/api/v1/lists/"+list.List.ID+"/items/"+list.Items[0].ID, "check", map[string]any{"picked_up": true, "expected_list_revision": list.List.Revision, "expected_revision": list.Items[0].Revision})
	if response.StatusCode != 200 {
		t.Fatal("browser check failed")
	}
	list = decoded[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{}))
	if !list.Items[0].PickedUp {
		t.Fatal("browser picked-up state was not durable")
	}
	if insight := decoded[shopping.Insights](t, call(t, agent, "get_insights", shopping.InsightsInput{})); len(insight.Spend) != 0 {
		t.Fatal("checkoff manufactured spending")
	}
	// Two actual MCP clients race the same revisions with different payloads.
	// Exactly one wins; a browser then deliberately restores the desired count.
	concurrent := connect(t, base, f.Bearer("alice", "shopping-chatgpt", testfixture.Origin+"/mcp", scopes), nil)
	conflicts := make(chan *sdk.CallToolResult, 2)
	conflictErrors := make(chan error, 2)
	var edits sync.WaitGroup
	for i, client := range []*sdk.ClientSession{agent, concurrent} {
		input := shopping.UpdateItemsInput{WriteMeta: meta([]string{"concurrent-a", "concurrent-b"}[i]), ListID: list.List.ID, ExpectedRevision: list.List.Revision, Operations: []sqlite.ItemOperation{{Kind: "set_quantity", ItemID: list.Items[0].ID, ExpectedRevision: list.Items[0].Revision, Quantity: new([]string{"2", "3"}[i])}}}
		edits.Go(func() {
			result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "update_list_items", Arguments: input})
			conflicts <- result
			conflictErrors <- err
		})
	}
	edits.Wait()
	successes := 0
	for range 2 {
		if err := <-conflictErrors; err != nil {
			t.Fatal(err)
		}
		if result := <-conflicts; !result.IsError {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("concurrent clients did not preserve optimistic revision conflicts")
	}
	list = decoded[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{}))
	response, _ = browser(t, base, cookie, csrf, "PATCH", "/api/v1/lists/"+list.List.ID+"/items/"+list.Items[0].ID, "restore-count", map[string]any{"quantity": "1", "expected_list_revision": list.List.Revision, "expected_revision": list.Items[0].Revision})
	if response.StatusCode != 200 {
		t.Fatal("browser could not resolve the concurrent count change")
	}
	list = decoded[sqlite.ListView](t, call(t, agent, "get_list", shopping.ListInput{}))
	prefs := decoded[shopping.Context](t, call(t, agent, "get_context", map[string]any{})).Preferences
	prefs.WeekStart = "sunday"
	call(t, agent, "update_preferences", shopping.PreferencesInput{WriteMeta: meta("preferences"), Preferences: prefs, ExpectedRevision: prefs.Revision})
	process.stop(t, true)
	process = startProcess(t, app, environment, base)
	response, body = browser(t, base, cookie, csrf, "GET", "/api/v1/lists/active", "", nil)
	if response.StatusCode != 200 || !bytes.Contains(body, []byte(`"picked_up":true`)) {
		t.Fatal("graceful process restart lost encrypted browser session or checked state")
	}
	agent = connect(t, base, f.Bearer("alice", "shopping-codex", testfixture.Origin+"/mcp", scopes), nil)
	if context := decoded[shopping.Context](t, call(t, agent, "get_context", map[string]any{})); context.Preferences.WeekStart != "sunday" {
		t.Fatal("restart lost preferences")
	}
	draft := sourcedDraft(product.ID, "source-once")
	draft.Lines[0].ListLink = &sqlite.PurchaseListLink{ListID: list.List.ID, ItemID: list.Items[0].ID, ExpectedListRevision: list.List.Revision, ExpectedItemRevision: list.Items[0].Revision}
	saved := decoded[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "save_purchase", shopping.SavePurchaseInput{WriteMeta: meta("draft"), Purchase: draft})).Record
	confirmation := shopping.ConfirmPurchaseInput{WriteMeta: meta("confirm"), PurchaseID: saved.ID, ExpectedRevision: saved.Revision}
	drop := &responseLoss{committed: make(chan []byte, 1), release: make(chan struct{})}
	faulty := connect(t, base, f.Bearer("alice", "shopping-codex", testfixture.Origin+"/mcp", scopes), drop)
	interrupted := make(chan error, 1)
	go func() {
		_, err := faulty.CallTool(t.Context(), &sdk.CallToolParams{Name: "confirm_purchase", Arguments: confirmation})
		interrupted <- err
	}()
	select {
	case committed := <-drop.committed:
		if !bytes.Contains(committed, []byte(`confirmed`)) {
			t.Fatal("fault did not occur after successful confirmation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("confirmation did not reach the response-loss boundary")
	}
	process.stop(t, false) // Commit exists, but the client has not received an outcome.
	close(drop.release)
	if err := <-interrupted; err == nil {
		t.Fatal("client unexpectedly received the lost response")
	}
	process = startProcess(t, app, environment, base)
	agent = connect(t, base, f.Bearer("alice", "shopping-codex", testfixture.Origin+"/mcp", scopes), nil)
	agent2 := connect(t, base, f.Bearer("alice", "shopping-chatgpt", testfixture.Origin+"/mcp", scopes), nil)
	var retries sync.WaitGroup
	retryResults := make(chan *sdk.CallToolResult, 2)
	retryErrors := make(chan error, 2)
	for _, client := range []*sdk.ClientSession{agent, agent2} {
		retries.Go(func() {
			result, err := client.CallTool(t.Context(), &sdk.CallToolParams{Name: "confirm_purchase", Arguments: confirmation})
			retryResults <- result
			retryErrors <- err
		})
	}
	retries.Wait()
	for range 2 {
		if err := <-retryErrors; err != nil {
			t.Fatal(err)
		}
		replay := decoded[shopping.WriteResult[shopping.PurchaseOutcome]](t, <-retryResults)
		if !replay.Replayed || replay.Record.ID != saved.ID || len(replay.Record.Reconciliations) != 1 {
			t.Fatal("crash/retry did not replay the persisted confirmation")
		}
	}
	assertSpend(t, agent, 599, 1)
	for _, path := range []string{"/history", "/history?purchase=" + saved.ID, "/insights"} {
		response, body := browser(t, base, cookie, csrf, "GET", path, "", nil)
		if response.StatusCode != 200 || !bytes.Contains(body, []byte("5.99")) {
			t.Fatalf("actual browser History/Insights: %s %d", path, response.StatusCode)
		}
	}
	bob = connect(t, base, f.Bearer("bob", "shopping-chatgpt", testfixture.Origin+"/mcp", scopes), nil)
	for name, input := range map[string]any{"get_product": map[string]any{"product_id": product.ID}, "get_purchase": map[string]any{"purchase_id": saved.ID}, "get_list": shopping.ListInput{ListID: list.List.ID}} {
		if result := callRaw(t, bob, name, input); !result.IsError {
			t.Fatalf("foreign MCP read succeeded: %s", name)
		}
	}
	for _, path := range []string{"/api/v1/products/" + product.ID, "/api/v1/purchases/" + saved.ID} {
		response, _ := browser(t, base, bobCookie, "", "GET", path, "", nil)
		if response.StatusCode != 404 {
			t.Fatalf("foreign browser read: %s %d", path, response.StatusCode)
		}
	}
	if !callRaw(t, agent, "save_purchase", shopping.SavePurchaseInput{WriteMeta: meta("duplicate-source"), Purchase: draft}).IsError {
		t.Fatal("new key duplicated an already claimed receipt")
	}
	// Force the second confirmation to wait behind a real SQLite writer. The
	// client's request-written trace is the barrier; no timing sleep guesses
	// whether an uncommitted application transaction ran before process death.
	second := decoded[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "save_purchase", shopping.SavePurchaseInput{WriteMeta: meta("second-draft"), Purchase: sourcedDraft(product.ID, "source-interrupted-before-commit")})).Record
	u := url.URL{Scheme: "file", Path: f.Path, RawQuery: "_txlock=immediate&_pragma=busy_timeout(5000)"}
	locker, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	held, err := locker.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	written := make(chan struct{})
	var traceOnce sync.Once
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { traceOnce.Do(func() { close(written) }) }})
	secondConfirm := shopping.ConfirmPurchaseInput{WriteMeta: meta("second-confirm"), PurchaseID: second.ID, ExpectedRevision: second.Revision}
	go func() {
		_, err := agent.CallTool(ctx, &sdk.CallToolParams{Name: "confirm_purchase", Arguments: secondConfirm})
		interrupted <- err
	}()
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked request was not written")
	}
	process.stop(t, false)
	if err := <-interrupted; err == nil {
		t.Fatal("blocked interrupted request unexpectedly succeeded")
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := locker.Close(); err != nil {
		t.Fatal(err)
	}
	process = startProcess(t, app, environment, base)
	agent = connect(t, base, f.Bearer("alice", "shopping-codex", testfixture.Origin+"/mcp", scopes), nil)
	first := decoded[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "confirm_purchase", secondConfirm))
	if first.Replayed {
		t.Fatal("uncommitted blocked confirmation reserved its idempotency result")
	}
	replay := decoded[shopping.WriteResult[shopping.PurchaseOutcome]](t, call(t, agent, "confirm_purchase", secondConfirm))
	if !replay.Replayed || !reflectJSONEqual(first.Record, replay.Record) {
		t.Fatal("retried interrupted purchase changed the original result")
	}
	assertSpend(t, agent, 1198, 2)
	response, _ = browser(t, base, cookie, csrf, "GET", "/api/v1/me", "", nil)
	if response.StatusCode != 200 {
		t.Fatal("abrupt restart lost durable session")
	}
	archive := filepath.Join(t.TempDir(), "running-process.tar")
	runAdmin(t, admin, "backup", "--db", f.Path, "--archive", archive)
	runAdmin(t, admin, "verify", "--archive", archive)
	restored := filepath.Join(t.TempDir(), "isolated.sqlite")
	runAdmin(t, admin, "restore", "--archive", archive, "--target", restored)
	var report sqlite.RecoveryReport
	if err := json.Unmarshal(runAdmin(t, admin, "inspect", "--db", restored), &report); err != nil || report.Tables["accounts"] != 2 || report.Tables["web_sessions"] != 2 || report.Tables["purchases"] != 2 || report.Confirmed[0].MerchandiseMinor != "1198" {
		t.Fatalf("actual operator recovery failed: %#v %v", report, err)
	}
	if target := os.Getenv("SHOPPING_ACCEPTANCE_EXPORT"); target != "" {
		runAdmin(t, admin, "backup", "--db", f.Path, "--archive", target)
	}
	process.stop(t, true)
	t.Log("production binary: two owners, OIDC+PKCE browser sessions, SDK Polar/check/no spend/confirm/history, graceful restart, committed-response loss+crash/concurrent replay, SQLite-blocked interruption/retry, durable preferences/sessions, active-process backup/isolated restore passed")
}

func sourcedDraft(product, source string) sqlite.PurchaseDraft {
	now := time.Now().Add(-time.Minute).UnixMilli()
	return sqlite.PurchaseDraft{
		Retailer:         "Walmart",
		PurchasedAt:      now,
		Currency:         "USD",
		SourceKind:       "user_confirmation",
		SourceReference:  source,
		SourceEvidence:   "Synthetic test: user confirmed one pack purchased for 599 minor units.",
		SourceObservedAt: now,
		Lines: []sqlite.PurchaseLineDraft{{
			ProductID:               product,
			ExpectedProductRevision: 1,
			Quantity:                "1",
			QuantityUnit:            "pack",
			UnitPriceMinor:          new(int64(599)),
			DiscountMinor:           new(int64(0)),
			LineTotalMinor:          new(int64(599)),
			TaxMinor:                new(int64(0)),
			TaxTreatment:            "excluded",
		}},
	}
}

func assertSpend(t *testing.T, session *sdk.ClientSession, amount int64, count int) {
	t.Helper()
	insight := decoded[shopping.Insights](t, call(t, session, "get_insights", shopping.InsightsInput{}))
	purchases := decoded[shopping.PurchasePage](t, call(t, session, "search_purchases", shopping.SearchPurchasesInput{Status: "confirmed"}))
	if len(insight.Spend) != 1 || insight.Spend[0].MerchandiseMinor == nil || *insight.Spend[0].MerchandiseMinor != amount || len(purchases.Purchases) != count {
		t.Fatalf("purchase replay changed totals/count: %#v %d", insight.Spend, len(purchases.Purchases))
	}
}

func decoded[T any](t *testing.T, result *sdk.CallToolResult) T {
	t.Helper()
	var value T
	if result == nil || result.IsError {
		t.Fatalf("MCP tool error: %#v", result)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func callRaw(t *testing.T, session *sdk.ClientSession, name string, input any) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func call(t *testing.T, session *sdk.ClientSession, name string, input any) *sdk.CallToolResult {
	t.Helper()
	result := callRaw(t, session, name, input)
	if result.IsError {
		raw, _ := json.Marshal(result.Content)
		t.Fatalf("%s: %s", name, raw)
	}
	return result
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Host = "shopping.example"
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(clone)
}

func connect(t *testing.T, base, token string, transport http.RoundTripper) *sdk.ClientSession {
	t.Helper()
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "process-acceptance", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: transport}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type responseLoss struct {
	used      atomic.Bool
	committed chan []byte
	release   chan struct{}
}

func (fault *responseLoss) RoundTrip(request *http.Request) (*http.Response, error) {
	var message struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if request.Body != nil {
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(raw))
		_ = json.Unmarshal(raw, &message)
	}
	response, err := http.DefaultTransport.RoundTrip(request)
	if err != nil || message.Method != "tools/call" || message.Params.Name != "confirm_purchase" || fault.used.Swap(true) {
		return response, err
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	fault.committed <- raw
	select {
	case <-fault.release:
	case <-request.Context().Done():
	}
	return nil, errors.New("synthetic response lost after commit")
}

func browser(t *testing.T, base string, cookie *http.Cookie, csrf, method, path, key string, input any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(t.Context(), method, base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "shopping.example"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	request.Header.Set("Origin", testfixture.Origin)
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "process-browser-"+key)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func login(t *testing.T, base, subject string) (*http.Cookie, string) {
	t.Helper()
	start, _ := browser(t, base, nil, "", "GET", "/auth/login", "", nil)
	if start.StatusCode != 302 || len(start.Cookies()) == 0 {
		t.Fatal("production process did not initiate login")
	}
	authorize, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	query.Set("test_subject", subject)
	authorize.RawQuery = query.Encode()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(authorize.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	callback, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	finish, _ := browser(t, base, start.Cookies()[0], "", "GET", callback.RequestURI(), "", nil)
	if finish.StatusCode != 303 {
		t.Fatalf("production OIDC callback failed: %d", finish.StatusCode)
	}
	for _, cookie := range finish.Cookies() {
		if cookie.Name == "__Host-shopping-session" {
			_, page := browser(t, base, cookie, "", "GET", "/", "", nil)
			match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindSubmatch(page)
			if len(match) != 2 {
				t.Fatal("authenticated page omitted CSRF token")
			}
			return cookie, string(match[1])
		}
	}
	t.Fatal("production callback omitted secure session cookie")
	return nil, ""
}

type appProcess struct {
	cmd    *exec.Cmd
	done   chan error
	exited bool
	logs   *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (buffer *lockedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Write(value)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.String()
}

func startProcess(t *testing.T, binary string, environment map[string]string, base string) *appProcess {
	t.Helper()
	command := exec.Command(binary)
	// Do not inherit a developer's live shopping configuration or credentials.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "SHOPPING_") {
			command.Env = append(command.Env, entry)
		}
	}
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	process := &appProcess{cmd: command, done: make(chan error, 1), logs: &lockedBuffer{}}
	command.Stdout, command.Stderr = process.logs, process.logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { process.done <- command.Wait() }()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		response, err := client.Get(base + "/health/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return process
			}
		}
		select {
		case err := <-process.done:
			process.exited = true
			t.Fatalf("production process exited before readiness: %v %s", err, process.logs.String())
		case <-deadline.C:
			process.stop(t, false)
			t.Fatalf("production readiness timed out: %s", process.logs.String())
		case <-ticker.C:
		}
	}
}

func (process *appProcess) stop(t *testing.T, graceful bool) {
	t.Helper()
	if process == nil || process.exited {
		return
	}
	if graceful {
		_ = process.cmd.Process.Signal(syscall.SIGTERM)
	} else {
		_ = process.cmd.Process.Kill()
	}
	select {
	case err := <-process.done:
		process.exited = true
		if graceful && err != nil {
			t.Fatalf("graceful process exit: %v %s", err, process.logs.String())
		}
	case <-time.After(8 * time.Second):
		_ = process.cmd.Process.Kill()
		<-process.done
		process.exited = true
		t.Fatal("application shutdown timed out")
	}
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func binaries(t *testing.T) (string, string) {
	t.Helper()
	if app, admin := os.Getenv("SHOPPING_ACCEPTANCE_BINARY"), os.Getenv("SHOPPING_ACCEPTANCE_ADMIN_BINARY"); app != "" && admin != "" {
		return app, admin
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for _, name := range []string{"shopping", "shopping-admin"} {
		command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", filepath.Join(directory, name), "./cmd/"+name)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build production command: %v %s", err, output)
		}
	}
	return filepath.Join(directory, "shopping"), filepath.Join(directory, "shopping-admin")
}

func runAdmin(t *testing.T, binary string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("operator command %s: %v %s", args[0], err, output)
	}
	return output
}

func reflectJSONEqual(left, right any) bool {
	l, _ := json.Marshal(left)
	r, _ := json.Marshal(right)
	return bytes.Equal(l, r)
}
