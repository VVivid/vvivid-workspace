// example-mcp is a small gateway-authenticated reference backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"os"
	"strings"
	"time"
)

type principalKey struct{}

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "internal listener")
	issuer := flag.String("gateway-issuer", "", "trusted gateway origin")
	audience := flag.String("audience", "example-mcp", "backend audience")
	keys := flag.String("jwks", "", "public gateway JWKS file")
	flag.Parse()
	verifier, err := trust.NewVerifier(trust.Config{Issuer: *issuer, Audience: *audience, JWKSFile: *keys})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "gateway-example", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "whoami", Description: "Return the verified caller identity; requires example:read."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, trust.Principal, error) {
		p := ctx.Value(principalKey{}).(trust.Principal)
		return nil, p, nil
	})
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 128 << 10, DisableLocalhostProtection: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Authorization")) != 1 {
			http.Error(w, "Unauthorized", 401)
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, "Unauthorized", 401)
			return
		}
		p, err := verifier.Verify(parts[1])
		if err != nil {
			http.Error(w, "Unauthorized", 401)
			return
		}
		allowed := false
		for _, scope := range strings.Fields(p.Scope) {
			if scope == "example:read" {
				allowed = true
			}
		}
		if !allowed {
			http.Error(w, "Forbidden", 403)
			return
		}
		transport.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}))
	httpServer := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	if err := httpServer.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
