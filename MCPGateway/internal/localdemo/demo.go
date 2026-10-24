// Package localdemo runs a development-only acceptance scenario. Its mock issuer
// is never imported by the production gateway or enabled by gateway configuration.
package localdemo

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/VVivid/vvivid-workspace/MCPGateway/gateway"
	"github.com/VVivid/vvivid-workspace/MCPGateway/internal/examplemcp"
	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const demoClient = "demo-agent"

type backend struct {
	url            string
	requests       atomic.Int64
	identityTokens chan string
}

// Run starts the actual gateway executable and two sample backend handlers on
// ephemeral loopback ports. All signing material and tokens are generated for
// this invocation, never printed, and discarded on exit. The entire scenario
// has a 30-second budget, followed by bounded server/process cleanup.
func Run(ctx context.Context, gatewayBinary string, output io.Writer) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "mcp-gateway-demo-")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(dir)) }()
	gatewayKey, err := newKey("local-gateway")
	if err != nil {
		return err
	}
	issuerKey, err := newKey("local-issuer")
	if err != nil {
		return err
	}
	privateFile := filepath.Join(dir, "gateway.private.json")
	publicFile := filepath.Join(dir, "gateway.public.json")
	if err := writeJSON(privateFile, gatewayKey); err != nil {
		return err
	}
	if err := writeJSON(publicFile, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{gatewayKey.Public()}}); err != nil {
		return err
	}
	issuer, closeIssuer, err := serveLocal(func(origin string) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"issuer": origin, "jwks_uri": origin + "/keys"})
		})
		mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{issuerKey.Public()}})
		})
		return mux
	})
	if err != nil {
		return err
	}
	defer closeIssuer()

	// Reserve an ephemeral port until configuration is ready. Closing it just
	// before exec lets the unmodified production binary bind its normal listener.
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer reservation.Close()
	address := reservation.Addr().String()
	origin := "http://" + address
	cfg := gateway.Config{Listen: address, Origin: origin, Issuer: issuer, SigningKeyFile: privateFile}
	backends := make([]*backend, 0, 2)
	for _, name := range []string{"service-a", "service-b"} {
		scope := name + ":use"
		handler, err := examplemcp.New(trust.Config{Issuer: origin, Audience: name + "-mcp", JWKSFile: publicFile}, demoClient, scope, name == "service-a")
		if err != nil {
			return err
		}
		b := &backend{identityTokens: make(chan string, 64)}
		backendURL, closeBackend, err := serveLocal(func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b.requests.Add(1)
				select {
				case b.identityTokens <- strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "):
				default:
				}
				handler.ServeHTTP(w, r)
			})
		})
		if err != nil {
			return err
		}
		defer closeBackend()
		b.url = backendURL
		backends = append(backends, b)
		cfg.Routes = append(cfg.Routes, gateway.Route{
			Name: name, Path: "/" + name + "/mcp", Upstream: b.url + "/mcp", Audience: name + "-mcp",
			RequiredScopes: []string{scope}, ForwardScopes: []string{scope}, ClientIDs: []string{demoClient}, MaxTokenLifetimeSeconds: 300,
		})
	}
	configFile := filepath.Join(dir, "gateway.json")
	if err := writeJSON(configFile, cfg); err != nil {
		return err
	}
	if err := reservation.Close(); err != nil {
		return err
	}
	processCtx, stopProcess := context.WithCancel(ctx)
	defer stopProcess()
	command := exec.CommandContext(processCtx, gatewayBinary, "--config", configFile)
	// Gateway logs are unnecessary for this scenario and may be configured
	// differently in a developer's build. Do not relay subprocess output.
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 3 * time.Second
	if err := command.Start(); err != nil {
		return fmt.Errorf("start gateway: %w", err)
	}
	processDone := make(chan struct{})
	go func() {
		command.Wait()
		close(processDone)
	}()
	defer func() {
		stopProcess()
		select {
		case <-processDone:
		case <-time.After(5 * time.Second):
			command.Process.Kill()
			resultErr = errors.Join(resultErr, errors.New("gateway cleanup exceeded five seconds"))
		}
	}()
	if err := waitReady(ctx, origin, processDone); err != nil {
		return err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: issuerKey}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		return err
	}
	mintAccessToken := func(subject, path, client, scope string, lifetime time.Duration) (string, error) {
		now := time.Now()
		return jwt.Signed(signer).Claims(jwt.Claims{
			Issuer: issuer, Subject: subject, Audience: jwt.Audience{origin + path},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(lifetime)),
		}).Claims(map[string]any{"client_id": client, "scope": scope}).Serialize()
	}
	for _, route := range cfg.Routes {
		if err := checkMetadata(ctx, origin, issuer, route.Path); err != nil {
			return err
		}
		for _, subject := range []string{"alice", "bob"} {
			token, err := mintAccessToken(subject, route.Path, demoClient, route.RequiredScopes[0]+" unrelated:admin", 2*time.Minute)
			if err != nil {
				return err
			}
			want := trust.Principal{Issuer: issuer, Subject: subject, ClientID: demoClient, Scope: route.RequiredScopes[0]}
			if err := exerciseAgent(ctx, origin+route.Path, token, want); err != nil {
				return fmt.Errorf("%s agent scenario: %w", route.Name, err)
			}
		}
		fmt.Fprintf(output, "%s: protocol 2026-07-28; discovered add, echo, whoami; echo(hello)=hello; add(2,3)=5\n", route.Name)
	}
	fmt.Fprintln(output, "Alice and Bob: original issuer/subject and client preserved; forwarded scopes attenuated")

	before := backends[0].requests.Load() + backends[1].requests.Load()
	for _, denied := range []struct {
		name, subject, tokenPath, requestPath, clientID, scope string
		lifetime                                               time.Duration
		status                                                 int
	}{
		{name: "missing token", requestPath: "/service-a/mcp", status: http.StatusUnauthorized},
		{
			name: "wrong audience", subject: "alice", tokenPath: "/service-a/mcp", requestPath: "/service-b/mcp",
			clientID: demoClient, scope: "service-a:use service-b:use", lifetime: time.Minute, status: http.StatusUnauthorized,
		},
		{
			name: "wrong client", subject: "alice", tokenPath: "/service-a/mcp", requestPath: "/service-a/mcp",
			clientID: "other-agent", scope: "service-a:use", lifetime: time.Minute, status: http.StatusForbidden,
		},
		{
			name: "missing scope", subject: "alice", tokenPath: "/service-a/mcp", requestPath: "/service-a/mcp",
			clientID: demoClient, scope: "unrelated:admin", lifetime: time.Minute, status: http.StatusForbidden,
		},
		{
			name: "expired token", subject: "alice", tokenPath: "/service-a/mcp", requestPath: "/service-a/mcp",
			clientID: demoClient, scope: "service-a:use", lifetime: -time.Minute, status: http.StatusUnauthorized,
		},
		{
			name: "excessive lifetime", subject: "alice", tokenPath: "/service-a/mcp", requestPath: "/service-a/mcp",
			clientID: demoClient, scope: "service-a:use", lifetime: time.Hour, status: http.StatusUnauthorized,
		},
	} {
		token := ""
		if denied.subject != "" {
			token, err = mintAccessToken(denied.subject, denied.tokenPath, denied.clientID, denied.scope, denied.lifetime)
			if err != nil {
				return err
			}
		}
		if err := checkStatus(ctx, origin+denied.requestPath, token, denied.status); err != nil {
			return fmt.Errorf("%s: %w", denied.name, err)
		}
	}
	externalToken, err := mintAccessToken("alice", "/service-a/mcp", demoClient, "service-a:use", time.Minute)
	if err != nil {
		return err
	}
	if err := checkStatus(ctx, origin+"/service-a/mcp", externalToken+"tampered", http.StatusUnauthorized); err != nil {
		return fmt.Errorf("forged token: %w", err)
	}
	if after := backends[0].requests.Load() + backends[1].requests.Load(); after != before {
		return errors.New("denied gateway request reached a backend")
	}
	// These tokens remain in memory only. A service-a identity must fail at
	// service-b, and neither token class may be substituted at the other boundary.
	var backendToken string
	select {
	case backendToken = <-backends[0].identityTokens:
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, boundary := range []struct{ endpoint, token string }{
		{backends[1].url + "/mcp", backendToken},
		{backends[0].url + "/mcp", externalToken},
		{origin + "/service-a/mcp", backendToken},
	} {
		if err := checkStatus(ctx, boundary.endpoint, boundary.token, http.StatusUnauthorized); err != nil {
			return fmt.Errorf("backend identity isolation: %w", err)
		}
	}
	fmt.Fprintln(output, "Denied: missing/forged/expired/long-lived tokens, wrong client/scope/audience, and token substitution")
	return nil
}

func newKey(id string) (jose.JSONWebKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	return jose.JSONWebKey{Key: key, KeyID: id, Algorithm: "RS256", Use: "sig"}, err
}

func writeJSON(file string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0600)
}

func serveLocal(handler func(origin string) http.Handler) (string, func(), error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	origin := "http://" + listener.Addr().String()
	server := &http.Server{Handler: handler(origin), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 32 << 10}
	go server.Serve(listener)
	return origin, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			server.Close()
		}
	}, nil
}

func waitReady(ctx context.Context, origin string, processDone <-chan struct{}) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for tick := time.Tick(25 * time.Millisecond); ; {
		select {
		case <-processDone:
			return errors.New("gateway exited before readiness")
		case <-ctx.Done():
			return fmt.Errorf("gateway readiness: %w", ctx.Err())
		case <-tick:
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/health/ready", nil)
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

func checkMetadata(ctx context.Context, origin, issuer, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/.well-known/oauth-protected-resource"+path, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return errors.New("protected-resource discovery failed")
	}
	defer response.Body.Close()
	var metadata struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&metadata) != nil || metadata.Resource != origin+path || !slices.Equal(metadata.AuthorizationServers, []string{issuer}) {
		return errors.New("incorrect protected-resource metadata")
	}
	return nil
}

type accessTransport struct{ token string }

func (a accessTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+a.token)
	copy.Header.Set("X-Forwarded-User", "mallory")
	copy.Header.Set("Mcp-Identity", "forged")
	copy.Header.Set("Cookie", "untrusted=client")
	return http.DefaultTransport.RoundTrip(copy)
}

func exerciseAgent(ctx context.Context, endpoint, token string, want trust.Principal) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "local-demo-agent", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &http.Client{Transport: accessTransport{token}, Timeout: 5 * time.Second},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return errors.New("MCP initialization failed")
	}
	defer session.Close()
	if session.InitializeResult().ProtocolVersion != "2026-07-28" {
		return errors.New("unexpected negotiated protocol")
	}
	catalog, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return errors.New("tool discovery failed")
	}
	names := make([]string, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"add", "echo", "whoami"}) {
		return errors.New("unexpected tool catalog")
	}
	var echo examplemcp.Echo
	if err := callTool(ctx, session, "echo", examplemcp.Echo{Value: "hello"}, &echo); err != nil {
		return err
	}
	if echo.Value != "hello" {
		return errors.New("echo result changed")
	}
	var sum examplemcp.AddResult
	if err := callTool(ctx, session, "add", examplemcp.AddInput{A: 2, B: 3}, &sum); err != nil {
		return err
	}
	if sum.Sum != 5 {
		return errors.New("incorrect addition result")
	}
	var identity trust.Principal
	if err := callTool(ctx, session, "whoami", struct{}{}, &identity); err != nil {
		return err
	}
	if identity.Issuer != want.Issuer || identity.Subject != want.Subject || identity.ClientID != want.ClientID || identity.Scope != want.Scope {
		return errors.New("caller identity changed or permissions escalated")
	}
	return nil
}

func callTool(ctx context.Context, session *mcp.ClientSession, name string, arguments, target any) error {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil || result.IsError {
		return fmt.Errorf("%s call failed", name)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("%s result encoding failed", name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%s result decoding failed", name)
	}
	return nil
}

func checkStatus(ctx context.Context, endpoint, token string, want int) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("X-Forwarded-User", "alice")
	request.Header.Set("Mcp-Identity", "forged")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return errors.New("denial request failed")
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
	if response.StatusCode != want {
		return fmt.Errorf("status %d, want %d", response.StatusCode, want)
	}
	return nil
}
