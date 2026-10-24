// Package examplemcp implements a harmless gateway-authenticated MCP backend.
package examplemcp

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type principalKey struct{}

type Echo struct {
	Value string `json:"value"`
}

type AddInput struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
}

type AddResult struct {
	Sum float64 `json:"sum"`
}

// New requires signed gateway identity on every MCP request. The example keeps
// no user data: whoami shows the original issuer/subject pair an application
// would use for its own account mapping and ownership checks.
func New(config trust.Config, clientID, requiredScope string, jsonResponse bool) (http.Handler, error) {
	if clientID == "" || requiredScope == "" {
		return nil, errors.New("example backend requires a client and scope")
	}
	verifier, err := trust.NewVerifier(config)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "gateway-example", Version: "0.1.0"}, nil)
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Return the supplied text without side effects.", Annotations: annotations},
		func(_ context.Context, _ *mcp.CallToolRequest, input Echo) (*mcp.CallToolResult, Echo, error) {
			return nil, input, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "add", Description: "Add two numbers without side effects.", Annotations: annotations},
		func(_ context.Context, _ *mcp.CallToolRequest, input AddInput) (*mcp.CallToolResult, AddResult, error) {
			sum := input.A + input.B
			if math.IsInf(sum, 0) || math.IsNaN(sum) {
				return nil, AddResult{}, errors.New("sum must be finite")
			}
			return nil, AddResult{Sum: sum}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "whoami", Description: "Return the verified caller identity and granted permissions.", Annotations: annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, trust.Principal, error) {
			return nil, ctx.Value(principalKey{}).(trust.Principal), nil
		})
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: jsonResponse, MaxRequestBodyBytes: 128 << 10,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Authorization")) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		principal, err := verifier.Verify(parts[1])
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if principal.ClientID != clientID || !slices.Contains(strings.Fields(principal.Scope), requiredScope) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		transport.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	}))
	return mux, nil
}
