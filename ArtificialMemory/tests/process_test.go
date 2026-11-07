//go:build integration

package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/identity"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/mcpserver"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
	"github.com/go-jose/go-jose/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type process struct {
	cmd     *exec.Cmd
	done    chan error
	log     bytes.Buffer
	stopped bool
}

func environment() []string {
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "MEMORY_") {
			env = append(env, value)
		}
	}
	return env
}

func start(t *testing.T, binary string, env []string, endpoint string) *process {
	t.Helper()
	p := &process{cmd: exec.Command(binary, "serve"), done: make(chan error, 1)}
	p.cmd.Env = append(environment(), env...)
	p.cmd.Stdout = io.Discard
	p.cmd.Stderr = &p.log
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait() }()
	t.Cleanup(func() {
		if !p.stopped {
			p.cmd.Process.Kill()
			<-p.done
			p.stopped = true
		}
	})
	deadline := time.After(10 * time.Second)
	tick := time.Tick(25 * time.Millisecond)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		select {
		case err := <-p.done:
			p.stopped = true
			t.Fatalf("production command exited before ready: %v: %s", err, p.log.String())
		case <-deadline:
			t.Fatal("production command readiness timed out")
		case <-tick:
			response, err := client.Get(endpoint + "/health/ready")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					return p
				}
			}
		}
	}
}

func (p *process) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.done:
		p.stopped = true
		if err != nil {
			t.Fatalf("graceful signal exit failed: %v: %s", err, p.log.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful shutdown exceeded deadline")
	}
	if !strings.Contains(p.log.String(), "ArtificialMemory stopped.") {
		t.Fatal("command did not finish graceful shutdown")
	}
}

type gatewayTransport struct{ token string }

func (g gatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+g.token)
	r.Host = "memory.internal:8080"
	return http.DefaultTransport.RoundTrip(r)
}

func TestProductionCommandEnrollmentRestartAndLostAcknowledgment(t *testing.T) {
	binary := os.Getenv("MEMORY_TEST_BINARY")
	if binary == "" {
		t.Fatal("integration requires the production binary via MEMORY_TEST_BINARY")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	private := t.TempDir()
	database := filepath.Join(private, "memory.db")
	subjectFile := filepath.Join(private, "subject")
	const issuer = "https://identity.example/realms/test"
	const subject = "synthetic-process-subject"
	if err := os.WriteFile(subjectFile, []byte(subject+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	admin := func(operation string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, "admin", operation, "--database", database, "--issuer", issuer, "--subject-file", subjectFile)
		cmd.Env = environment()
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("admin %s failed: %v: %s", operation, err, output)
		}
		if bytes.Contains(output, []byte(subject)) || bytes.Contains(output, []byte(subjectFile)) {
			t.Fatal("admin exposed original identity or private path")
		}
		return output
	}
	enrollment := admin("enroll")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	jwks := filepath.Join(private, "gateway-public.jwks")
	if err := os.WriteFile(jwks, public, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	endpoint := "http://" + address
	env := []string{"MEMORY_LISTEN=" + address, "MEMORY_DATABASE=" + database, "MEMORY_BACKEND_ORIGIN=http://memory.internal:8080", "MEMORY_GATEWAY_ISSUER=https://gateway.example", "MEMORY_AUDIENCE=https://gateway.example/memory/mcp", "MEMORY_IDENTITY_ISSUER=" + issuer, "MEMORY_GATEWAY_JWKS_FILE=" + jwks, "MEMORY_ALLOWED_CLIENT_IDS=agent-one"}
	token := func() string {
		t.Helper()
		now := time.Now().Unix()
		payload, err := json.Marshal(map[string]any{"iss": "https://gateway.example", "aud": []string{"https://gateway.example/memory/mcp"}, "identity_issuer": issuer, "sub": subject, "client_id": "agent-one", "scope": "memory:read memory:write", "iat": now, "nbf": now, "exp": now + 60, "jti": "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType(identity.TokenType).WithHeader(jose.HeaderKey("kid"), "fixture"))
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
	connect := func() *mcp.ClientSession {
		t.Helper()
		client := mcp.NewClient(&mcp.Implementation{Name: "synthetic-process-client", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint + "/mcp", HTTPClient: &http.Client{Transport: gatewayTransport{token: token()}, Timeout: 10 * time.Second}, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	call := func(session *mcp.ClientSession, name string, input any, out any) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
		if err != nil || result.IsError {
			t.Fatalf("production tool %s failed: %v", name, err)
		}
		if len(result.Content) != 1 {
			t.Fatal("missing tool text result")
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || json.Unmarshal([]byte(text.Text), out) != nil {
			t.Fatal("invalid tool result")
		}
	}
	p := start(t, binary, env, endpoint)
	session := connect()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatal("production SDK discovery failed")
	}
	const original = `{"space":"Process","idempotency_key":"original","entries":[{"kind":"topic","title":"Persistent topic"},{"kind":"note","title":"Precise payload","payload":{"number":9007199254740993}}]}`
	var first mcpserver.RememberReceipt
	call(session, "remember", json.RawMessage(original), &first)
	// Receive HTTP success headers, then deliberately discard the acknowledgment
	// without decoding or saving its IDs. A later SDK retry must find that commit.
	const lost = `{"space":"Process","idempotency_key":"lost-ack","entries":[{"kind":"topic","title":"Lost acknowledgment"}]}`
	body := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"remember","arguments":` + lost + `}}`
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "memory.internal:8080"
	request.Header.Set("Authorization", "Bearer "+token())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("lost-ack request did not commit")
	}
	var lostReceipt mcpserver.RememberReceipt
	call(session, "remember", json.RawMessage(lost), &lostReceipt)
	query := memory.Query{Spaces: []string{"Process"}, Limit: 1, ContextLimit: 1, LinkLimit: 1}
	var page memory.Page
	call(session, "retrieve", query, &page)
	if page.NextCursor == "" {
		t.Fatal("fixture needs a persisted continuation")
	}
	query.Cursor = page.NextCursor
	admin("disable")
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("running service retained disabled enrollment")
	}
	if !bytes.Equal(enrollment, admin("enroll")) {
		t.Fatal("operator re-enrollment changed stable account")
	}
	session.Close()
	p.stop(t)
	for _, secret := range []string{subject, database, jwks, "9007199254740993", "Bearer"} {
		if strings.Contains(p.log.String(), secret) {
			t.Fatal("production service logged private input")
		}
	}
	p = start(t, binary, env, endpoint)
	session = connect()
	var replay mcpserver.RememberReceipt
	call(session, "remember", json.RawMessage(original), &replay)
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("restart lost durable replay")
	}
	var lostReplay mcpserver.RememberReceipt
	call(session, "remember", json.RawMessage(lost), &lostReplay)
	if !reflect.DeepEqual(lostReceipt, lostReplay) {
		t.Fatal("restart lost committed-but-unacknowledged replay")
	}
	var continued memory.Page
	call(session, "retrieve", query, &continued)
	if continued.SnapshotSequence != page.SnapshotSequence {
		t.Fatal("restart invalidated persisted cursor")
	}
	var all memory.Page
	call(session, "retrieve", memory.Query{Spaces: []string{"Process"}}, &all)
	if len(all.Matches) != 3 {
		t.Fatal("restart/replay duplicated or lost records")
	}
	precise := false
	for _, record := range all.Matches {
		if bytes.Contains(record.Payload, []byte("9007199254740993")) {
			precise = true
		}
	}
	if !precise {
		t.Fatal("process transport changed numeric payload")
	}
	session.Close()
	p.stop(t)
	// No environment or test-only flag permits an unauthenticated production
	// service, and invalid pinned keys also fail before listening.
	for _, badEnv := range [][]string{environment(), append(append(environment(), env...), "MEMORY_GATEWAY_JWKS_FILE="+subjectFile)} {
		cmd := exec.CommandContext(ctx, binary, "serve")
		cmd.Env = badEnv
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("unconfigured/invalid-key production command started")
		}
		if bytes.Contains(output, []byte(subjectFile)) || bytes.Contains(output, []byte(subject)) {
			t.Fatal("startup failure exposed private input")
		}
	}
}
