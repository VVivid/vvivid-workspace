package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/identity"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/mcpserver"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/store/sqlite"
	"github.com/go-jose/go-jose/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type observedStore struct {
	*sqlite.Store
	writes     atomic.Int64
	failWrite  atomic.Bool
	blockWrite atomic.Bool
	entered    chan struct{}
	canceled   chan struct{}
}

func (s *observedStore) Remember(ctx context.Context, actor memory.Actor, input memory.RememberRequest) (memory.RememberResult, error) {
	s.writes.Add(1)
	if s.blockWrite.Load() {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		select {
		case s.canceled <- struct{}{}:
		default:
		}
		return memory.RememberResult{}, ctx.Err()
	}
	if s.failWrite.Load() {
		return memory.RememberResult{}, errors.New("SELECT secret FROM /private/journal.db: source text private-token")
	}
	return s.Store.Remember(ctx, actor, input)
}

type httpFixture struct {
	cfg      Config
	store    *observedStore
	server   *httptest.Server
	key      *rsa.PrivateKey
	accounts map[string]string
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.Database, cfg.Identity.JWKSFile = filepath.Join(t.TempDir(), "memory.db"), filepath.Join(t.TempDir(), "public.jwks")
	keys, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Identity.JWKSFile, keys, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	f := &httpFixture{cfg: cfg, store: &observedStore{Store: store, entered: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}, key: key, accounts: map[string]string{}}
	for _, subject := range []string{"synthetic-alice", "synthetic-bob"} {
		id, err := store.EnrollAccount(t.Context(), account.Identity{Issuer: cfg.Identity.IdentityIssuer, Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		f.accounts[subject] = id
	}
	handler, err := NewHandler(cfg, f.store)
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(handler)
	t.Cleanup(func() { f.server.Close(); store.Close() })
	return f
}

func (f *httpFixture) token(t *testing.T, subject, client, scopes string, change func(map[string]any)) string {
	t.Helper()
	now := time.Now().Unix()
	c := map[string]any{"iss": f.cfg.Identity.GatewayIssuer, "aud": []string{f.cfg.Identity.Audience}, "identity_issuer": f.cfg.Identity.IdentityIssuer, "sub": subject, "client_id": client, "scope": scopes, "iat": now, "nbf": now, "exp": now + 60, "jti": "synthetic-token"}
	if change != nil {
		change(c)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType(identity.TokenType).WithHeader(jose.HeaderKey("kid"), "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type authTransport struct {
	token string
	host  string
	mu    sync.Mutex
	wire  [][]byte
}

func (a *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+a.token)
	r.Host = a.host
	response, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		a.mu.Lock()
		a.wire = append(a.wire, bytes.Clone(body))
		a.mu.Unlock()
	}
	return response, nil
}

func (f *httpFixture) session(t *testing.T, subject, client, scopes string, version ...string) (*mcp.ClientSession, *authTransport) {
	t.Helper()
	origin, _ := url.Parse(f.cfg.BackendOrigin)
	transport := &authTransport{token: f.token(t, subject, client, scopes, nil), host: origin.Host}
	clientSDK := mcp.NewClient(&mcp.Implementation{Name: "synthetic-agent", Version: "1"}, nil)
	var options *mcp.ClientSessionOptions
	if len(version) > 0 {
		options = &mcp.ClientSessionOptions{ProtocolVersion: version[0]}
	}
	session, err := clientSDK.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: f.server.URL + "/mcp", HTTPClient: &http.Client{Transport: transport, Timeout: 10 * time.Second}, DisableStandaloneSSE: true}, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, transport
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, input any, code string) *mcp.CallToolResult {
	t.Helper()
	// The domain's RawMessage zero value marshals as null. This synthetic
	// client explicitly sends the documented default object instead.
	if request, ok := input.(memory.RememberRequest); ok {
		request.Entries = slices.Clone(request.Entries)
		for i := range request.Entries {
			if len(request.Entries[i].Payload) == 0 {
				request.Entries[i].Payload = json.RawMessage(`{}`)
			}
		}
		input = request
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != (code != "") {
		t.Fatalf("tool %s error=%v, expected code %q", name, result.IsError, code)
	}
	if code != "" {
		failure := decodeTool[struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}](t, result)
		if failure.Error.Code != code {
			t.Fatalf("tool error code %q, want %q", failure.Error.Code, code)
		}
	}
	return result
}

func decodeTool[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	var value T
	if len(result.Content) != 1 {
		t.Fatal("missing single text result")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || json.Unmarshal([]byte(text.Text), &value) != nil {
		t.Fatal("invalid structured text result")
	}
	return value
}

func (a *authTransport) lastStructured(t *testing.T) json.RawMessage {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.wire) - 1; i >= 0; i-- {
		var response struct {
			Result struct {
				Structured json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		if json.Unmarshal(a.wire[i], &response) == nil && len(response.Result.Structured) != 0 {
			return bytes.Clone(response.Result.Structured)
		}
	}
	t.Fatal("no structured result on actual HTTP wire")
	return nil
}

const originalBatch = `{"space":"GuestUp","idempotency_key":"original","entries":[
 {"local_id":"root","kind":"topic","title":"PAYM-123 service","context":"Original discussion: one small service, clear operations.","external_ref":"PAYM-123","occurred_at":"2025-04-01T09:00:00Z"},
 {"local_id":"go","kind":"decision","title":"Language","schema_version":0,"payload":{"question":"Which language?","outcome":"Go","reasons":["One deployable binary"],"alternatives":[{"option":"Python","disposition":"rejected","reasons":["Separate runtime packaging"]}]},"occurred_at":"2025-04-01T10:00:00Z","provenance":{"sources":[{"reference":"meeting://synthetic/PAYM-123","excerpt":"Choose Go for the small service."}]}},
 {"local_id":"sqlite","parent":{"local_id":"go"},"kind":"decision","title":"Storage","payload":{"question":"Which database?","outcome":"SQLite","reasons":["One writer and a small operational footprint"],"alternatives":[{"option":"PostgreSQL","disposition":"rejected","reasons":["Additional database service was unnecessary"]}],"large_integer":9007199254740993,"exact_decimal":1.0,"optional_fact":null},"occurred_at":"2025-04-02T11:00:00Z","tags":["storage"],"provenance":{"attribution":"user-confirmed","sources":[{"reference":"meeting://synthetic/PAYM-123","excerpt":"SQLite chosen; PostgreSQL rejected for the first single-writer deployment."}]}}
]}`

func TestSDKJournalOverHTTP(t *testing.T) {
	f := newHTTPFixture(t)
	alice, wire := f.session(t, "synthetic-alice", "agent-one", "memory:read memory:write")
	if !strings.Contains(alice.InitializeResult().Instructions, "untrusted") || !strings.Contains(alice.InitializeResult().Instructions, "topic") {
		t.Fatal("missing agent use instructions")
	}
	tools, err := alice.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Fatal("tool lacks human-readable guidance")
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if json.Unmarshal(schema, &object) != nil || object["type"] != "object" || object["additionalProperties"] != false {
			t.Fatal("not a real bounded object schema")
		}
		if tool.Name == "remember" && (!bytes.Contains(schema, []byte(`"payload"`)) || !bytes.Contains(schema, []byte(`"alternatives"`))) {
			t.Fatal("decision payload is opaque in discovery")
		}
	}
	slices.Sort(names)
	if !reflect.DeepEqual(names, []string{"list_spaces", "remember", "retrieve"}) {
		t.Fatalf("unexpected tools: %v", names)
	}
	spaces := decodeTool[struct {
		Spaces []memory.Space `json:"spaces"`
	}](t, callTool(t, alice, "list_spaces", nil, ""))
	if len(spaces.Spaces) != 0 {
		t.Fatal("read probe or enrollment created a memory space")
	}
	first := decodeTool[mcpserver.RememberReceipt](t, callTool(t, alice, "remember", json.RawMessage(originalBatch), ""))
	if len(first.Records) != 3 || first.Records[0].SpanID != first.TopicID || first.Records[2].ParentID != first.Records[1].SpanID {
		t.Fatal("topic tree receipt is incorrect")
	}
	if replay := decodeTool[mcpserver.RememberReceipt](t, callTool(t, alice, "remember", json.RawMessage(originalBatch), "")); !reflect.DeepEqual(first, replay) {
		t.Fatal("same-key replay changed the receipt")
	}
	changed := strings.Replace(originalBatch, "9007199254740993", "9007199254740992", 1)
	callTool(t, alice, "remember", json.RawMessage(changed), "idempotency_conflict")
	reuse := memory.RememberRequest{Space: "GuestUp/reuse", Key: "second-ticket", Entries: []memory.Entry{{Kind: memory.Topic, Title: "PAYM-1234 reuse", ExternalRef: "PAYM-1234", Links: []memory.LinkInput{{Type: memory.RelatedTo, Target: memory.Reference{SpanID: first.TopicID}}}}, {Kind: memory.Context, Title: "Reuse the SQLite decision", Payload: json.RawMessage(`{"question":{"id":7},"nested":[null,true,9007199254740993]}`), Links: []memory.LinkInput{{Type: memory.DependsOn, Target: memory.Reference{SpanID: first.Records[2].SpanID}}}}}}
	reused := decodeTool[mcpserver.RememberReceipt](t, callTool(t, alice, "remember", reuse, ""))
	query := memory.Query{Spaces: []string{"GuestUp"}, TopicIDs: []string{first.TopicID}, Keywords: "PostgreSQL", Tags: []string{"storage"}, Occurred: memory.TimeRange{Calendar: &memory.CalendarRange{StartDate: "2025-04-02", EndDate: "2025-04-03", Timezone: "UTC"}}, Limit: 1, ContextLimit: 1, LinkLimit: 1}
	contextIDs := map[string]bool{}
	relationshipFound := false
	var payload json.RawMessage
	pages := 0
	for {
		page := decodeTool[memory.Page](t, callTool(t, alice, "retrieve", query, ""))
		pages++
		if pages > 20 {
			t.Fatal("continuation did not finish")
		}
		for _, record := range page.Matches {
			if record.SpanID != first.Records[2].SpanID {
				t.Fatal("keyword/date filter selected wrong decision")
			}
			payload = record.Payload
		}
		for _, record := range page.Context {
			contextIDs[record.SpanID] = true
		}
		for _, link := range page.Relationships {
			if link.Type == memory.DependsOn && link.Source.TopicID == reused.TopicID && link.Source.Space == "GuestUp/reuse" {
				relationshipFound = true
			}
		}
		if page.NextCursor == "" {
			if !page.Complete {
				t.Fatal("cursor ended without completeness")
			}
			break
		}
		if page.Complete {
			t.Fatal("partial support presented as complete")
		}
		query.Cursor = page.NextCursor
	}
	if pages < 2 || !contextIDs[first.TopicID] || !contextIDs[first.Records[1].SpanID] || !relationshipFound {
		t.Fatal("missing older context or explicit cross-space reuse reference")
	}
	var exact map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&exact) != nil || exact["large_integer"] != json.Number("9007199254740993") || exact["exact_decimal"] != json.Number("1.0") {
		t.Fatal("MCP changed exact JSON numbers")
	}
	// Inspect StructuredContent on the actual HTTP wire independently of the
	// SDK client's any decoding, which itself may use float64.
	callTool(t, alice, "retrieve", memory.Query{Spaces: []string{"GuestUp"}, SpanIDs: []string{first.Records[2].SpanID}}, "")
	var onWire memory.Page
	if json.Unmarshal(wire.lastStructured(t), &onWire) != nil || len(onWire.Matches) != 1 || !bytes.Contains(onWire.Matches[0].Payload, []byte(`9007199254740993`)) {
		t.Fatal("structured wire output lost large integer precision")
	}
	linked := decodeTool[memory.Page](t, callTool(t, alice, "retrieve", memory.Query{Spaces: []string{"GuestUp/reuse"}, TopicIDs: []string{reused.TopicID}}, ""))
	if len(linked.Matches) != 2 {
		t.Fatal("explicit linked-tree expansion failed")
	}
	if !bytes.Contains(linked.Matches[1].Payload, []byte(`"question":{"id":7}`)) || !bytes.Contains(linked.Matches[1].Payload, []byte(`9007199254740993`)) {
		t.Fatal("generic nondecision payload was constrained by decision field names")
	}
	bob, _ := f.session(t, "synthetic-bob", "agent-one", "memory:read memory:write")
	callTool(t, bob, "retrieve", memory.Query{Spaces: []string{"GuestUp"}, TopicIDs: []string{first.TopicID}}, "not_found")
	callTool(t, bob, "remember", memory.RememberRequest{Space: "GuestUp", Key: "foreign-parent", TopicID: first.TopicID, Entries: []memory.Entry{{Kind: memory.Note, Title: "foreign append"}}}, "not_found")
	callTool(t, bob, "remember", memory.RememberRequest{Space: "GuestUp", Key: "foreign-link", Entries: []memory.Entry{{Kind: memory.Topic, Title: "foreign link", Links: []memory.LinkInput{{Type: memory.RelatedTo, Target: memory.Reference{SpanID: first.TopicID}}}}}}, "not_found")
	callTool(t, bob, "remember", memory.RememberRequest{Space: "GuestUp", Key: "bob", Entries: []memory.Entry{{Kind: memory.Topic, Title: "Bob's private discussion"}}}, "")
	bobPage := decodeTool[memory.Page](t, callTool(t, bob, "retrieve", memory.Query{AllSpaces: true}, ""))
	if len(bobPage.Matches) != 1 || bobPage.Matches[0].Title != "Bob's private discussion" {
		t.Fatal("owner isolation failed")
	}
	secondClient, _ := f.session(t, "synthetic-alice", "agent-two", "memory:read memory:write")
	callTool(t, secondClient, "remember", json.RawMessage(originalBatch), "idempotency_conflict")
	readOnly, _ := f.session(t, "synthetic-alice", "agent-one", "memory:read")
	before := f.store.writes.Load()
	callTool(t, readOnly, "remember", json.RawMessage(originalBatch), "forbidden")
	callTool(t, readOnly, "remember", memory.RememberRequest{Space: "forbidden", Key: "new", Entries: []memory.Entry{{Kind: memory.Topic, Title: "denied"}}}, "forbidden")
	if f.store.writes.Load() != before {
		t.Fatal("read-only request reached write/replay lookup")
	}
	callTool(t, readOnly, "list_spaces", nil, "")
	writeOnly, _ := f.session(t, "synthetic-alice", "agent-one", "memory:write")
	callTool(t, writeOnly, "retrieve", memory.Query{AllSpaces: true}, "forbidden")
	callTool(t, writeOnly, "list_spaces", nil, "forbidden")
	if found, err := f.store.DisableAccount(t.Context(), account.Identity{Issuer: f.cfg.Identity.IdentityIssuer, Subject: "synthetic-alice"}); err != nil || !found {
		t.Fatal("disable failed")
	}
	if _, err := alice.ListTools(t.Context(), nil); err == nil {
		t.Fatal("existing SDK session retained disabled enrollment")
	}
	if id, err := f.store.EnrollAccount(t.Context(), account.Identity{Issuer: f.cfg.Identity.IdentityIssuer, Subject: "synthetic-alice"}); err != nil || id != f.accounts["synthetic-alice"] {
		t.Fatal("re-enrollment changed identity")
	}
	// The SDK closes its client connection after an HTTP authorization failure.
	// A freshly connected client resolves the re-enabled mapping on its request.
	alice, _ = f.session(t, "synthetic-alice", "agent-one", "memory:read memory:write")
	callTool(t, alice, "remember", json.RawMessage(originalBatch), "")
}

func (f *httpFixture) post(t *testing.T, token string, body []byte, mutate func(*http.Request)) (int, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse(f.cfg.BackendOrigin)
	request.Host = origin.Host
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if mutate != nil {
		mutate(request)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

func TestHTTPRejectsUntrustedBoundaryInputs(t *testing.T) {
	f := newHTTPFixture(t)
	valid := f.token(t, "synthetic-alice", "agent-one", "memory:read memory:write", nil)
	const rpc = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_spaces","arguments":{}}}`
	for name, test := range map[string]struct {
		token  string
		body   string
		mutate func(*http.Request)
		status int
	}{
		"anonymous":      {body: rpc, status: 401},
		"external token": {token: "provider-token", body: rpc, status: 401},
		"forwarded identity": {body: rpc, status: 401, mutate: func(r *http.Request) {
			r.Header.Set("X-Forwarded-User", "synthetic-alice")
			r.Header.Set("X-Identity-Issuer", f.cfg.Identity.IdentityIssuer)
		}},
		"unknown enrollment":      {token: f.token(t, "unknown", "agent-one", "memory:read", nil), body: rpc, status: 403},
		"wrong issuer":            {token: f.token(t, "synthetic-alice", "agent-one", "memory:read", func(c map[string]any) { c["iss"] = "https://other.example" }), body: rpc, status: 401},
		"wrong audience":          {token: f.token(t, "synthetic-alice", "agent-one", "memory:read", func(c map[string]any) { c["aud"] = []string{"other"} }), body: rpc, status: 401},
		"wrong client":            {token: f.token(t, "synthetic-alice", "untrusted", "memory:read", nil), body: rpc, status: 401},
		"wrong scope":             {token: f.token(t, "synthetic-alice", "agent-one", "admin:all", nil), body: rpc, status: 401},
		"duplicate authorization": {token: valid, body: rpc, status: 401, mutate: func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+valid) }},
		"wrong host":              {token: valid, body: rpc, status: 421, mutate: func(r *http.Request) { r.Host = "attacker.example" }},
		"wrong origin":            {token: valid, body: rpc, status: 403, mutate: func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") }},
		"duplicate origin":        {token: valid, body: rpc, status: 403, mutate: func(r *http.Request) { r.Header["Origin"] = []string{f.cfg.BackendOrigin, f.cfg.BackendOrigin} }},
		"null origin":             {token: valid, body: rpc, status: 403, mutate: func(r *http.Request) { r.Header.Set("Origin", "null") }},
		"trailing JSON":           {token: valid, body: rpc + `{}`, status: 400},
		"duplicate RPC":           {token: valid, body: strings.Replace(rpc, `"id":1`, `"id":1,"id":2`, 1), status: 400},
		"unknown RPC":             {token: valid, body: strings.Replace(rpc, `"id":1`, `"id":1,"owner":"other"`, 1), status: 400},
		"null arguments":          {token: valid, body: strings.Replace(rpc, `"arguments":{}`, `"arguments":null`, 1), status: 400},
		"duplicate arguments":     {token: valid, body: strings.Replace(rpc, `"arguments":{}`, `"arguments":{"x":1,"x":2}`, 1), status: 400},
		"oversized chunked":       {token: valid, body: strings.Repeat(" ", MaxRequestBytes) + rpc, status: 413, mutate: func(r *http.Request) { r.ContentLength = -1 }},
		"wrong media":             {token: valid, body: rpc, status: 415, mutate: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
	} {
		t.Run(name, func(t *testing.T) {
			status, data := f.post(t, test.token, []byte(test.body), test.mutate)
			if status != test.status {
				t.Fatalf("status %d, want %d: %s", status, test.status, data)
			}
			if bytes.Contains(data, []byte("synthetic-alice")) || bytes.Contains(data, []byte(valid)) || bytes.Contains(data, []byte(f.cfg.Database)) {
				t.Fatal("unsafe HTTP error")
			}
		})
	}
	if f.store.writes.Load() != 0 {
		t.Fatal("boundary rejection reached persistence")
	}
	session, _ := f.session(t, "synthetic-alice", "agent-one", "memory:read memory:write")
	for _, input := range []string{
		`{"account_id":"other","space":"forged","idempotency_key":"k","entries":[{"kind":"topic","title":"fake"}]}`,
		`{"space":"forged","idempotency_key":"k","entries":[{"kind":"topic","title":"fake","writer":{"account_id":"other"}}]}`,
		`{"space":"forged","idempotency_key":"k","entries":[{"kind":"topic","title":"fake","payload":null}]}`,
		`{"space":"forged","idempotency_key":"k","entries":[{"kind":"topic","title":"fake","provenance":{"sources":[{"reference":null}]}}]}`,
	} {
		callTool(t, session, "remember", json.RawMessage(input), "invalid_arguments")
	}
	if f.store.writes.Load() != 0 {
		t.Fatal("invalid envelope reached persistence")
	}
	f.store.failWrite.Store(true)
	result := callTool(t, session, "remember", json.RawMessage(originalBatch), "storage_unavailable")
	text := result.Content[0].(*mcp.TextContent).Text
	for _, secret := range []string{"SELECT", "private-token", "source text", "/private/"} {
		if strings.Contains(text, secret) {
			t.Fatal("storage error leaked implementation or data")
		}
	}
	f.store.failWrite.Store(false)
	status, _ := f.post(t, valid, []byte(rpc), func(r *http.Request) {
		r.Header.Set("Origin", f.cfg.BackendOrigin)
		r.Header.Set("X-Forwarded-User", "synthetic-bob")
	})
	if status != 200 {
		t.Fatal("canonical origin or signed identity was overridden by forwarded headers")
	}
	for _, path := range []string{"/health/live", "/health/ready"} {
		response, err := http.Get(f.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("healthy probe failed")
		}
	}
	f.store.Close()
	for path, want := range map[string]int{"/health/live": 200, "/health/ready": 503} {
		response, err := http.Get(f.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatal("readiness did not reflect storage availability")
		}
	}
}

func TestLargeEscapedWriteHasReplayableReceipt(t *testing.T) {
	f := newHTTPFixture(t)
	token := f.token(t, "synthetic-alice", "agent-one", "memory:read memory:write", nil)
	input := memory.RememberRequest{Space: "Escaped", Key: "escaped"}
	sources := make([]memory.Source, 16)
	for i := range sources {
		sources[i] = memory.Source{Reference: strings.Repeat("<", 2048), Excerpt: strings.Repeat("<&>", 1365)}
	}
	for i := range 20 {
		kind := memory.Note
		if i == 0 {
			kind = memory.Topic
		}
		input.Entries = append(input.Entries, memory.Entry{Kind: kind, Title: "Escaped source", Context: strings.Repeat("<", 16384), Payload: json.RawMessage(`{}`), Provenance: memory.Provenance{Sources: sources}})
	}
	// Disable HTML escaping only in this synthetic client request. The resulting
	// stored/retrieved JSON expands substantially, but the receipt cannot.
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "remember", "arguments": input}}); err != nil {
		t.Fatal(err)
	}
	if body.Len() > MaxRequestBytes {
		t.Fatal("fixture exceeds HTTP body limit")
	}
	status, first := f.post(t, token, body.Bytes(), nil)
	if status != 200 || len(first) > 100<<10 || bytes.Contains(first, []byte(`"isError":true`)) {
		t.Fatal("committed escaped batch lacks a bounded success receipt")
	}
	status, replay := f.post(t, token, body.Bytes(), nil)
	if status != 200 || !bytes.Equal(first, replay) {
		t.Fatal("same-key retry cannot acknowledge the committed escaped batch")
	}
	session, _ := f.session(t, "synthetic-alice", "agent-one", "memory:read")
	callTool(t, session, "retrieve", memory.Query{Spaces: []string{"Escaped"}}, "result_too_large")
	page := decodeTool[memory.Page](t, callTool(t, session, "retrieve", memory.Query{Spaces: []string{"Escaped"}, Limit: 1, ContextLimit: 1, LinkLimit: 1}, ""))
	if len(page.Matches) != 1 || page.NextCursor == "" {
		t.Fatal("smaller retrieval cannot continue a large topic")
	}
}

func TestSDKCancellationReachesStorage(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := newHTTPFixture(t)
			session, _ := f.session(t, "synthetic-alice", "agent-one", "memory:read memory:write", version)
			if session.InitializeResult().ProtocolVersion != version {
				t.Fatal("SDK negotiated an unexpected protocol version")
			}
			f.store.blockWrite.Store(true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: json.RawMessage(originalBatch)})
				done <- err
			}()
			select {
			case <-f.store.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("SDK request did not reach storage")
			}
			cancel()
			select {
			case <-f.store.canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP cancellation was detached from the storage context")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled SDK call succeeded")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled SDK call did not finish")
			}
			spaces, err := f.store.ListSpaces(t.Context(), f.accounts["synthetic-alice"])
			if err != nil || len(spaces) != 0 {
				t.Fatal("canceled call wrote a memory")
			}
		})
	}
}
