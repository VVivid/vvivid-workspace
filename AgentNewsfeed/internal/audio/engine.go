// Package audio turns an article's text into spoken MP3 segments with
// ElevenLabs, one segment at a time, and keeps them as files. The feed service
// owns who may create, read and delete audio; this package only generates and
// stores it.
package audio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"agentnewsfeed/internal/store/sqlite"
)

// Voice is one voice the owner may choose. ID is the app's name for it;
// ElevenLabsID is the voice in ElevenLabs.
type Voice struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ElevenLabsID string `json:"elevenlabs_voice_id"`
}

type Config struct {
	APIKey string
	// Model and OutputFormat are ElevenLabs settings.
	Model        string
	OutputFormat string
	// Dir holds one folder of MP3 files per audio, plus voice samples.
	Dir     string
	Voices  []Voice
	BaseURL string
}

// sampleText is what a voice sample says. It contains nothing private.
const sampleText = "Hi, I'm %s. I'll read your saved articles aloud, from system design chapters to LeetCode walkthroughs, at whatever speed you like."

// attempts is how often one segment is tried before the audio fails.
const attempts = 4

// Engine generates audio in the background. Create work with the store, then
// call Wake.
type Engine struct {
	cfg    Config
	store  *sqlite.Store
	logger *slog.Logger
	tts    *elevenLabs
	wake   chan struct{}

	mu sync.Mutex
	// cancels stops the generation of an audio that is deleted meanwhile.
	cancels map[string]context.CancelFunc
	// samples serializes sample generation, so a voice is paid for once.
	samples sync.Mutex
}

func New(cfg Config, store *sqlite.Store, logger *slog.Logger) (*Engine, error) {
	if cfg.APIKey == "" || cfg.Dir == "" || len(cfg.Voices) == 0 {
		return nil, errors.New("audio needs an API key, a directory and at least one voice")
	}
	for i, v := range cfg.Voices {
		if v.ID == "" || v.Name == "" || v.ElevenLabsID == "" || slices.ContainsFunc(cfg.Voices[:i], func(o Voice) bool { return o.ID == v.ID }) {
			return nil, fmt.Errorf("audio voice %d needs a unique id, a name and an ElevenLabs voice ID", i+1)
		}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("audio directory: %w", err)
	}
	return &Engine{
		cfg: cfg, store: store, logger: logger,
		tts: &elevenLabs{
			client: &http.Client{Timeout: 3 * time.Minute}, baseURL: cfg.BaseURL, apiKey: cfg.APIKey,
			model: cfg.Model, outputFormat: cfg.OutputFormat,
		},
		wake:    make(chan struct{}, 1),
		cancels: map[string]context.CancelFunc{},
	}, nil
}

// Voices lists the voices the owner may choose.
func (e *Engine) Voices() []Voice { return e.cfg.Voices }

// Voice returns the voice with the app's id.
func (e *Engine) Voice(id string) (Voice, bool) {
	i := slices.IndexFunc(e.cfg.Voices, func(v Voice) bool { return v.ID == id })
	if i < 0 {
		return Voice{}, false
	}
	return e.cfg.Voices[i], true
}

// Wake makes the generator look for new work now.
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Stop cancels the generation of a deleted audio and removes its files.
func (e *Engine) Stop(audioID string) {
	e.mu.Lock()
	if cancel, ok := e.cancels[audioID]; ok {
		cancel()
	}
	e.mu.Unlock()
	e.removeFiles(audioID)
}

// Run generates audio until ctx ends. It first removes files no audio owns any
// more, then resumes audio left generating by a restart.
func (e *Engine) Run(ctx context.Context) {
	e.sweep(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		ids, err := e.store.GeneratingAudioIDs(ctx)
		if err != nil && ctx.Err() == nil {
			e.logger.Error("audio queue unavailable", "error", err)
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			e.generate(ctx, id)
		}
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-ticker.C:
		}
	}
}

// generate speaks every segment of one audio that is not done yet.
func (e *Engine) generate(parent context.Context, audioID string) {
	ctx, cancel := context.WithCancel(parent)
	e.mu.Lock()
	e.cancels[audioID] = cancel
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.cancels, audioID)
		e.mu.Unlock()
		cancel()
	}()

	audio, err := e.store.AudioByID(ctx, audioID)
	if err != nil {
		return
	}
	voice, ok := e.Voice(audio.Voice)
	if !ok {
		e.fail(ctx, audioID, errPermanent{"This voice is no longer offered. Delete the audio and choose another voice."})
		return
	}
	segments, err := e.store.AudioSegments(ctx, audioID)
	if err != nil {
		return
	}
	if err := os.MkdirAll(e.audioDir(audioID), 0o700); err != nil {
		e.logger.Error("audio directory unavailable", "error", err)
		return
	}
	for i, seg := range segments {
		if seg.Done {
			continue
		}
		var previous, next string
		if i > 0 {
			previous = segments[i-1].Text
		}
		if i+1 < len(segments) {
			next = segments[i+1].Text
		}
		spoken, err := e.synthesize(ctx, voice.ElevenLabsID, seg.Text, previous, next)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			e.fail(ctx, audioID, err)
			return
		}
		if err := writeFile(e.segmentPath(audioID, seg.Index), spoken.audio); err != nil {
			e.logger.Error("audio segment not saved", "error", err)
			return
		}
		if err := e.store.CompleteAudioSegment(ctx, audioID, seg.Index, spoken.durationMS, spoken.charStartsMS); err != nil {
			// The owner deleted the audio while this segment was spoken.
			if errors.Is(err, sqlite.ErrNotFound) {
				e.removeFiles(audioID)
			}
			return
		}
	}
}

// synthesize tries a segment a few times, waiting longer after each failure.
func (e *Engine) synthesize(ctx context.Context, voiceID, text, previous, next string) (speech, error) {
	var err error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return speech{}, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 5 * time.Second):
			}
		}
		var spoken speech
		spoken, err = e.tts.synthesize(ctx, voiceID, text, previous, next)
		if _, permanent := errors.AsType[errPermanent](err); err == nil || permanent || ctx.Err() != nil {
			return spoken, err
		}
		e.logger.Warn("audio segment retry", "attempt", attempt+1, "error", err)
	}
	return speech{}, err
}

func (e *Engine) fail(ctx context.Context, audioID string, err error) {
	message := "ElevenLabs didn't respond. Try again later."
	if permanent, ok := errors.AsType[errPermanent](err); ok {
		message = permanent.message
	}
	e.logger.Error("audio failed", "error", err)
	if err := e.store.FailArticleAudio(ctx, audioID, message); err != nil {
		e.logger.Error("audio failure not recorded", "error", err)
	}
}

// Segment reads one generated segment's MP3.
func (e *Engine) Segment(audioID string, index int) ([]byte, error) {
	return os.ReadFile(e.segmentPath(audioID, index))
}

// Sample returns a short MP3 of the voice, generated once and then kept.
func (e *Engine) Sample(ctx context.Context, voice Voice) ([]byte, error) {
	e.samples.Lock()
	defer e.samples.Unlock()
	path := filepath.Join(e.cfg.Dir, "samples", voice.ID+"-"+e.cfg.Model+".mp3")
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}
	spoken, err := e.tts.synthesize(ctx, voice.ElevenLabsID, fmt.Sprintf(sampleText, voice.Name), "", "")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return spoken.audio, writeFile(path, spoken.audio)
}

// sweep removes audio folders that no audio owns, such as audio deleted with
// its article's move to Trash.
func (e *Engine) sweep(ctx context.Context) {
	ids, err := e.store.AllAudioIDs(ctx)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(e.cfg.Dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "samples" && !slices.Contains(ids, entry.Name()) {
			e.Stop(entry.Name())
		}
	}
}

// Sweep removes leftover audio files now, for example after a move to Trash.
func (e *Engine) Sweep(ctx context.Context) { e.sweep(ctx) }

func (e *Engine) audioDir(audioID string) string { return filepath.Join(e.cfg.Dir, audioID) }

func (e *Engine) segmentPath(audioID string, index int) string {
	return filepath.Join(e.audioDir(audioID), strconv.Itoa(index)+".mp3")
}

func (e *Engine) removeFiles(audioID string) {
	if err := os.RemoveAll(e.audioDir(audioID)); err != nil {
		e.logger.Error("audio files not removed", "error", err)
	}
}

// writeFile replaces path atomically, so a reader never sees half a file.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
