package managementhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"

	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type Config struct {
	OAuth          OAuthConfig
	AllowedOrigins []string
}

// NewHandler owns no listener or worker. The daemon can mount this handler next
// to ingress while tests exercise real SDK streamable HTTP clients independently.
func NewHandler(config Config, service *management.Service, verifier auth.TokenVerifier) (http.Handler, error) {
	resource, err := trustedURL(config.OAuth.Resource, config.OAuth.AllowInsecureLoopback)
	if err != nil {
		return nil, err
	}
	if _, err := trustedURL(config.OAuth.Issuer, config.OAuth.AllowInsecureLoopback); err != nil {
		return nil, err
	}
	if resource.Path != "/mcp" || service == nil || verifier == nil {
		return nil, errors.New("management requires pinned /mcp resource, service and verifier")
	}
	origin := resource.Scheme + "://" + resource.Host
	origins := append([]string{origin}, config.AllowedOrigins...)
	for _, raw := range origins {
		u, err := trustedURL(raw, config.OAuth.AllowInsecureLoopback)
		if err != nil || u.Path != "" {
			return nil, errors.New("invalid management origin")
		}
	}
	metadataURL := origin + "/.well-known/oauth-protected-resource"
	metadata := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource: config.OAuth.Resource, AuthorizationServers: []string{config.OAuth.Issuer},
		ScopesSupported: []string{Scope}, BearerMethodsSupported: []string{"header"},
	})
	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/oauth-protected-resource", metadata)
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp", metadata)
	mux.HandleFunc("POST /v1/manage/{operation}", func(w http.ResponseWriter, r *http.Request) {
		actor, err := identity(auth.TokenInfoFromContext(r.Context()))
		if err != nil {
			writeError(w, &management.Error{Code: "unauthenticated", Message: "Verified identity required", Status: 401})
			return
		}
		media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		validParams := len(params) == 0 || len(params) == 1 && params["charset"] == "utf-8"
		encoding := r.Header.Get("Content-Encoding")
		validEncoding := encoding == "" || encoding == "identity"
		if err != nil || media != "application/json" || !validParams || !validEncoding {
			writeError(w, &management.Error{Code: "invalid_request", Message: "Uncompressed application/json is required", Status: 400})
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			writeError(w, &management.Error{Code: "request_too_large", Message: "Request exceeds management limit", Status: 413})
			return
		}
		out, err := service.Execute(r.Context(), actor, r.PathValue("operation"), raw)
		if err != nil {
			writeError(w, management.PublicError(err))
			return
		}
		writeJSON(w, out)
	})
	transport := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		actor, err := identity(auth.TokenInfoFromContext(r.Context()))
		if err != nil {
			return nil
		}
		ops, err := service.PermittedOperations(r.Context(), actor)
		if err != nil {
			return nil
		}
		server := mcp.NewServer(&mcp.Implementation{Name: "webhook-control", Version: "0.1.0"}, nil)
		// Register the fixed public registry so a call to an unlisted operation
		// still receives the shared stable authorization error. Filter discovery
		// separately; stale listings never confer permission to execute.
		for _, op := range management.Operations() {
			server.AddTool(&mcp.Tool{Name: op.Name, Description: op.Description, InputSchema: op.InputSchema, OutputSchema: op.OutputSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				// Use identity from the actual HTTP request, never from tool input
				// or a cached tools/list. Execute rechecks all resource authority.
				var info *auth.TokenInfo
				if req.Extra != nil {
					info = req.Extra.TokenInfo
				}
				actual, err := identity(info)
				if err != nil {
					return toolResult(nil, &management.Error{Code: "unauthenticated", Message: "Verified identity required", Status: 401}), nil
				}
				out, err := service.Execute(ctx, actual, op.Name, req.Params.Arguments)
				return toolResult(out, err), nil
			})
		}
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				result, err := next(ctx, method, req)
				if err == nil && method == "tools/list" {
					if listed, ok := result.(*mcp.ListToolsResult); ok {
						listed.Cacheable = mcp.Cacheable{TTLMs: 0, CacheScope: "private"}
						listed.Tools = slices.DeleteFunc(listed.Tools, func(tool *mcp.Tool) bool {
							return !slices.ContainsFunc(ops, func(op management.Operation) bool { return op.Name == tool.Name })
						})
					}
				}
				return result, err
			}
		})
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 2 << 20})
	mux.Handle("/mcp", transport)
	protected := requireBearerTokenJSON(verifier, &auth.RequireBearerTokenOptions{ResourceMetadataURL: metadataURL, Scopes: []string{Scope}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := identity(auth.TokenInfoFromContext(r.Context()))
		if err != nil {
			writeError(w, &management.Error{Code: "unauthenticated", Message: "Verified identity required", Status: 401})
			return
		}
		if _, err := service.PermittedOperations(r.Context(), actor); err != nil {
			writeError(w, management.PublicError(err))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Origin")) > 1 || r.Header.Get("Origin") != "" && !slices.Contains(origins, r.Header.Get("Origin")) {
			writeError(w, &management.Error{Code: "forbidden_origin", Message: "Origin is not permitted", Status: 403})
			return
		}
		if r.URL.Path == "/.well-known/oauth-protected-resource" || r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if len(r.Header.Values("Authorization")) > 1 {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`", scope="`+Scope+`"`)
			writeError(w, &management.Error{Code: "unauthenticated", Message: "Exactly one bearer credential required", Status: 401})
			return
		}
		protected.ServeHTTP(w, r)
	}), nil
}

// The SDK owns token verification, scope checks and OAuth challenges. Its failure
// body is discarded so both transports return the public JSON error contract.
// Successful authentication passes the original writer to preserve MCP streaming.
func requireBearerTokenJSON(verifier auth.TokenVerifier, options *auth.RequireBearerTokenOptions, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failure := &authFailureWriter{header: w.Header()}
		authenticated := false
		auth.RequireBearerToken(verifier, options)(http.HandlerFunc(func(_ http.ResponseWriter, authenticatedRequest *http.Request) {
			authenticated = true
			next.ServeHTTP(w, authenticatedRequest)
		})).ServeHTTP(failure, r)
		if authenticated {
			return
		}
		public := &management.Error{Code: "unavailable", Message: "OAuth authentication is unavailable", Status: failure.status}
		switch failure.status {
		case http.StatusUnauthorized:
			public.Code, public.Message = "unauthenticated", "A valid bearer access token is required"
		case http.StatusForbidden:
			public.Code, public.Message = "forbidden", "Required OAuth scope is missing"
		case http.StatusBadRequest:
			public.Code, public.Message = "invalid_request", "OAuth authentication request is invalid"
		}
		writeError(w, public)
	})
}

type authFailureWriter struct {
	header http.Header
	status int
}

func (w *authFailureWriter) Header() http.Header { return w.header }
func (w *authFailureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *authFailureWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(body), nil
}

func writeJSON(w http.ResponseWriter, out any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
func writeError(w http.ResponseWriter, err *management.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(err.Status)
	json.NewEncoder(w).Encode(err)
}
func toolResult(out any, err error) *mcp.CallToolResult {
	if err != nil {
		out = management.PublicError(err)
	}
	raw, _ := json.Marshal(out)
	return &mcp.CallToolResult{IsError: err != nil, StructuredContent: out, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}
