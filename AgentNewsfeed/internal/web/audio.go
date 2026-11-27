package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
)

// maxAudioCreateBody fits the largest request feed.CreateArticleAudio accepts,
// with room for JSON escaping.
const maxAudioCreateBody = 3 << 20

// registerAudio adds the article audio routes. Reads serve the browser and
// native apps; creating and deleting audio is for native apps, which play it.
func (h *handler) registerAudio(mux *http.ServeMux) {
	read := []string{identity.Read}
	write := []string{identity.Read, identity.ArticlesWrite}
	nativeOnly := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONError(w, http.StatusNotFound, "Audio is created in the Android app.")
	})
	mux.Handle("GET /api/v1/audio/voices", h.auth.BrowserOrNativeAppMiddleware(read, http.HandlerFunc(h.audioVoices)))
	mux.Handle("GET /api/v1/audio/voices/{voice}/sample", h.auth.BrowserOrNativeAppMiddleware(read, http.HandlerFunc(h.audioSample)))
	mux.Handle("GET /api/v1/articles/{id}/audio", h.auth.BrowserOrNativeAppMiddleware(read, http.HandlerFunc(h.articleAudio)))
	mux.Handle("GET /api/v1/articles/{id}/audio/segments/{index}", h.auth.BrowserOrNativeAppMiddleware(read, http.HandlerFunc(h.articleAudioSegment)))
	mux.Handle("POST /api/v1/articles/{id}/audio", h.auth.BrowserFormOrNativeAppMiddleware(write, nativeOnly, http.HandlerFunc(h.createArticleAudio)))
	mux.Handle("DELETE /api/v1/articles/{id}/audio", h.auth.BrowserFormOrNativeAppMiddleware(write, nativeOnly, http.HandlerFunc(h.deleteArticleAudio)))
}

func (h *handler) audioVoices(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	out, err := h.service.ListAudioVoices(r.Context(), p)
	writeAudioJSON(w, http.StatusOK, out, err)
}

func (h *handler) audioSample(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	sample, err := h.service.AudioVoiceSample(r.Context(), p, r.PathValue("voice"))
	writeMP3(w, sample, err)
}

func (h *handler) articleAudio(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	out, err := h.service.ArticleAudio(r.Context(), p, r.PathValue("id"))
	writeAudioJSON(w, http.StatusOK, out, err)
}

func (h *handler) articleAudioSegment(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "Audio segment not found.")
		return
	}
	segment, err := h.service.ArticleAudioSegment(r.Context(), p, r.PathValue("id"), index)
	writeMP3(w, segment, err)
}

func (h *handler) createArticleAudio(w http.ResponseWriter, r *http.Request) {
	var in feed.CreateAudioInput
	if err := readJSONBody(w, r, maxAudioCreateBody, &in); err != nil {
		writeAudioJSON(w, 0, nil, err)
		return
	}
	p, _ := identity.FromContext(r.Context())
	out, err := h.service.CreateArticleAudio(r.Context(), p, r.PathValue("id"), in)
	writeAudioJSON(w, http.StatusAccepted, out, err)
}

func (h *handler) deleteArticleAudio(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	if err := h.service.DeleteArticleAudio(r.Context(), p, r.PathValue("id")); err != nil {
		writeAudioJSON(w, 0, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAudioJSON writes out with status, or err as {"error": message}.
func writeAudioJSON(w http.ResponseWriter, status int, out any, err error) {
	if err != nil {
		writeJSONError(w, errorStatus(err), feed.SafeError(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{message})
}

// writeMP3 serves audio, uncached like every response (see New): deleted and
// new audio reuse the same addresses.
func writeMP3(w http.ResponseWriter, body []byte, err error) {
	if err != nil {
		writeAudioJSON(w, 0, nil, err)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
