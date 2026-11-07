package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/identity"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/mcpserver"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/strictjson"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const MaxRequestBytes = 4 << 20 // Less than MCPGateway's 6,422,528-byte limit.

type Store interface {
	memory.Store
	ResolveAccount(context.Context, account.Identity) (string, bool, error)
	Ready(context.Context) error
}

// NewHandler installs pinned public trust before exposing any route. Canonical
// Host is exactly the gateway route's upstream_host; forwarded headers never
// change this policy or supply an identity. Origin, when present, is exact too.
func NewHandler(c Config, store Store) (http.Handler, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	verifier, err := identity.NewVerifier(c.Identity)
	if err != nil {
		return nil, err
	}
	origin, _ := url.Parse(c.BackendOrigin)
	server := mcpserver.New(store)
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: MaxRequestBytes,
		PropagateRequestCancellation: true,
		// The exact Host check below also covers private DNS names on loopback.
		DisableLocalhostProtection: true,
	})
	requests := make(chan struct{}, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if store.Ready(ctx) != nil {
			http.Error(w, "Storage unavailable.", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != origin.Host {
			http.Error(w, "Unknown backend host.", http.StatusMisdirectedRequest)
			return
		}
		origins := r.Header.Values("Origin")
		if len(origins) > 1 || len(origins) == 1 && origins[0] != c.BackendOrigin {
			http.Error(w, "Origin is not allowed.", http.StatusForbidden)
			return
		}
		if len(r.Header.Values("Authorization")) != 1 {
			http.Error(w, "Unauthorized.", http.StatusUnauthorized)
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, "Unauthorized.", http.StatusUnauthorized)
			return
		}
		principal, err := verifier.Verify(parts[1])
		if err != nil {
			http.Error(w, "Unauthorized.", http.StatusUnauthorized)
			return
		}
		select {
		case requests <- struct{}{}:
			defer func() { <-requests }()
		default:
			http.Error(w, "Service busy.", http.StatusServiceUnavailable)
			return
		}
		id, active, err := store.ResolveAccount(r.Context(), principal.Identity)
		if err != nil {
			http.Error(w, "Storage unavailable.", http.StatusServiceUnavailable)
			return
		}
		if !active {
			http.Error(w, "Account is not active.", http.StatusForbidden)
			return
		}
		principal.Actor.AccountID = id
		if r.Method == http.MethodPost {
			if len(r.Header.Values("Content-Type")) != 1 {
				http.Error(w, "JSON request required.", http.StatusUnsupportedMediaType)
				return
			}
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				http.Error(w, "JSON request required.", http.StatusUnsupportedMediaType)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
			if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
				http.Error(w, "Request too large.", http.StatusRequestEntityTooLarge)
				return
			}
			if err != nil || !validRPC(body) {
				http.Error(w, "Invalid MCP request.", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		ctx := mcpserver.WithRequestContext(identity.WithPrincipal(r.Context(), principal))
		transport.ServeHTTP(w, r.WithContext(ctx))
	}))
	return http.TimeoutHandler(mux, 30*time.Second, "Request timed out.\n"), nil
}

// validRPC keeps SDK protocol errors from echoing untrusted envelope names or
// values, while leaving tool argument semantics to the strict domain handlers.
func validRPC(body []byte) bool {
	var request struct {
		Version string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		ID      json.RawMessage `json:"id"`
		Params  json.RawMessage `json:"params"`
	}
	if strictjson.Decode(body, &request) != nil || request.Version != "2.0" ||
		!slices.Contains([]string{"server/discover", "initialize", "notifications/initialized", "notifications/cancelled", "ping", "tools/list", "tools/call"}, request.Method) {
		return false
	}
	if len(request.ID) != 0 {
		var text string
		if json.Unmarshal(request.ID, &text) != nil || len(text) > 128 {
			if _, err := strconv.ParseInt(string(request.ID), 10, 64); err != nil {
				return false
			}
		}
	}
	if request.Method == "tools/call" {
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		if strictjson.Decode(request.Params, &call) != nil || !slices.Contains([]string{"remember", "retrieve", "list_spaces"}, call.Name) {
			return false
		}
	}
	return true
}

func HTTPServer(c Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: c.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 << 10,
		// net/http's default panic log includes request addresses and source paths.
		// Application logs intentionally contain lifecycle events only.
		ErrorLog: log.New(io.Discard, "", 0),
	}
}
