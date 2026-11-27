package audio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"unicode/utf8"
)

// DefaultBaseURL is ElevenLabs' API.
const DefaultBaseURL = "https://api.elevenlabs.io"

// maxResponseBytes bounds one response: a 4500-byte segment is about five
// minutes of speech, a few megabytes of MP3 once base64-encoded.
const maxResponseBytes = 32 << 20

// maxContextBytes bounds the neighbouring text sent for natural flow between
// segments; ElevenLabs only needs a little of it.
const maxContextBytes = 1000

// errPermanent marks a failure that retrying cannot fix. Its message is safe to
// show the owner.
type errPermanent struct{ message string }

func (e errPermanent) Error() string { return e.message }

// speech is one synthesized segment: MP3 bytes and when each code point of the
// text starts.
type speech struct {
	audio        []byte
	durationMS   int64
	charStartsMS []int64
}

// elevenLabs calls the text-to-speech API with timestamps.
type elevenLabs struct {
	client       *http.Client
	baseURL      string
	apiKey       string
	model        string
	outputFormat string
}

type voiceSettings struct {
	Stability       float64 `json:"stability"`
	SimilarityBoost float64 `json:"similarity_boost"`
	Style           float64 `json:"style"`
	UseSpeakerBoost bool    `json:"use_speaker_boost"`
}

// narration suits long technical reading: steady, close to the voice, little
// added drama. These are the settings the owner listened to and approved.
var narration = voiceSettings{Stability: 0.55, SimilarityBoost: 0.75, Style: 0.05, UseSpeakerBoost: true}

type speechRequest struct {
	Text          string        `json:"text"`
	ModelID       string        `json:"model_id"`
	VoiceSettings voiceSettings `json:"voice_settings"`
	PreviousText  string        `json:"previous_text,omitempty"`
	NextText      string        `json:"next_text,omitempty"`
}

type speechResponse struct {
	AudioBase64 string `json:"audio_base64"`
	Alignment   *struct {
		Characters []string  `json:"characters"`
		Starts     []float64 `json:"character_start_times_seconds"`
		Ends       []float64 `json:"character_end_times_seconds"`
	} `json:"alignment"`
}

type errorResponse struct {
	Detail struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"detail"`
}

// synthesize speaks text in the ElevenLabs voice. previous and next are the
// neighbouring segments, so sentences flow across segment boundaries.
func (e *elevenLabs) synthesize(ctx context.Context, voiceID, text, previous, next string) (speech, error) {
	body, err := json.Marshal(speechRequest{
		Text: text, ModelID: e.model, VoiceSettings: narration,
		PreviousText: tail(previous, maxContextBytes), NextText: head(next, maxContextBytes),
	})
	if err != nil {
		return speech{}, err
	}
	endpoint := e.baseURL + "/v1/text-to-speech/" + url.PathEscape(voiceID) + "/with-timestamps?output_format=" + url.QueryEscape(e.outputFormat)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return speech{}, err
	}
	req.Header.Set("xi-api-key", e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return speech{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return speech{}, err
	}
	if len(raw) > maxResponseBytes {
		return speech{}, errors.New("elevenlabs response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return speech{}, classify(resp.StatusCode, raw)
	}
	var out speechResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return speech{}, fmt.Errorf("elevenlabs response: %w", err)
	}
	audio, err := base64.StdEncoding.DecodeString(out.AudioBase64)
	if err != nil || len(audio) == 0 {
		return speech{}, errors.New("elevenlabs returned no audio")
	}
	result := speech{audio: audio, charStartsMS: make([]int64, utf8.RuneCountInString(text))}
	if a := out.Alignment; a != nil && len(a.Starts) > 0 && len(a.Starts) == len(a.Ends) {
		result.durationMS = int64(a.Ends[len(a.Ends)-1] * 1000)
		// The alignment normally has one entry per code point of the text. If
		// it differs, each code point takes the start of the entry at the same
		// relative position, which keeps highlighting close.
		for i := range result.charStartsMS {
			j := i * len(a.Starts) / len(result.charStartsMS)
			result.charStartsMS[i] = int64(a.Starts[j] * 1000)
		}
	}
	return result, nil
}

// classify turns an ElevenLabs error into a retryable error, or errPermanent
// with a message for the owner.
func classify(status int, body []byte) error {
	var e errorResponse
	_ = json.Unmarshal(body, &e)
	switch {
	case e.Detail.Status == "quota_exceeded":
		return errPermanent{"Your ElevenLabs credits ran out. Add credits, then try again."}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errPermanent{"ElevenLabs refused the server's API key."}
	case status == http.StatusNotFound:
		return errPermanent{"ElevenLabs no longer offers this voice. Delete the audio and choose another voice."}
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return errPermanent{"ElevenLabs could not read part of this article."}
	default:
		return fmt.Errorf("elevenlabs status %d: %s", status, e.Detail.Status)
	}
}

// head returns at most max bytes from the start of s, on a character boundary.
func head(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// tail returns at most max bytes from the end of s, on a character boundary.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
