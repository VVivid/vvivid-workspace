package identity

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type loginState struct{ State, Nonce, Verifier, Host string }
type sessionState struct {
	Access, Refresh, Subject, Owner, CSRF, Nonce string
	Expiry                                       int64
	Pending                                      *pendingGrant `json:",omitempty"`
}
type pendingGrant struct {
	Access, ID string
	ReceivedAt int64
}
type tokenResponse struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	ID      string `json:"id_token"`
	Type    string `json:"token_type"`
}

func (m *Manager) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", m.login)
	mux.HandleFunc("GET /auth/callback", m.callback)
	mux.HandleFunc("POST /auth/logout", m.logout)
	mux.HandleFunc("GET /api/v1/session", m.session)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/api/v1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"resource": m.cfg.APIAudience, "authorization_servers": []string{m.cfg.Issuer}, "scopes_supported": []string{store.Read, store.Write}, "bearer_methods_supported": []string{"header"}})
	})
	if m.cfg.MCPMode == "direct" {
		mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"resource": m.cfg.MCPResource, "authorization_servers": []string{m.cfg.Issuer}, "scopes_supported": []string{store.Read, store.Write}, "bearer_methods_supported": []string{"header"}})
		})
	}
}

func (m *Manager) origin(host string) string {
	for _, raw := range m.cfg.Origins {
		u, _ := url.Parse(raw)
		if u.Host == host {
			return raw
		}
	}
	return ""
}

func (m *Manager) login(w http.ResponseWriter, r *http.Request) {
	state := loginState{State: rand.Text(), Nonce: rand.Text(), Verifier: oauth2.GenerateVerifier(), Host: r.Host}
	cookie := rand.Text()
	h := hash(cookie)
	encrypted, err := m.cfg.Keys.seal(state, purpose("login", r.Host, h))
	if err != nil || m.store.SaveLogin(r.Context(), h, r.Host, encrypted, m.now().Add(10*time.Minute)) != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", 503)
		return
	}
	setCookie(w, loginCookie, cookie, 600)
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {m.cfg.WebClientID},
		"redirect_uri":          {m.origin(r.Host) + "/auth/callback"},
		"scope":                 {"openid offline_access " + store.Read + " " + store.Write},
		"state":                 {state.State},
		"nonce":                 {state.Nonce},
		"code_challenge":        {oauth2.S256ChallengeFromVerifier(state.Verifier)},
		"code_challenge_method": {"S256"},
		"resource":              {m.cfg.APIAudience},
	}
	if r.URL.Query().Get("reauthenticate") == "1" {
		q.Set("prompt", "login")
	}
	http.Redirect(w, r, m.metadata.Auth+"?"+q.Encode(), http.StatusFound)
}

func (m *Manager) callback(w http.ResponseWriter, r *http.Request) {
	setCookie(w, loginCookie, "", -1)
	credential, err := cookieValue(r, loginCookie)
	if err != nil {
		loginError(w, err)
		return
	}
	h := hash(credential)
	raw, err := m.store.ConsumeLogin(r.Context(), h, r.Host, m.now())
	var attempt loginState
	if err != nil || m.cfg.Keys.open(raw, purpose("login", r.Host, h), &attempt) != nil || attempt.Host != r.Host {
		loginError(w, ErrUnauthenticated)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil ||
		len(q["state"]) != 1 || !equal(q.Get("state"), attempt.State) ||
		len(q["code"]) != 1 || q.Get("code") == "" || len(q.Get("code")) > 4096 ||
		len(q["iss"]) > 1 ||
		(q.Get("iss") != "" && q.Get("iss") != m.cfg.Issuer) ||
		(m.metadata.ResponseISS && q.Get("iss") != m.cfg.Issuer) ||
		q.Get("error") != "" {
		loginError(w, ErrUnauthenticated)
		return
	}
	tokens, err := m.tokens(r.Context(), url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {q.Get("code")},
		"redirect_uri":  {m.origin(r.Host) + "/auth/callback"},
		"code_verifier": {attempt.Verifier},
		"resource":      {m.cfg.APIAudience},
	})
	if err != nil {
		loginError(w, err)
		return
	}
	claims, err := m.verifyAccess(r.Context(), tokens.Access, m.cfg.APIAudience)
	if err != nil {
		_ = m.revoke(r.Context(), tokens.Refresh)
		loginError(w, err)
		return
	}
	principal, err := m.principal(r.Context(), claims)
	if err != nil ||
		principal.Actor.Require(store.Read) != nil ||
		!slices.Contains(strings.Fields(claims.Scope), "offline_access") {
		_ = m.revoke(r.Context(), tokens.Refresh)
		loginError(w, ErrUnauthenticated)
		return
	}
	if err = m.verifyID(r.Context(), tokens.ID, tokens.Access, claims.Subject, attempt.Nonce, true, m.now()); err != nil {
		_ = m.revoke(r.Context(), tokens.Refresh)
		loginError(w, err)
		return
	}
	state := sessionState{
		Access:  tokens.Access,
		Refresh: tokens.Refresh,
		Subject: claims.Subject,
		Owner:   principal.Actor.Owner,
		CSRF:    rand.Text(),
		Nonce:   attempt.Nonce,
		Expiry:  claims.Expiry,
	}
	secret := rand.Text()
	h = hash(secret)
	encrypted, err := m.cfg.Keys.seal(state, purpose("session", r.Host, h))
	if err != nil {
		_ = m.revoke(r.Context(), state.Refresh)
		loginError(w, ErrUnavailable)
		return
	}
	session := store.Session{Hash: h, Owner: state.Owner, Host: r.Host, Cipher: encrypted, ExpiresAt: m.now().Add(IdleLifetime)}
	if err = m.store.SaveSession(r.Context(), session); err != nil {
		_ = m.revoke(r.Context(), state.Refresh)
		loginError(w, ErrUnavailable)
		return
	}
	if prior, err := cookieValue(r, sessionCookie); err == nil {
		m.destroyPrior(r.Context(), hash(prior), r.Host)
	}
	setCookie(w, sessionCookie, secret, int(IdleLifetime/time.Second))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (m *Manager) verifyID(ctx context.Context, raw, access, subject, nonce string, initial bool, at time.Time) error {
	if raw == "" && !initial {
		return nil
	}
	if raw == "" || len(raw) > 32<<10 {
		return ErrUnauthenticated
	}
	check := &signatureCheck{keys: m.keys}
	verifier := oidc.NewVerifier(m.cfg.Issuer, check, &oidc.Config{
		ClientID: m.cfg.WebClientID, SupportedSigningAlgs: []string{oidc.RS256},
		Now: func() time.Time { return at },
	})
	id, err := verifier.Verify(ctx, raw)
	if err != nil {
		if signatureUnavailable(check.err) {
			return ErrUnavailable
		}
		return ErrUnauthenticated
	}
	// Initial login requires its nonce. A refresh ID token may omit the nonce,
	// but a supplied nonce must still match the original login attempt.
	if id.Issuer != m.cfg.Issuer || id.Subject != subject ||
		len(id.Audience) != 1 || id.Audience[0] != m.cfg.WebClientID ||
		id.IssuedAt.Unix() <= 0 || id.IssuedAt.After(at.Add(30*time.Second)) ||
		!id.Expiry.After(at) ||
		(initial && !equal(id.Nonce, nonce)) ||
		(!initial && id.Nonce != "" && !equal(id.Nonce, nonce)) ||
		(id.AccessTokenHash != "" && id.VerifyAccessToken(access) != nil) {
		return ErrUnauthenticated
	}
	var extra struct {
		Client    string `json:"azp"`
		NotBefore int64  `json:"nbf"`
	}
	if id.Claims(&extra) != nil || extra.NotBefore > at.Unix() || (extra.Client != "" && extra.Client != m.cfg.WebClientID) {
		return ErrUnauthenticated
	}
	return nil
}

func cookieValue(r *http.Request, name string) (string, error) {
	v := r.CookiesNamed(name)
	if len(v) != 1 || len(v[0].Value) < 26 || len(v[0].Value) > 128 {
		return "", ErrUnauthenticated
	}
	return v[0].Value, nil
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (m *Manager) readSession(ctx context.Context, h []byte, host string) (store.Session, sessionState, error) {
	session, err := m.store.Session(ctx, h, host, m.now())
	var state sessionState
	if err != nil ||
		m.cfg.Keys.open(session.Cipher, purpose("session", host, h), &state) != nil ||
		state.Owner != session.Owner || !identifier(state.Subject) ||
		state.CSRF == "" || state.Refresh == "" || state.Expiry <= 0 ||
		(state.Pending != nil && (state.Pending.Access == "" || state.Pending.ReceivedAt <= 0)) {
		return session, state, ErrUnauthenticated
	}
	return session, state, nil
}

// A single application replica serializes rotating refresh grants per session.
// Retaining the last durable grant after provider network/5xx errors permits a
// later retry. Only invalid_grant (or invalid verified claims) ends the session.
func (m *Manager) browser(w http.ResponseWriter, r *http.Request) (Principal, string, error) {
	credential, err := cookieValue(r, sessionCookie)
	if err != nil {
		return Principal{}, "", err
	}
	h := hash(credential)
	lock := &m.locks[int(h[0])%len(m.locks)]
	lock.Lock()
	defer lock.Unlock()
	session, state, err := m.readSession(r.Context(), h, r.Host)
	if err != nil {
		return Principal{}, "", err
	}
	if state.Pending != nil {
		if err = m.verifyAndPromotePendingGrant(r.Context(), &state); err != nil {
			return Principal{}, "", m.retainOrRevokePendingGrant(r.Context(), h, state, err)
		}
	}
	if state.Expiry <= m.now().Unix()+60 {
		tokens, refreshErr := m.tokens(r.Context(), url.Values{"grant_type": {"refresh_token"}, "refresh_token": {state.Refresh}, "resource": {m.cfg.APIAudience}})
		if refreshErr != nil {
			if errors.Is(refreshErr, ErrUnauthenticated) {
				_ = m.store.DeleteSession(r.Context(), h)
				return Principal{}, "", ErrUnauthenticated
			}
			// A transient refresh failure can use the existing access token only
			// until it expires. Keep the durable refresh grant for a later retry.
			if state.Expiry <= m.now().Unix() {
				return Principal{}, "", ErrUnavailable
			}
		} else {
			// The provider has consumed the previous rotating grant. Persist its
			// replacement before JWKS verification, even if that verification must
			// wait for recovery or another process. Pending tokens never authorize.
			state.Refresh = tokens.Refresh
			state.Pending = &pendingGrant{Access: tokens.Access, ID: tokens.ID, ReceivedAt: m.now().Unix()}
			persistCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 8*time.Second)
			err = m.saveSession(persistCtx, session, state)
			cancel()
			if err != nil {
				_ = m.revoke(r.Context(), state.Refresh)
				_ = m.store.DeleteSession(r.Context(), h)
				return Principal{}, "", ErrUnavailable
			}
			if err = m.verifyAndPromotePendingGrant(r.Context(), &state); err != nil {
				return Principal{}, "", m.retainOrRevokePendingGrant(r.Context(), h, state, err)
			}
		}
	}
	principal, err := m.Authenticate(r.Context(), state.Access, false)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return Principal{}, "", err
		}
		return Principal{}, "", ErrUnauthenticated
	}
	if principal.Actor.Owner != state.Owner {
		return Principal{}, "", ErrUnauthenticated
	}
	session.ExpiresAt = m.now().Add(IdleLifetime)
	if err = m.saveSession(r.Context(), session, state); err != nil {
		// The earlier durable state still has the current refresh grant. A
		// failed idle-extension/pending-promotion write must not destroy it.
		return Principal{}, "", ErrUnavailable
	}
	setCookie(w, sessionCookie, credential, int(IdleLifetime/time.Second))
	return principal, state.CSRF, nil
}

func (m *Manager) saveSession(ctx context.Context, session store.Session, state sessionState) error {
	encrypted, err := m.cfg.Keys.seal(state, purpose("session", session.Host, session.Hash))
	if err != nil {
		return err
	}
	session.Cipher = encrypted
	return m.store.SaveSession(ctx, session)
}

// Promotion changes only the in-memory state. browser() remains responsible for
// authenticating a currently valid access token and saving the session.
func (m *Manager) verifyAndPromotePendingGrant(ctx context.Context, state *sessionState) error {
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	pending := state.Pending
	// Claims must have been valid when this trusted provider response arrived.
	// A long JWKS outage may outlive them; after verification browser() refreshes
	// the durable replacement grant before accepting any expired access token.
	at := time.Unix(pending.ReceivedAt, 0)
	claims, err := m.verifyAccessAt(ctx, pending.Access, m.cfg.APIAudience, at)
	if err != nil {
		return err
	}
	if claims.Subject != state.Subject || !slices.Contains(strings.Fields(claims.Scope), "offline_access") {
		return ErrUnauthenticated
	}
	if err = m.verifyID(ctx, pending.ID, pending.Access, state.Subject, state.Nonce, false, at); err != nil {
		return err
	}
	principal, err := m.principal(ctx, claims)
	if err != nil || principal.Actor.Owner != state.Owner {
		return ErrUnauthenticated
	}
	state.Access, state.Expiry = pending.Access, claims.Expiry
	state.Pending = nil
	return nil
}

func (m *Manager) retainOrRevokePendingGrant(ctx context.Context, h []byte, state sessionState, err error) error {
	if errors.Is(err, ErrUnavailable) {
		return ErrUnavailable
	}
	_ = m.revoke(ctx, state.Refresh)
	_ = m.store.DeleteSession(ctx, h)
	return ErrUnauthenticated
}

func (m *Manager) session(w http.ResponseWriter, r *http.Request) {
	p, csrf, err := m.browser(w, r)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"owner_id": p.Actor.Owner, "label": p.Label, "csrf_token": csrf, "scopes": p.Actor.Scopes})
}

func (m *Manager) logout(w http.ResponseWriter, r *http.Request) {
	credential, err := cookieValue(r, sessionCookie)
	if err != nil {
		setCookie(w, sessionCookie, "", -1)
		authError(w, err)
		return
	}
	h := hash(credential)
	lock := &m.locks[int(h[0])%len(m.locks)]
	lock.Lock()
	defer lock.Unlock()
	_, state, err := m.readSession(r.Context(), h, r.Host)
	if err != nil {
		setCookie(w, sessionCookie, "", -1)
		authError(w, err)
		return
	}
	if r.Header.Get("Origin") != m.origin(r.Host) || !equal(r.Header.Get("X-CSRF-Token"), state.CSRF) {
		http.Error(w, "Request could not be verified.", 403)
		return
	}
	if err = m.store.DeleteSession(r.Context(), h); err != nil {
		authError(w, ErrUnavailable)
		return
	}
	setCookie(w, sessionCookie, "", -1)
	if err = m.revoke(r.Context(), state.Refresh); err != nil {
		writeJSON(w, 502, map[string]string{"error": "Signed out here; provider revocation could not be confirmed."})
		return
	}
	writeJSON(w, 200, map[string]bool{"signed_out": true})
}

func (m *Manager) destroyPrior(ctx context.Context, h []byte, host string) {
	lock := &m.locks[int(h[0])%len(m.locks)]
	lock.Lock()
	defer lock.Unlock()
	_, state, err := m.readSession(ctx, h, host)
	if err == nil {
		_ = m.store.DeleteSession(ctx, h)
		_ = m.revoke(ctx, state.Refresh)
	}
}

func (m *Manager) tokens(ctx context.Context, form url.Values) (tokenResponse, error) {
	raw, err := m.post(ctx, m.metadata.Token, form)
	if err != nil {
		return tokenResponse{}, err
	}
	var tokens tokenResponse
	if json.Unmarshal(raw, &tokens) != nil ||
		!strings.EqualFold(tokens.Type, "Bearer") ||
		tokens.Access == "" || len(tokens.Access) > 32<<10 ||
		tokens.Refresh == "" || len(tokens.Refresh) > 32<<10 {
		return tokens, ErrUnauthenticated
	}
	return tokens, nil
}

func (m *Manager) revoke(ctx context.Context, refresh string) error {
	if refresh == "" {
		return ErrUnauthenticated
	}
	_, err := m.post(ctx, m.metadata.Revoke, url.Values{"token": {refresh}, "token_type_hint": {"refresh_token"}})
	return err
}

func (m *Manager) post(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(m.cfg.WebClientID), url.QueryEscape(m.cfg.WebClientSecret))
	res, err := m.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, ErrUnavailable
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Error == "invalid_grant" {
			return nil, ErrUnauthenticated
		}
		return nil, ErrUnavailable
	}
	return raw, nil
}

func authError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnavailable) {
		http.Error(w, "Sign-in is temporarily unavailable. Please retry.", 503)
	} else {
		setCookie(w, sessionCookie, "", -1)
		http.Error(w, "Sign in to continue.", 401)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// A failed login attempt must not clear an unrelated, already valid session.
func loginError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnavailable) {
		http.Error(w, "Sign-in is temporarily unavailable. Please retry.", 503)
		return
	}
	http.Error(w, "Sign-in could not be verified.", 401)
}
