package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type accessTransport struct{ token string }

func (a accessTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+a.token)
	copy.Header.Set("Cookie", "private=client")
	copy.Header.Set("Mcp-Identity", "must-not-forward")
	copy.Host = "gateway.example"
	return http.DefaultTransport.RoundTrip(copy)
}
func TestOfficialMCPClientThroughGateway(t *testing.T) {
	for _, jsonResponse := range []bool{true, false} {
		name := "SSE"
		if jsonResponse {
			name = "JSON"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			backend := mcp.NewServer(&mcp.Implementation{Name: "test-backend", Version: "1"}, nil)
			mcp.AddTool(backend, &mcp.Tool{Name: "echo", Description: "Echo a value", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "x-mcp-header": "Value"}}, "required": []string{"value"},
			}}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
				Value string `json:"value"`
			}) (*mcp.CallToolResult, struct {
				Value string `json:"value"`
			}, error) {
				return nil, in, nil
			})
			// A cached catalog lets the SDK derive parameter headers from the schema.
			backend.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					result, err := next(ctx, method, req)
					if catalog, ok := result.(*mcp.ListToolsResult); ok && err == nil {
						catalog.TTLMs = 60000
					}
					return result, err
				}
			})
			transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backend }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: jsonResponse})
			seen := make(chan http.Header, 16)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				writeBackendInformationalResponses(w)
				transport.ServeHTTP(w, r)
			}))
			defer upstream.Close()
			f.config.Routes[0].Upstream = upstream.URL + "/mcp"
			f.config.Routes[0].UpstreamHost = ""
			gateway := httptest.NewServer(f.build(t))
			defer gateway.Close()
			token := f.token(t, "rfc9068", "/service-a/mcp", nil)
			client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: gateway.URL + "/service-a/mcp", HTTPClient: &http.Client{Transport: accessTransport{token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
				t.Fatalf("negotiated protocol = %q, want 2026-07-28", got)
			}
			catalog, err := session.ListTools(t.Context(), &mcp.ListToolsParams{})
			if err != nil || len(catalog.Tools) != 1 || catalog.Tools[0].Name != "echo" {
				t.Fatalf("tools: %+v %v", catalog, err)
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"value": "hello"}})
			if err != nil || result.IsError {
				t.Fatalf("call: %+v %v", result, err)
			}
			if len(result.Content) == 0 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "hello") {
				t.Fatal("result changed in transit")
			}
			var callHeaders http.Header
			for len(seen) > 0 {
				headers := <-seen
				if headers.Get("Cookie") != "" || headers.Get("Mcp-Identity") != "" {
					t.Fatal("non-protocol metadata reached backend")
				}
				if headers.Get("Mcp-Method") == "tools/call" {
					callHeaders = headers
				}
			}
			for name, want := range map[string]string{"Mcp-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": "echo", "Mcp-Param-Value": "hello"} {
				if got := callHeaders.Get(name); got != want {
					t.Errorf("backend %s = %q, want %q", name, got, want)
				}
			}
		})
	}
}
