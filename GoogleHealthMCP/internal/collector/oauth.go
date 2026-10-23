package collector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const authorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
const tokenEndpoint = "https://oauth2.googleapis.com/token"

type ClientCredentials struct {
	ID     string `json:"client_id"`
	Secret string `json:"client_secret"`
}

type Tokens struct {
	Access           string    `json:"access_token"`
	Refresh          string    `json:"refresh_token"`
	Expires          time.Time `json:"expires_at"`
	RefreshExpires   time.Time `json:"refresh_expires_at,omitzero"`
	Scopes           []string  `json:"granted_scopes"`
	HealthUserID     string    `json:"health_user_id,omitempty"`
	Verified         bool      `json:"verified"`
	VerifiedAt       string    `json:"verified_at,omitempty"`
	PublishingStatus string    `json:"publishing_status"`
	Problem          string    `json:"problem,omitempty"`
}

type OAuth struct {
	HTTP        *HTTP
	Credentials ClientCredentials
	TokenDir    string
	Tokens      Tokens
	tokenURL    string
}

func ReadCredentials(path string) (ClientCredentials, error) {
	data, err := readPrivate(path)
	if err != nil {
		return ClientCredentials{}, err
	}
	var file struct {
		Installed *ClientCredentials `json:"installed"`
		Web       *ClientCredentials `json:"web"`
	}
	if json.Unmarshal(data, &file) != nil {
		return ClientCredentials{}, &Error{Code: "invalid_client_file"}
	}
	client := file.Installed
	if client == nil {
		client = file.Web
	}
	if client == nil || client.ID == "" {
		return ClientCredentials{}, &Error{Code: "invalid_client_file"}
	}
	return *client, nil
}

func NewOAuth(clientFile, tokenDir string, h *HTTP) (*OAuth, error) {
	client, err := ReadCredentials(clientFile)
	if err != nil {
		return nil, err
	}
	o := &OAuth{HTTP: h, Credentials: client, TokenDir: tokenDir, tokenURL: tokenEndpoint}
	o.Tokens, err = ReadTokens(tokenDir)
	return o, err
}

func ReadTokens(tokenDir string) (Tokens, error) {
	var tokens Tokens
	if info, err := os.Lstat(tokenDir); err == nil && (!info.IsDir() || info.Mode().Perm()&0077 != 0) {
		return tokens, &Error{Code: "insecure_token_directory"}
	}
	data, err := readPrivate(filepath.Join(tokenDir, "tokens.json"))
	if err != nil {
		if Code(err) == "file_missing" {
			return tokens, nil
		}
		return tokens, err
	}
	if json.Unmarshal(data, &tokens) != nil {
		return tokens, &Error{Code: "invalid_token_store"}
	}
	return tokens, nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &Error{Code: "file_missing"}
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, &Error{Code: "insecure_secret_file"}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, &Error{Code: "secret_read_failed"}
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, &Error{Code: "secret_read_failed"}
	}
	return data, nil
}

func (o *OAuth) Save() error {
	if err := os.MkdirAll(o.TokenDir, 0700); err != nil {
		return &Error{Code: "token_write_failed"}
	}
	info, err := os.Lstat(o.TokenDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return &Error{Code: "insecure_token_directory"}
	}
	data, err := json.Marshal(o.Tokens)
	if err != nil {
		return &Error{Code: "token_write_failed"}
	}
	f, err := os.CreateTemp(o.TokenDir, ".tokens-*")
	if err != nil {
		return &Error{Code: "token_write_failed"}
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return &Error{Code: "token_write_failed"}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return &Error{Code: "token_write_failed"}
	}
	if err := f.Close(); err != nil {
		return &Error{Code: "token_write_failed"}
	}
	if err := os.Rename(f.Name(), filepath.Join(o.TokenDir, "tokens.json")); err != nil {
		return &Error{Code: "token_write_failed"}
	}
	dir, err := os.Open(o.TokenDir)
	if err != nil {
		return &Error{Code: "token_write_failed"}
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return &Error{Code: "token_write_failed"}
	}
	return nil
}

type tokenResponse struct {
	Access         string `json:"access_token"`
	Refresh        string `json:"refresh_token"`
	Expires        int    `json:"expires_in"`
	RefreshExpires int    `json:"refresh_token_expires_in"`
	Scope          string `json:"scope"`
	Type           string `json:"token_type"`
}

func (o *OAuth) exchange(ctx context.Context, values url.Values, initial bool) error {
	values.Set("client_id", o.Credentials.ID)
	if o.Credentials.Secret != "" {
		values.Set("client_secret", o.Credentials.Secret)
	}
	var response tokenResponse
	err := o.HTTP.JSON(ctx, http.MethodPost, o.tokenURL, "", "application/x-www-form-urlencoded", []byte(values.Encode()), false, &response)
	if err != nil {
		if Code(err) == "reauthorization_required" {
			o.Tokens.Verified, o.Tokens.Access, o.Tokens.Problem = false, "", "reauthorization_required"
			if saveErr := o.Save(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if response.Access == "" || response.Expires <= 0 || !strings.EqualFold(response.Type, "Bearer") {
		return &Error{Code: "invalid_token_response"}
	}
	if initial {
		o.Tokens = Tokens{PublishingStatus: o.Tokens.PublishingStatus}
	}
	o.Tokens.Access = response.Access
	o.Tokens.Expires = time.Now().Add(time.Duration(response.Expires) * time.Second)
	if response.Refresh != "" {
		o.Tokens.Refresh = response.Refresh
	}
	if response.RefreshExpires > 0 {
		o.Tokens.RefreshExpires = time.Now().Add(time.Duration(response.RefreshExpires) * time.Second)
	}
	if response.Scope != "" || initial {
		o.Tokens.Scopes = strings.Fields(response.Scope)
	}
	o.Tokens.Problem = ""
	// Persist rotations before making another request; a later failure must not
	// discard the only valid refresh token.
	if err := o.Save(); err != nil {
		return err
	}
	if o.Tokens.Refresh == "" {
		return &Error{Code: "offline_access_missing"}
	}
	if !o.HasAnyScope() {
		return &Error{Code: "missing_consent"}
	}
	return nil
}

func (o *OAuth) HasAnyScope() bool {
	for _, scope := range RequiredScopes {
		if slices.Contains(o.Tokens.Scopes, scope) {
			return true
		}
	}
	return false
}

func (o *OAuth) Access(ctx context.Context, force bool) (string, error) {
	if !force && o.Tokens.Access != "" && time.Until(o.Tokens.Expires) > time.Minute {
		return o.Tokens.Access, nil
	}
	if o.Tokens.Refresh == "" || (!o.Tokens.RefreshExpires.IsZero() && !time.Now().Before(o.Tokens.RefreshExpires)) {
		return "", &Error{Code: "reauthorization_required"}
	}
	if err := o.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {o.Tokens.Refresh}}, false); err != nil {
		return "", err
	}
	return o.Tokens.Access, nil
}

type AuthorizationEvent struct {
	Status string `json:"status"`
	URL    string `json:"authorization_url"`
}

// Authorize binds only loopback. For a headless Linux host the owner forwards
// this port with SSH and opens the emitted URL in their local system browser.
func (o *OAuth) Authorize(ctx context.Context, port string, noBrowser bool, emit func(AuthorizationEvent)) error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return &Error{Code: "loopback_listen_failed"}
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/callback"
	state, verifier := randomURLToken(), randomURLToken()
	challengeBytes := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"client_id": {o.Credentials.ID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {strings.Join(RequiredScopes, " ")}, "access_type": {"offline"}, "prompt": {"consent"},
		"state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challengeBytes[:])}, "code_challenge_method": {"S256"},
	}
	authorizationURL := authorizationEndpoint + "?" + params.Encode()
	type callback struct{ code, problem string }
	completed := make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid authorization state.", http.StatusBadRequest)
			return
		}
		result := callback{code: r.URL.Query().Get("code")}
		if result.code == "" || r.URL.Query().Get("error") != "" {
			result.problem = "authorization_denied"
		}
		select {
		case completed <- result:
		default:
		}
		_, _ = io.WriteString(w, "Authorization received. Return to the terminal for the verified result. You may close this tab.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener) // Server is closed on every return, including cancellation.
	emit(AuthorizationEvent{"awaiting_consent", authorizationURL})
	if !noBrowser {
		if err := openBrowser(authorizationURL); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return &Error{Code: "authorization_timeout"}
	case result := <-completed:
		if result.problem != "" {
			return &Error{Code: result.problem}
		}
		return o.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {result.code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}, true)
	}
}

func randomURLToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read cannot fail in supported Go versions.
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func openBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if err := exec.Command(name, url).Run(); err != nil {
		return &Error{Code: "browser_open_failed_use_no_browser"}
	}
	return nil
}
