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
			mcp.AddTool(backend, &mcp.Tool{Name: "echo", Description: "Echo a value"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
				Value string `json:"value"`
			}) (*mcp.CallToolResult, struct {
				Value string `json:"value"`
			}, error) {
				return nil, in, nil
			})
			transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backend }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: jsonResponse})
			upstream := httptest.NewServer(transport)
			defer upstream.Close()
			f.config.Routes[0].Upstream = upstream.URL + "/mcp"
			f.config.Routes[0].UpstreamHost = ""
			gateway := httptest.NewServer(f.build(t))
			defer gateway.Close()
			token := f.token(t, "rfc9068", "/shopping/mcp", nil)
			client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: gateway.URL + "/shopping/mcp", HTTPClient: &http.Client{Transport: accessTransport{token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			catalog, err := session.ListTools(t.Context(), nil)
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
		})
	}
}
