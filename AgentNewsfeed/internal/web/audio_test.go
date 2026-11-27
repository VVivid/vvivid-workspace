package web_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"agentnewsfeed/internal/audio"
	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
)

const fakeElevenLabsKey = "test-elevenlabs-key"

// fakeElevenLabs answers text-to-speech requests like ElevenLabs: the "MP3" is
// the spoken text itself, and each character starts 50 ms after the previous.
// quotaExceeded makes every request fail the way an empty account does.
type fakeElevenLabs struct {
	mu            sync.Mutex
	texts         []string
	quotaExceeded bool
}

func (f *fakeElevenLabs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("xi-api-key") != fakeElevenLabsKey || r.Method != http.MethodPost ||
		!strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/voice-talia/with-timestamps") || r.URL.Query().Get("output_format") != "mp3_44100_64" {
		http.Error(w, `{"detail":{"status":"invalid_request"}}`, http.StatusBadRequest)
		return
	}
	var in struct {
		Text    string `json:"text"`
		ModelID string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ModelID != "eleven_multilingual_v2" {
		http.Error(w, `{"detail":{"status":"invalid_request"}}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.texts = append(f.texts, in.Text)
	quota := f.quotaExceeded
	f.mu.Unlock()
	if quota {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":{"status":"quota_exceeded","message":"no credits"}}`)
		return
	}
	var chars []string
	var starts, ends []float64
	for i, r := range []rune(in.Text) {
		chars = append(chars, string(r))
		starts = append(starts, float64(i)*0.05)
		ends = append(ends, float64(i+1)*0.05)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"audio_base64": base64.StdEncoding.EncodeToString([]byte("MP3:" + in.Text)),
		"alignment":    map[string]any{"characters": chars, "character_start_times_seconds": starts, "character_end_times_seconds": ends},
	})
}

func (f *fakeElevenLabs) spoken() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

// audioApp is nativeApp with audio enabled against a fake ElevenLabs, and the
// generator running until the test ends.
func audioApp(t *testing.T) (*testfixture.Identity, *feed.Service, http.Handler, *fakeElevenLabs) {
	t.Helper()
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	tts := &fakeElevenLabs{}
	server := httptest.NewServer(tts)
	t.Cleanup(server.Close)
	engine, err := audio.New(audio.Config{
		APIKey: fakeElevenLabsKey, Model: "eleven_multilingual_v2", OutputFormat: "mp3_44100_64", Dir: t.TempDir(), BaseURL: server.URL,
		Voices: []audio.Voice{{ID: "talia", Name: "Talia", Description: "Calm and clear", ElevenLabsID: "voice-talia"}},
	}, f.Store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.EnableAudio(engine)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { engine.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return f, s, h, tts
}

// send makes one native app request with the bearer token and an optional JSON body.
func send(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, testfixture.Origin+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}

// waitForAudio polls the audio route until its status is want.
func waitForAudio(t *testing.T, h http.Handler, path, token, want string) feed.AudioStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		response := send(h, http.MethodGet, path, token, "")
		var status feed.AudioStatus
		if response.Code == 200 && json.Unmarshal(response.Body.Bytes(), &status) == nil && status.Status == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("audio never became %s: HTTP %d %s", want, response.Code, response.Body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNativeAppCreatesPlaysAndDeletesArticleAudio(t *testing.T) {
	f, s, h, tts := audioApp(t)
	alice := f.Principal(t, "alice", allScopes)
	added, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "audio-source", Article: feed.Content{
		Title: "Payments", Summary: "How payments settle.", ContentMarkdown: "# Payments\n\nA wire arrives.", Tags: []string{"generated"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	id, revision := added.Article.ID, added.Article.Revision
	token := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), allScopes)
	readOnly := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), identity.Read)
	bob := f.Bearer("bob", nativeClient, f.Manager.APIAudience(), allScopes)
	path := "/api/v1/articles/" + id + "/audio"

	voices := send(h, http.MethodGet, "/api/v1/audio/voices", readOnly, "")
	if voices.Code != 200 || strings.TrimSpace(voices.Body.String()) != `{"voices":[{"id":"talia","name":"Talia","description":"Calm and clear"}]}` {
		t.Fatalf("voices: HTTP %d %s", voices.Code, voices.Body)
	}
	if sample := send(h, http.MethodGet, "/api/v1/audio/voices/talia/sample", readOnly, ""); sample.Code != 200 ||
		sample.Header().Get("Content-Type") != "audio/mpeg" || !strings.HasPrefix(sample.Body.String(), "MP3:Hi, I'm Talia.") {
		t.Fatalf("sample: HTTP %d %s", sample.Code, sample.Body)
	}
	requireNativeAppError(t, send(h, http.MethodGet, path, token, ""), http.StatusNotFound)

	create := `{"voice":"talia","expected_revision":` + jsonInt(revision) + `,"segments":[{"block":-1,"text":"Payments. How payments settle."},{"block":0,"text":"A wire arrives — café."}]}`
	// The bearer middleware refuses a missing scope before the route runs.
	if refused := send(h, http.MethodPost, path, readOnly, create); refused.Code != http.StatusForbidden {
		t.Fatalf("create without write scope: HTTP %d", refused.Code)
	}
	requireNativeAppError(t, send(h, http.MethodPost, path, bob, create), http.StatusNotFound)
	requireNativeAppError(t, send(h, http.MethodPost, path, token, strings.Replace(create, `"talia"`, `"nobody"`, 1)), http.StatusBadRequest)
	requireNativeAppError(t, send(h, http.MethodPost, path, token, strings.Replace(create, `"expected_revision":`+jsonInt(revision), `"expected_revision":99`, 1)), http.StatusConflict)
	started := send(h, http.MethodPost, path, token, create)
	if started.Code != http.StatusAccepted {
		t.Fatalf("create: HTTP %d %s", started.Code, started.Body)
	}
	requireNativeAppError(t, send(h, http.MethodPost, path, token, create), http.StatusConflict)

	ready := waitForAudio(t, h, path, readOnly, sqlite.AudioReady)
	if ready.Voice.Name != "Talia" || ready.SegmentsTotal != 2 || ready.SegmentsDone != 2 || len(ready.Segments) != 2 ||
		ready.Characters != utf8.RuneCountInString("Payments. How payments settle.A wire arrives — café.") {
		t.Fatalf("ready audio: %+v", ready)
	}
	second := ready.Segments[1]
	if second.Block != 0 || len(second.CharStartsMS) != utf8.RuneCountInString(second.Text) || second.CharStartsMS[2] != 100 || second.DurationMS != int64(len(second.CharStartsMS))*50 {
		t.Fatalf("segment timing: %+v", second)
	}
	if spoken := tts.spoken(); len(spoken) != 3 || spoken[2] != "A wire arrives — café." {
		t.Fatalf("spoken texts: %q", spoken)
	}
	clip := send(h, http.MethodGet, path+"/segments/1", readOnly, "")
	if clip.Code != 200 || clip.Header().Get("Content-Type") != "audio/mpeg" || clip.Body.String() != "MP3:A wire arrives — café." {
		t.Fatalf("segment: HTTP %d %s", clip.Code, clip.Body)
	}
	requireNativeAppError(t, send(h, http.MethodGet, path+"/segments/2", readOnly, ""), http.StatusNotFound)
	requireNativeAppError(t, send(h, http.MethodGet, path, bob, ""), http.StatusNotFound)

	// The text is locked while the audio exists; archiving still works.
	title := "New title"
	_, err = s.PatchArticle(t.Context(), alice, feed.PatchInput{IdempotencyKey: "locked-patch", ArticleID: id, ExpectedRevision: revision, Patch: feed.Patch{Title: &title}})
	if err == nil || feed.SafeError(err) != "This article has audio, so its text is locked. Ask the user to delete the audio in the app first." {
		t.Fatalf("patch of locked article: %v", err)
	}
	archived := changeState(t, h, "/api/v1/articles/"+id+"/archive", token, revision, "archive-with-audio")

	// Deleting the audio unlocks the text.
	if deleted := send(h, http.MethodDelete, path, token, ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: HTTP %d %s", deleted.Code, deleted.Body)
	}
	requireNativeAppError(t, send(h, http.MethodGet, path, readOnly, ""), http.StatusNotFound)
	if deleted := send(h, http.MethodDelete, path, token, ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("second delete: HTTP %d", deleted.Code)
	}
	patched, err := s.PatchArticle(t.Context(), alice, feed.PatchInput{IdempotencyKey: "unlocked-patch", ArticleID: id, ExpectedRevision: archived.Revision, Patch: feed.Patch{Title: &title}})
	if err != nil {
		t.Fatalf("patch after deleting audio: %v", err)
	}

	// Moving to Trash deletes new audio too.
	create = strings.Replace(create, `"expected_revision":`+jsonInt(revision), `"expected_revision":`+jsonInt(patched.Article.Revision), 1)
	if again := send(h, http.MethodPost, path, token, create); again.Code != http.StatusAccepted {
		t.Fatalf("create again: HTTP %d %s", again.Code, again.Body)
	}
	waitForAudio(t, h, path, readOnly, sqlite.AudioReady)
	changeState(t, h, "/api/v1/articles/"+id+"/trash", token, patched.Article.Revision, "trash-with-audio")
	requireNativeAppError(t, send(h, http.MethodGet, path, readOnly, ""), http.StatusNotFound)
	requireNativeAppError(t, send(h, http.MethodPost, path, token, create), http.StatusNotFound)
}

func TestArticleAudioFailureIsReadableAndReplaceable(t *testing.T) {
	f, s, h, tts := audioApp(t)
	added, err := s.AddArticle(t.Context(), f.Principal(t, "alice", allScopes), feed.AddInput{IdempotencyKey: "quota", Article: feed.Content{
		Title: "Short", Summary: "A short one.", ContentMarkdown: "Words.", Tags: []string{"generated"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	token := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), allScopes)
	path := "/api/v1/articles/" + added.Article.ID + "/audio"
	create := `{"voice":"talia","expected_revision":1,"segments":[{"block":0,"text":"Words."}]}`
	tts.mu.Lock()
	tts.quotaExceeded = true
	tts.mu.Unlock()
	if started := send(h, http.MethodPost, path, token, create); started.Code != http.StatusAccepted {
		t.Fatalf("create: HTTP %d %s", started.Code, started.Body)
	}
	failed := waitForAudio(t, h, path, token, sqlite.AudioFailed)
	if failed.Error != "Your ElevenLabs credits ran out. Add credits, then try again." || failed.Segments != nil {
		t.Fatalf("failed audio: %+v", failed)
	}
	tts.mu.Lock()
	tts.quotaExceeded = false
	tts.mu.Unlock()
	if retried := send(h, http.MethodPost, path, token, create); retried.Code != http.StatusAccepted {
		t.Fatalf("retry: HTTP %d %s", retried.Code, retried.Body)
	}
	waitForAudio(t, h, path, token, sqlite.AudioReady)
}

func TestAudioRoutesWithoutElevenLabsKeyOrInBrowser(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	added, err := s.AddArticle(t.Context(), f.Principal(t, "alice", allScopes), feed.AddInput{IdempotencyKey: "no-audio", Article: feed.Content{
		Title: "Plain", Summary: "No audio here.", ContentMarkdown: "Text.", Tags: []string{"generated"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	token := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), allScopes)
	path := "/api/v1/articles/" + added.Article.ID + "/audio"
	requireNativeAppError(t, send(h, http.MethodGet, "/api/v1/audio/voices", token, ""), http.StatusServiceUnavailable)
	requireNativeAppError(t, send(h, http.MethodGet, path, token, ""), http.StatusServiceUnavailable)
	requireNativeAppError(t, send(h, http.MethodPost, path, token, `{"voice":"talia","expected_revision":1,"segments":[{"block":0,"text":"Text."}]}`), http.StatusServiceUnavailable)

	// A browser cannot create audio, even with a valid form.
	r := httptest.NewRequest(http.MethodPost, testfixture.Origin+path, strings.NewReader("csrf=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(f.CookieWithScopes(t, "alice", allScopes))
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code == http.StatusAccepted {
		t.Fatalf("browser created audio")
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
