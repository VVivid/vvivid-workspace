package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"agentnewsfeed/internal/store/sqlite"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "__Host-newsfeed-session"
	loginCookie   = "__Host-newsfeed-login"
)

type loginAttempt struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	ReturnTo string `json:"return_to,omitempty"`
}

type sessionState struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Nonce        string `json:"nonce"`
	CSRF         string `json:"csrf"`
	VerifiedAt   int64  `json:"verified_at"`
	AccessExpiry int64  `json:"access_expiry"`
	Subject      string `json:"subject"`
}

func (m *Manager) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", m.login)
	mux.HandleFunc("GET /auth/callback", m.callback)
	mux.HandleFunc("POST /auth/logout", m.logout)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", m.resourceMetadata(m.cfg.MCPAudience))
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/api/v1", m.resourceMetadata(m.cfg.APIAudience))
}

func (m *Manager) login(w http.ResponseWriter, r *http.Request) {
	cookie := rand.Text()
	attempt := loginAttempt{State: rand.Text(), Nonce: rand.Text(), Verifier: oauth2.GenerateVerifier(), ReturnTo: localReturnPath(r.URL.Query().Get("return_to"))}
	hash := sha256.Sum256([]byte(cookie))
	encrypted, err := m.cfg.Keys.seal(attempt, loginPurpose(hash))
	if err != nil || m.store.SaveLoginAttempt(r.Context(), hash, encrypted, m.now().Add(10*time.Minute)) != nil {
		http.Error(w, "Sign-in is temporarily unavailable.", http.StatusServiceUnavailable)
		return
	}
	setCookie(w, loginCookie, cookie, 600)
	location := m.oauth.AuthCodeURL(attempt.State, oidc.Nonce(attempt.Nonce), oauth2.S256ChallengeOption(attempt.Verifier), oauth2.SetAuthURLParam("resource", m.cfg.APIAudience))
	http.Redirect(w, r, location, http.StatusFound)
}

func (m *Manager) callback(w http.ResponseWriter, r *http.Request) {
	setCookie(w, loginCookie, "", -1)
	cookie, err := uniqueCookie(r, loginCookie)
	if err != nil {
		authFailure(w)
		return
	}
	hash := sha256.Sum256([]byte(cookie))
	encrypted, err := m.store.ConsumeLoginAttempt(r.Context(), hash, m.now())
	var attempt loginAttempt
	if err != nil || m.cfg.Keys.open(encrypted, loginPurpose(hash), &attempt) != nil {
		authFailure(w)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 || !constantEqual(query.Get("state"), attempt.State) ||
		len(query["iss"]) > 1 || (query.Get("iss") != "" && query.Get("iss") != m.cfg.Issuer) || (m.metadata.ResponseISS && query.Get("iss") != m.cfg.Issuer) ||
		len(query["code"]) != 1 || query.Get("code") == "" || len(query.Get("code")) > 4096 || query.Get("error") != "" {
		authFailure(w)
		return
	}
	tokens, err := m.oauth.Exchange(oidc.ClientContext(r.Context(), m.client), query.Get("code"), oauth2.VerifierOption(attempt.Verifier), oauth2.SetAuthURLParam("resource", m.cfg.APIAudience))
	if err != nil {
		authFailure(w)
		return
	}
	rawID, _ := tokens.Extra("id_token").(string)
	state, principal, err := m.newSessionState(r.Context(), tokens.AccessToken, tokens.RefreshToken, rawID, tokens.TokenType, attempt.Nonce)
	if err != nil {
		// An unenrolled login creates no session. Revoke its newly issued grant.
		_ = m.revoke(r.Context(), tokens.RefreshToken)
		authFailure(w)
		return
	}
	credential := rand.Text()
	session := sqlite.Session{
		TokenHash: sha256.Sum256([]byte(credential)),
		Owner:     principal.Owner(),
		ClientID:  principal.ClientID(),
		CSRFHash:  sha256.Sum256([]byte(state.CSRF)),
		CreatedAt: m.now(),
		ExpiresAt: m.now().Add(sessionTTL),
	}
	session.EncryptedState, err = m.cfg.Keys.seal(state, sessionPurpose(session))
	if err != nil || m.store.CreateSession(r.Context(), session) != nil {
		_ = m.revoke(r.Context(), state.RefreshToken)
		http.Error(w, "Sign-in could not be saved.", http.StatusServiceUnavailable)
		return
	}
	if previous, err := uniqueCookie(r, sessionCookie); err == nil {
		if err := m.store.RevokeSession(r.Context(), sha256.Sum256([]byte(previous)), m.now()); err != nil {
			_ = m.store.RevokeSession(r.Context(), session.TokenHash, m.now())
			authFailure(w)
			return
		}
	}
	setCookie(w, sessionCookie, credential, int(sessionTTL/time.Second))
	http.Redirect(w, r, localReturnPath(attempt.ReturnTo), http.StatusSeeOther)
}

// Return destinations are stored inside the authenticated login attempt.
// Reject network-path and backslash forms that browsers can interpret as hosts.
func localReturnPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 512 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.Contains(u.Path, "\\") || strings.HasPrefix(u.Path, "//") || strings.ContainsFunc(u.Path, unicode.IsControl) || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return raw
}

func (m *Manager) newSessionState(ctx context.Context, access, refresh, rawID, tokenType, nonce string) (sessionState, Principal, error) {
	if !strings.EqualFold(tokenType, "Bearer") || refresh == "" || len(refresh) > 16<<10 || len(rawID) > 32<<10 {
		return sessionState{}, Principal{}, ErrUnauthenticated
	}
	id, err := m.verifyBrowserID(ctx, rawID, access)
	if err != nil || !constantEqual(id.Nonce, nonce) {
		return sessionState{}, Principal{}, ErrUnauthenticated
	}
	claims, err := m.verifyAccess(ctx, access, m.cfg.APIAudience)
	if err != nil || claims.Subject != id.Subject {
		return sessionState{}, Principal{}, ErrUnauthenticated
	}
	principal, err := m.principal(ctx, claims)
	if err != nil || principal.Require(Read) != nil {
		return sessionState{}, Principal{}, ErrUnauthenticated
	}
	state := sessionState{AccessToken: access, RefreshToken: refresh, IDToken: rawID, Nonce: nonce, CSRF: rand.Text(), VerifiedAt: m.now().Unix(), AccessExpiry: claims.Expiry, Subject: claims.Subject}
	return state, principal, nil
}

func (m *Manager) verifyBrowserID(ctx context.Context, raw, access string) (*oidc.IDToken, error) {
	if len(raw) == 0 || len(raw) > 32<<10 {
		return nil, ErrUnauthenticated
	}
	id, err := m.idVerifier.Verify(ctx, raw)
	if err != nil || id.Issuer != m.cfg.Issuer || len(id.Audience) != 1 || id.Audience[0] != m.cfg.WebClientID || id.IssuedAt.Unix() <= 0 || id.IssuedAt.After(m.now().Add(30*time.Second)) || !id.Expiry.After(m.now()) {
		return nil, ErrUnauthenticated
	}
	var extra struct {
		ClientID  string `json:"azp"`
		NotBefore int64  `json:"nbf"`
	}
	if id.Claims(&extra) != nil || extra.NotBefore > m.now().Unix() || (extra.ClientID != "" && extra.ClientID != m.cfg.WebClientID) || (id.AccessTokenHash != "" && id.VerifyAccessToken(access) != nil) {
		return nil, ErrUnauthenticated
	}
	return id, nil
}

// BrowserSession reads durable state and revalidates the grant at most one
// minute apart. A refresh failure revokes the local session; a Keycloak outage
// cannot extend it using an unverified refresh result.
func (m *Manager) BrowserSession(r *http.Request) (Principal, string, error) {
	_, state, principal, err := m.browserSession(r)
	return principal, state.CSRF, err
}

func (m *Manager) browserSession(r *http.Request) (sqlite.Session, sessionState, Principal, error) {
	credential, err := uniqueCookie(r, sessionCookie)
	if err != nil {
		return sqlite.Session{}, sessionState{}, Principal{}, ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(credential))
	lock := &m.sessionLocks[int(hash[0])%len(m.sessionLocks)]
	lock.Lock()
	defer lock.Unlock()
	session, err := m.store.Session(r.Context(), hash, m.now())
	var state sessionState
	if err != nil || session.ClientID != m.cfg.WebClientID || m.cfg.Keys.open(session.EncryptedState, sessionPurpose(session), &state) != nil {
		return sqlite.Session{}, sessionState{}, Principal{}, ErrUnauthenticated
	}
	if state.VerifiedAt > m.now().Unix()+30 || state.VerifiedAt <= 0 || state.AccessExpiry <= 0 || state.Subject == "" || state.CSRF == "" || sha256.Sum256([]byte(state.CSRF)) != session.CSRFHash {
		return sqlite.Session{}, sessionState{}, Principal{}, ErrUnauthenticated
	}
	if m.now().Unix()-state.VerifiedAt >= int64(refreshEvery/time.Second) || m.now().Unix()+15 >= state.AccessExpiry {
		updated, err := m.refresh(r.Context(), state)
		if err != nil {
			_ = m.store.RevokeSession(r.Context(), hash, m.now())
			return sqlite.Session{}, sessionState{}, Principal{}, ErrUnauthenticated
		}
		state = updated
		encrypted, err := m.cfg.Keys.seal(state, sessionPurpose(session))
		if err != nil || m.store.UpdateSession(r.Context(), hash, encrypted, m.now()) != nil {
			_ = m.store.RevokeSession(r.Context(), hash, m.now())
			return sqlite.Session{}, sessionState{}, Principal{}, ErrUnauthenticated
		}
	}
	principal, err := m.AuthenticateBearer(r.Context(), state.AccessToken, m.cfg.APIAudience)
	if err != nil || principal.Owner().ID() != session.Owner.ID() {
		return sqlite.Session{}, sessionState{}, Principal{}, ErrUnauthenticated
	}
	return session, state, principal, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
}

func (m *Manager) refresh(ctx context.Context, previous sessionState) (sessionState, error) {
	if previous.RefreshToken == "" {
		return sessionState{}, ErrUnauthenticated
	}
	response, err := m.postForm(ctx, m.oauth.Endpoint.TokenURL, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {previous.RefreshToken}, "resource": {m.cfg.APIAudience}})
	if err != nil {
		return sessionState{}, err
	}
	var tokens tokenResponse
	if json.Unmarshal(response, &tokens) != nil || !strings.EqualFold(tokens.TokenType, "Bearer") {
		return sessionState{}, ErrUnauthenticated
	}
	claims, err := m.verifyAccess(ctx, tokens.AccessToken, m.cfg.APIAudience)
	if err != nil {
		return sessionState{}, err
	}
	if previous.Subject != claims.Subject {
		return sessionState{}, ErrUnauthenticated
	}
	// A refresh may omit the ID token or retain the original nonce. It cannot
	// change the authenticated subject, client, or browser permissions.
	if tokens.IDToken != "" {
		id, err := m.verifyBrowserID(ctx, tokens.IDToken, tokens.AccessToken)
		if err != nil || id.Subject != claims.Subject || (id.Nonce != "" && !constantEqual(id.Nonce, previous.Nonce)) {
			return sessionState{}, ErrUnauthenticated
		}
		previous.IDToken = tokens.IDToken
	}
	if scopes := strings.Fields(claims.Scope); !slices.Contains(scopes, Read) {
		return sessionState{}, ErrForbidden
	}
	if _, err := m.principal(ctx, claims); err != nil {
		return sessionState{}, err
	}
	if tokens.RefreshToken != "" {
		previous.RefreshToken = tokens.RefreshToken
	}
	if len(previous.RefreshToken) > 16<<10 || len(previous.IDToken) > 32<<10 {
		return sessionState{}, ErrUnauthenticated
	}
	previous.AccessToken, previous.AccessExpiry, previous.VerifiedAt = tokens.AccessToken, claims.Expiry, m.now().Unix()
	return previous, nil
}

func (m *Manager) logout(w http.ResponseWriter, r *http.Request) {
	session, state, _, err := m.browserSession(r)
	if err != nil {
		setCookie(w, sessionCookie, "", -1)
		authFailure(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil || !m.validCSRF(r, session, r.PostForm.Get("csrf_token")) {
		http.Error(w, "The request could not be verified.", http.StatusForbidden)
		return
	}
	if err := m.store.RevokeSession(r.Context(), session.TokenHash, m.now()); err != nil {
		http.Error(w, "Sign-out could not be saved.", http.StatusServiceUnavailable)
		return
	}
	setCookie(w, sessionCookie, "", -1)
	if err := m.revoke(r.Context(), state.RefreshToken); err != nil {
		http.Error(w, "Signed out here. The identity provider could not confirm revocation.", http.StatusBadGateway)
		return
	}
	location, _ := url.Parse(m.metadata.EndSession)
	query := location.Query()
	query.Set("id_token_hint", state.IDToken)
	query.Set("post_logout_redirect_uri", m.cfg.Origin+"/")
	query.Set("client_id", m.cfg.WebClientID)
	location.RawQuery = query.Encode()
	http.Redirect(w, r, location.String(), http.StatusSeeOther)
}

func (m *Manager) revoke(ctx context.Context, refresh string) error {
	if refresh == "" {
		return ErrUnauthenticated
	}
	_, err := m.postForm(ctx, m.metadata.Revocation, url.Values{"token": {refresh}, "token_type_hint": {"refresh_token"}})
	return err
}

func (m *Manager) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ErrUnauthenticated
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(url.QueryEscape(m.cfg.WebClientID), url.QueryEscape(m.cfg.WebClientSecret))
	response, err := m.client.Do(request)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 || response.StatusCode != http.StatusOK {
		return nil, ErrUnauthenticated
	}
	return body, nil
}

func uniqueCookie(r *http.Request, name string) (string, error) {
	cookies := r.CookiesNamed(name)
	if len(cookies) != 1 || len(cookies[0].Value) < 26 || len(cookies[0].Value) > 128 {
		return "", ErrUnauthenticated
	}
	return cookies[0].Value, nil
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func constantEqual(a, b string) bool    { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func loginPurpose(hash [32]byte) string { return "login:" + hex.EncodeToString(hash[:]) }
func sessionPurpose(s sqlite.Session) string {
	return fmt.Sprintf("session:%x:%s:%s", s.TokenHash, s.Owner.ID(), s.ClientID)
}
func authFailure(w http.ResponseWriter) {
	http.Error(w, "Sign-in could not be verified.", http.StatusUnauthorized)
}
