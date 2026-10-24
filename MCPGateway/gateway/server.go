package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const MaxRequestBytes = 6422528

type Server struct {
	handler http.Handler
	signer  jose.Signer
	config  Config
}

func New(c Config, authenticator Authenticator, logger *slog.Logger) (*Server, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if authenticator == nil || logger == nil {
		return nil, errors.New("authenticator and logger required")
	}
	f, err := os.Open(c.SigningKeyFile)
	if err != nil {
		return nil, errors.New("cannot read signing key")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<10 {
		return nil, errors.New("signing JWK must be a private regular file, at most 16 KiB")
	}
	var key jose.JSONWebKey
	if json.NewDecoder(io.LimitReader(f, 16<<10)).Decode(&key) != nil {
		return nil, errors.New("invalid signing JWK")
	}
	private, ok := key.Key.(*rsa.PrivateKey)
	if !ok || private.N.BitLen() < 2048 || key.KeyID == "" || key.Algorithm != "RS256" || key.Use != "sig" || private.Validate() != nil {
		return nil, errors.New("signing JWK requires a private RSA key, kid, use=sig and alg=RS256")
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType(trust.TokenType))
	if err != nil {
		return nil, err
	}
	s := &Server{signer: signer, config: c}
	origin, _ := url.Parse(c.Origin)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, route := range c.Routes {
		resource := c.Origin + route.Path
		metadataPath := "/.well-known/oauth-protected-resource" + route.Path
		mux.HandleFunc("GET "+metadataPath, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"resource":                 resource,
				"authorization_servers":    []string{c.Issuer},
				"scopes_supported":         append(append([]string{}, route.RequiredScopes...), route.ForwardScopes...),
				"bearer_methods_supported": []string{"header"},
			})
		})
		mux.Handle(route.Path, s.newRouteHandler(route, authenticator, logger))
	}
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != origin.Host {
			http.Error(w, "Unexpected Host", 421)
			return
		}
		origins := r.Header.Values("Origin")
		if len(origins) > 1 || len(origins) == 1 && origins[0] != c.Origin {
			http.Error(w, "Unexpected Origin", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return s, nil
}

// newRouteHandler keeps admission, authorization and identity issuance ahead of
// proxy dispatch. Each route has its own capacity limit and upstream transport.
func (s *Server) newRouteHandler(route Route, authenticator Authenticator, logger *slog.Logger) http.Handler {
	resource := s.config.Origin + route.Path
	metadataURL := s.config.Origin + "/.well-known/oauth-protected-resource" + route.Path
	proxy := newRouteProxy(route, logger)
	slots := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != route.Path {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "GET, POST, DELETE")
			http.Error(w, "Method not allowed", 405)
			return
		}
		if r.URL.RawQuery != "" || r.Header.Get("Mcp-Session-Id") != "" {
			http.Error(w, "Queries and stateful MCP sessions are unsupported", 400)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Route capacity reached", 429)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		values := r.Header.Values("Authorization")
		raw := ""
		if len(values) == 1 {
			parts := strings.Fields(values[0])
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				raw = parts[1]
			}
		}
		principal, err := authenticator.Authenticate(ctx, raw, resource)
		if err != nil || !validTokenLifetime(principal, route, time.Now()) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`"`)
			http.Error(w, "Authentication required", 401)
			return
		}
		if !allowed(principal, route) {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", resource_metadata="`+metadataURL+`"`)
			http.Error(w, "Route access denied", 403)
			return
		}
		// Read the bounded body before dispatch: oversize requests must have no effects.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
		if err != nil {
			http.Error(w, "Request too large or unreadable", 413)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		r.ContentLength = int64(len(body))
		token, err := s.issue(principal, route)
		if err != nil {
			http.Error(w, "Identity unavailable", 503)
			return
		}
		start := time.Now()
		proxy.ServeHTTP(proxyResponseWriter{w}, r.WithContext(context.WithValue(ctx, tokenKey{}, token)))
		logger.Info("MCP request", "route", route.Name, "duration", time.Since(start))
	})
}

// newRouteProxy defines the metadata allowed to cross the backend boundary.
func newRouteProxy(route Route, logger *slog.Logger) *httputil.ReverseProxy {
	upstream, _ := url.Parse(route.Upstream)
	transport := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		DialContext:            (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           64,
		MaxIdleConnsPerHost:    16,
		MaxConnsPerHost:        64,
		IdleConnTimeout:        60 * time.Second,
		TLSHandshakeTimeout:    8 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
	}
	return &httputil.ReverseProxy{
		Transport:     backendTransport{transport},
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(logger.Handler(), slog.LevelError),
		Rewrite: func(pr *httputil.ProxyRequest) {
			token := pr.In.Context().Value(tokenKey{}).(string)
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.URL.Path = upstream.Scheme, upstream.Host, upstream.Path
			pr.Out.URL.RawPath, pr.Out.URL.RawQuery, pr.Out.URL.ForceQuery = "", "", false
			pr.Out.Host = upstream.Host
			if route.UpstreamHost != "" {
				pr.Out.Host = route.UpstreamHost
			}
			pr.Out.Header = make(http.Header)
			for _, h := range []string{
				"Content-Type",
				"Accept",
				"Mcp-Protocol-Version",
				"Last-Event-Id",
				"Mcp-Method",
				"Mcp-Name",
			} {
				if v := pr.In.Header.Get(h); v != "" {
					pr.Out.Header.Set(h, v)
				}
			}
			// SEP-2243 reserves Mcp-Param-{Name} for schema-annotated tool
			// arguments. These are untrusted protocol data, never identity.
			// The backend SDK validates them against the tool schema and body.
			for name, values := range pr.In.Header {
				if suffix, ok := strings.CutPrefix(http.CanonicalHeaderKey(name), "Mcp-Param-"); ok && suffix != "" {
					pr.Out.Header[name] = slices.Clone(values)
				}
			}
			pr.Out.Header.Set("Authorization", "Bearer "+token)
			// Trailers bypass Header filtering; only the bounded body is forwarded.
			pr.Out.Trailer = nil
			pr.Out.TransferEncoding = nil
			pr.Out.GetBody = nil
		},
		ModifyResponse: func(resp *http.Response) error {
			// Backends must be stateless. Do not leak internal URLs, cookies or challenges.
			if resp.Header.Get("Mcp-Session-Id") != "" {
				return errors.New("stateful backend unsupported")
			}
			for _, h := range []string{"Set-Cookie", "Location", "WWW-Authenticate", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"} {
				resp.Header.Del(h)
			}
			resp.Header.Del("Trailer")
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				return errors.New("upstream redirect rejected")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Warn("upstream request failed", "route", route.Name)
			http.Error(w, "MCP backend unavailable; execution may be uncertain. Do not replay writes automatically.", http.StatusBadGateway)
		},
	}
}

type tokenKey struct{}

type proxyResponseWriter struct{ http.ResponseWriter }

func (w proxyResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w proxyResponseWriter) WriteHeader(status int) {
	if status >= 200 {
		// ReverseProxy clears the writer's headers after each 1xx response.
		// Restore gateway defaults ahead of final backend values, matching
		// responses without 1xx while preserving those backend values.
		if values := w.Header().Values("Cache-Control"); len(values) == 0 || values[0] != "no-store" {
			w.Header()["Cache-Control"] = append([]string{"no-store"}, values...)
		}
		if values := w.Header().Values("X-Content-Type-Options"); len(values) == 0 || values[0] != "nosniff" {
			w.Header()["X-Content-Type-Options"] = append([]string{"nosniff"}, values...)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

type backendTransport struct{ *http.Transport }

func (t backendTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// ReverseProxy forwards 1xx before ModifyResponse. Install this trace at
	// dispatch, after its forwarding hook: WithClientTrace runs new hooks first.
	// Stateless MCP has no informational metadata; preserve statuses and other
	// instrumentation hooks, but discard headers before either can observe them.
	r = r.WithContext(httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{
		Got1xxResponse: func(_ int, header textproto.MIMEHeader) error {
			clear(header)
			return nil
		},
	}))
	resp, err := t.Transport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	// net/http repopulates the original response's trailers at body EOF. Give
	// ReverseProxy a copy so trailers cannot bypass the response header filter.
	clean := new(*resp)
	clean.Trailer = nil
	return clean, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) issue(p trust.Principal, r Route) (string, error) {
	now := time.Now()
	if !validTokenLifetime(p, r, now) {
		return "", errors.New("access token lifetime invalid before dispatch")
	}
	scopes := []string{}
	for scope := range strings.FieldsSeq(p.Scope) {
		if slices.Contains(r.ForwardScopes, scope) && !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	expires := now.Add(60 * time.Second)
	if p.ExpiresAt.Before(expires) {
		expires = p.ExpiresAt
	}
	if !expires.After(now) {
		return "", errors.New("access token expired before dispatch")
	}
	claims := trust.Claims{
		Claims: jwt.Claims{
			Issuer:    s.config.Origin,
			Subject:   p.Subject,
			Audience:  jwt.Audience{r.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(expires),
			ID:        rand.Text(),
		},
		IdentityIssuer: p.Issuer,
		ClientID:       p.ClientID,
		Scope:          strings.Join(scopes, " "),
	}
	return jwt.Signed(s.signer).Claims(claims).Serialize()
}
