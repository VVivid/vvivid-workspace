package daemon_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/daemon"
	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ingress"
	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
	"github.com/VVivid/vvivid-workspace/WebhookControl/managementhttp"
	"github.com/go-jose/go-jose/v4"
)

var daemonBinary, adminBinary string

// Build the actual race-instrumented executables once, then exercise their
// process boundaries, signals, command-line provisioning and on-disk recovery.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "webhook-control-executables-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create executable test directory failed")
		os.Exit(1)
	}
	daemonBinary, adminBinary = filepath.Join(dir, "webhook-control"), filepath.Join(dir, "webhook-control-admin")
	cmd := exec.Command("go", "build", "-race", "-o", dir, "./cmd/webhook-control", "./cmd/webhook-control-admin")
	cmd.Dir = ".."
	if output, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build executable fixtures failed: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type received struct {
	id, authorization string
	body              []byte
	at                time.Time
}

type fixture struct {
	t                      *testing.T
	dir                    string
	cfg                    daemon.Config
	key, inbound, outbound []byte
	issuer                 *httptest.Server
	signingKey             *rsa.PrivateKey
	consumer               *httptest.Server
	consumerStatus         atomic.Int64
	received               chan received
	config                 domain.Configuration
	client                 *http.Client
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), received: make(chan received, 128), client: &http.Client{Timeout: 3 * time.Second}}
	var err error
	f.signingKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": f.issuer.URL, "jwks_uri": f.issuer.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.signingKey.PublicKey, KeyID: "synthetic", Algorithm: "RS256", Use: "sig"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.issuer.Close)
	f.consumerStatus.Store(204)
	f.consumer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 65537))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		f.received <- received{id: r.Header.Get("Idempotency-Key"), authorization: r.Header.Get("Authorization"), body: body, at: time.Now()}
		status := f.consumerStatus.Load()
		if status == -1 {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(int(status))
	}))
	t.Cleanup(f.consumer.Close)
	f.key = make([]byte, 32)
	digest := make([]byte, 32)
	if _, err := rand.Read(f.key); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(digest); err != nil {
		t.Fatal(err)
	}
	f.inbound, f.outbound = []byte("synthetic-source-HMAC-secret-for-tests-only"), []byte("synthetic-receiver-credential-for-tests-only")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	f.cfg = daemon.Config{DatabasePath: filepath.Join(f.dir, "state.db"), KeyFile: f.file("master.key", f.key), DigestKeyFile: f.file("digest.key", digest), Listen: address, OAuth: managementhttp.OAuthConfig{Issuer: f.issuer.URL, Resource: "http://" + address + "/mcp", AllowInsecureLoopback: true}, DestinationOrigins: []httptransport.OriginRule{{Origin: f.consumer.URL, AllowPrivate: true}}, WorkerPollMilliseconds: 20, WorkerLeaseSeconds: 2, WorkerRenewSeconds: 1, WorkerInitialBackoffSeconds: 1, WorkerMaxBackoffSeconds: 1, CleanupIntervalSeconds: 1, ShutdownTimeoutSeconds: 2}
	f.config = domain.Configuration{
		Source:       domain.Source{ID: "source-a", Revision: 1, Enabled: true, Name: "Synthetic acceptance", Auth: domain.SourceAuth{Kind: domain.AuthHMAC, Secret: domain.SecretRef{Name: "inbound", Version: "v1"}}, OriginalTTL: 0, DeliveryTTL: 60},
		Rules:        []domain.Rule{{ID: "rule-a", Revision: 1, SourceID: "source-a", Expression: "event.kind == 'notice'"}},
		Templates:    []domain.Template{{ID: "template-a", Revision: 1, SourceID: "source-a", Fields: map[string]string{"message": "event.message", "contact": "mask(event.contact)"}}},
		Transporters: []domain.Transporter{{ID: "http-a", Revision: 1, SourceID: "source-a", Kind: domain.TransporterHTTP, HTTP: domain.HTTPConfig{URL: f.consumer.URL + "/receive", TimeoutSeconds: 3, SecretHeaders: map[string]domain.SecretRef{"Authorization": {Name: "outbound", Version: "v1"}}}}},
		Routes:       []domain.Route{{ID: "route-a", Revision: 1, SourceID: "source-a", Enabled: true, Rules: []domain.RevisionRef{{ID: "rule-a", Revision: 1}}, Template: domain.RevisionRef{ID: "template-a", Revision: 1}, Transporters: []domain.RevisionRef{{ID: "http-a", Revision: 1}}}},
	}
	return f
}

func (f *fixture) file(name string, raw []byte) string {
	f.t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func encode(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func (f *fixture) admin(args ...string) ([]byte, error) {
	f.t.Helper()
	base := []string{"-db", f.cfg.DatabasePath, "-key-file", f.cfg.KeyFile, "-actor-issuer", f.issuer.URL, "-actor-subject", "agent"}
	cmd := exec.CommandContext(f.t.Context(), adminBinary, append(base, args...)...)
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	output, err := cmd.CombinedOutput()
	f.noSecrets(output)
	return output, err
}

func (f *fixture) mustAdmin(args ...string) []byte {
	f.t.Helper()
	out, err := f.admin(args...)
	if err != nil {
		f.t.Fatalf("local admin command %s failed: %v %s", args[0], err, out)
	}
	return out
}

func (f *fixture) provision() {
	f.t.Helper()
	f.mustAdmin("secret-put", "-name", "inbound", "-version", "v1", "-purpose", "source-auth", "-value-file", f.file("inbound.secret", f.inbound))
	f.mustAdmin("secret-put", "-name", "outbound", "-version", "v1", "-purpose", "http-header", "-value-file", f.file("outbound.secret", f.outbound))
	grant := domain.Grant{ID: "agent-a", SourceIDs: []domain.ID{"source-a"}, Actions: []domain.Action{domain.ActionReadConfig, domain.ActionWriteConfig, domain.ActionActivateConfig, domain.ActionPreview, domain.ActionReadEvents, domain.ActionReplayEvents, domain.ActionReadDeliveries, domain.ActionRetryDelivery}, DestinationOrigins: []string{f.consumer.URL}, SecretNames: []string{"inbound", "outbound"}, OriginalRetention: domain.RetentionLimit{MaxSeconds: 120}, DeliveryRetention: domain.RetentionLimit{MaxSeconds: 120}}
	f.mustAdmin("grant-put", "-issuer", f.issuer.URL, "-subject", "agent", "-file", f.file("grant.json", encode(grant)))
	policy := f.file("policy.json", encode(f.cfg.DestinationOrigins))
	f.mustAdmin("invoke", "-policy-file", policy, "-operation", "config_put", "-file", f.file("put.json", encode(management.Request[management.PutInput]{SourceID: "source-a", ExpectedRevision: new(domain.Revision(0)), Input: management.PutInput{Configuration: f.config}})))
	f.mustAdmin("invoke", "-policy-file", policy, "-operation", "config_activate", "-file", f.file("activate.json", []byte(`{"source_id":"source-a","expected_revision":0,"input":{"draft_revision":1}}`)))
}

type process struct {
	f       *fixture
	command *exec.Cmd
	output  bytes.Buffer
	done    chan error
	exited  bool
}

func (f *fixture) launch() *process {
	f.t.Helper()
	p := &process{f: f, done: make(chan error, 1)}
	path := f.file("daemon.json", encode(f.cfg))
	p.command = exec.Command(daemonBinary, "-config", path)
	// Keep race detection enabled while avoiding the race runtime's one-second
	// post-exit delay for each short-lived synthetic CLI/service process.
	p.command.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	p.command.Stdout, p.command.Stderr = &p.output, &p.output
	if err := p.command.Start(); err != nil {
		f.t.Fatal(err)
	}
	go func() { p.done <- p.command.Wait() }()
	f.t.Cleanup(func() {
		if !p.exited {
			p.command.Process.Kill()
			<-p.done
			p.exited = true
		}
		f.noSecrets(p.output.Bytes())
	})
	return p
}

func (f *fixture) start() *process {
	f.t.Helper()
	p := f.launch()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.done:
			p.exited = true
			f.noSecrets(p.output.Bytes())
			f.t.Fatalf("daemon exited before readiness: %v %s", err, p.output.Bytes())
		default:
		}
		response, err := f.client.Get("http://" + f.cfg.Listen + "/readyz")
		if err == nil {
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				if string(raw) != "{\"status\":\"ok\"}\n" {
					f.t.Fatal("readiness disclosed details")
				}
				return p
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("daemon never became ready")
	return nil
}

func (p *process) wait(success bool) {
	p.f.t.Helper()
	select {
	case err := <-p.done:
		p.exited = true
		p.f.noSecrets(p.output.Bytes())
		if (err == nil) != success {
			p.f.t.Fatalf("daemon exit success=%v want=%v logs=%s", err == nil, success, p.output.Bytes())
		}
	case <-time.After(5 * time.Second):
		p.f.t.Fatal("daemon failed to stop promptly")
	}
}
func (p *process) stop() {
	p.f.t.Helper()
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
		p.f.t.Fatal(err)
	}
	p.wait(true)
}

func (f *fixture) token(subject string, expired bool) string {
	f.t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: f.signingKey, KeyID: "synthetic"}}, new(jose.SignerOptions).WithType("at+jwt"))
	if err != nil {
		f.t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	if expired {
		expiry = time.Now().Add(-time.Hour)
	}
	object, err := signer.Sign(encode(map[string]any{"iss": f.issuer.URL, "aud": f.cfg.OAuth.Resource, "sub": subject, "exp": expiry.Unix(), "scope": managementhttp.Scope}))
	if err != nil {
		f.t.Fatal(err)
	}
	token, err := object.CompactSerialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

func (f *fixture) manage(operation, source string, input any, token string) (int, []byte) {
	f.t.Helper()
	raw := encode(management.Request[any]{SourceID: domain.ID(source), Input: input})
	req, _ := http.NewRequestWithContext(f.t.Context(), "POST", "http://"+f.cfg.Listen+"/v1/manage/"+operation, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	f.noSecrets(body)
	return resp.StatusCode, body
}

func (f *fixture) send(key string, raw, secret []byte) (int, ingress.Response, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(f.t.Context(), "POST", "http://"+f.cfg.Listen+"/v1/hooks/source-a", bytes.NewReader(raw))
	if err != nil {
		return 0, ingress.Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Webhook-Timestamp", timestamp)
	req.Header.Set("X-Webhook-Signature", ingress.HMACSignature(secret, "source-a", key, timestamp, raw))
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, ingress.Response{}, err
	}
	defer resp.Body.Close()
	var result ingress.Response
	if resp.StatusCode == 200 || resp.StatusCode == 202 {
		err = json.NewDecoder(resp.Body).Decode(&result)
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, result, err
}

func (f *fixture) mustSend(key string, raw []byte, want int) ingress.Response {
	f.t.Helper()
	status, result, err := f.send(key, raw, f.inbound)
	if err != nil || status != want {
		f.t.Fatalf("ingress status=%d want=%d err=%v", status, want, err)
	}
	return result
}

func (f *fixture) receive() received {
	f.t.Helper()
	select {
	case result := <-f.received:
		return result
	case <-time.After(5 * time.Second):
		f.t.Fatal("receiver did not get delivery")
		return received{}
	}
}

func (f *fixture) waitState(id domain.ID, state domain.DeliveryState) {
	f.t.Helper()
	token := f.token("agent", false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, raw := f.manage("deliveries_get", "source-a", management.DeliveryInput{DeliveryID: id}, token)
		var result domain.DeliveryStatus
		if status == 200 && json.Unmarshal(raw, &result) == nil && result.State == state {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("delivery never reached %s", state)
}

func (f *fixture) noSecrets(raw []byte) {
	f.t.Helper()
	for _, forbidden := range [][]byte{f.key, f.inbound, f.outbound, []byte("private-original-only-marker"), []byte("private-contact-marker")} {
		if bytes.Contains(raw, forbidden) {
			f.t.Fatal("output exposed synthetic secret or payload")
		}
	}
}

func (f *fixture) db() *sql.DB {
	f.t.Helper()
	db, err := sql.Open("sqlite", f.cfg.DatabasePath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { db.Close() })
	return db
}

func (f *fixture) assertEmptyAcceptance() {
	f.t.Helper()
	db := f.db()
	for _, table := range []string{"events", "originals", "deduplication", "deliveries", "delivery_payloads"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			f.t.Fatalf("acceptance rollback %s count=%d err=%v", table, count, err)
		}
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
