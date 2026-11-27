package feed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentnewsfeed/internal/audio"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

// ErrAudioUnavailable means this server has no ElevenLabs key, so it offers no
// audio.
var ErrAudioUnavailable = errors.New("audio is not configured")

// errInvalidAudio rejects a create request whose voice or segments are invalid.
var errInvalidAudio = fmt.Errorf("invalid audio request: %w", ErrInvalid)

// Limits on what the owner's app may ask to have spoken: each segment fits one
// ElevenLabs request, and the whole stays within the longest article.
const (
	maxAudioSegments       = 2000
	maxAudioSegmentBytes   = 4500
	maxAudioCharacters     = 300_000
	audioTitleSummaryBlock = -1
)

// EnableAudio lets the service create and serve audio with engine.
func (s *Service) EnableAudio(engine *audio.Engine) { s.audio = engine }

type AudioSegmentInput struct {
	Block int    `json:"block"`
	Text  string `json:"text"`
}

type CreateAudioInput struct {
	Voice            string              `json:"voice"`
	ExpectedRevision int64               `json:"expected_revision"`
	Segments         []AudioSegmentInput `json:"segments"`
}

type AudioVoice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type AudioVoices struct {
	Voices []AudioVoice `json:"voices"`
}

type AudioSegment struct {
	Index        int     `json:"index"`
	Block        int     `json:"block"`
	Text         string  `json:"text"`
	DurationMS   int64   `json:"duration_ms"`
	CharStartsMS []int64 `json:"char_starts_ms"`
}

// AudioStatus is an article's audio as the app sees it. Segments, with their
// text and timing, come only once the audio is ready.
type AudioStatus struct {
	Status        string         `json:"status"`
	Voice         AudioVoice     `json:"voice"`
	CreatedAt     int64          `json:"created_at"`
	Characters    int            `json:"characters"`
	SegmentsTotal int            `json:"segments_total"`
	SegmentsDone  int            `json:"segments_done"`
	Error         string         `json:"error,omitempty"`
	DurationMS    int64          `json:"duration_ms,omitzero"`
	Segments      []AudioSegment `json:"segments,omitempty"`
}

func (s *Service) authorizeAudio(ctx context.Context, p identity.Principal, scopes ...string) error {
	if s.audio == nil {
		return ErrAudioUnavailable
	}
	return s.authorize(ctx, p, scopes...)
}

// ListAudioVoices lists the voices the owner may choose.
func (s *Service) ListAudioVoices(ctx context.Context, p identity.Principal) (AudioVoices, error) {
	if err := s.authorizeAudio(ctx, p, identity.Read); err != nil {
		return AudioVoices{}, err
	}
	out := AudioVoices{Voices: []AudioVoice{}}
	for _, v := range s.audio.Voices() {
		out.Voices = append(out.Voices, AudioVoice{ID: v.ID, Name: v.Name, Description: v.Description})
	}
	return out, nil
}

// AudioVoiceSample returns a short MP3 of a voice.
func (s *Service) AudioVoiceSample(ctx context.Context, p identity.Principal, voiceID string) ([]byte, error) {
	if err := s.authorizeAudio(ctx, p, identity.Read); err != nil {
		return nil, err
	}
	voice, ok := s.audio.Voice(voiceID)
	if !ok {
		return nil, sqlite.ErrNotFound
	}
	return s.audio.Sample(ctx, voice)
}

// ArticleAudio returns the article's audio, or sqlite.ErrNotFound if it has none.
func (s *Service) ArticleAudio(ctx context.Context, p identity.Principal, articleID string) (AudioStatus, error) {
	if err := s.authorizeAudio(ctx, p, identity.Read); err != nil {
		return AudioStatus{}, err
	}
	a, err := s.store.ArticleAudio(ctx, p.Owner(), articleID)
	if err != nil {
		return AudioStatus{}, err
	}
	out := s.audioStatus(a)
	if a.Status == sqlite.AudioReady {
		segments, err := s.store.AudioSegments(ctx, a.ID)
		if err != nil {
			return AudioStatus{}, err
		}
		for _, seg := range segments {
			out.Segments = append(out.Segments, AudioSegment{Index: seg.Index, Block: seg.Block, Text: seg.Text, DurationMS: seg.DurationMS, CharStartsMS: seg.CharStartsMS})
		}
	}
	return out, nil
}

func (s *Service) audioStatus(a sqlite.ArticleAudio) AudioStatus {
	voice := AudioVoice{ID: a.Voice, Name: a.Voice}
	if v, ok := s.audio.Voice(a.Voice); ok {
		voice.Name = v.Name
	}
	out := AudioStatus{
		Status: a.Status, Voice: voice, CreatedAt: a.CreatedAt, Characters: a.Characters,
		SegmentsTotal: a.SegmentsTotal, SegmentsDone: a.SegmentsDone, Error: a.Error,
	}
	if a.Status == sqlite.AudioReady {
		out.DurationMS = a.DurationMS
	}
	return out
}

// CreateArticleAudio starts generating audio for the article at the revision
// the app read, from the segments the app built from that text. From now on the
// article's text is locked until the audio is deleted.
func (s *Service) CreateArticleAudio(ctx context.Context, p identity.Principal, articleID string, in CreateAudioInput) (AudioStatus, error) {
	if err := s.authorizeAudio(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return AudioStatus{}, err
	}
	if _, ok := s.audio.Voice(in.Voice); !ok || len(in.Segments) == 0 || len(in.Segments) > maxAudioSegments {
		return AudioStatus{}, errInvalidAudio
	}
	segments := make([]sqlite.NewAudioSegment, 0, len(in.Segments))
	characters := 0
	for _, seg := range in.Segments {
		if !validSpokenText(seg.Text) || seg.Block < audioTitleSummaryBlock {
			return AudioStatus{}, errInvalidAudio
		}
		characters += utf8.RuneCountInString(seg.Text)
		segments = append(segments, sqlite.NewAudioSegment{Block: seg.Block, Text: seg.Text})
	}
	if characters > maxAudioCharacters {
		return AudioStatus{}, errInvalidAudio
	}
	a, err := s.store.CreateArticleAudio(ctx, p.Owner(), articleID, in.ExpectedRevision, in.Voice, characters, segments)
	if err != nil {
		return AudioStatus{}, err
	}
	s.audio.Wake()
	return s.audioStatus(a), nil
}

func validSpokenText(text string) bool {
	return len(text) <= maxAudioSegmentBytes && utf8.ValidString(text) && strings.TrimSpace(text) != "" &&
		!strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' })
}

// DeleteArticleAudio deletes the article's audio, if any, and unlocks its text.
// It stops a generation that is still running.
func (s *Service) DeleteArticleAudio(ctx context.Context, p identity.Principal, articleID string) error {
	if err := s.authorizeAudio(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return err
	}
	a, err := s.store.ArticleAudio(ctx, p.Owner(), articleID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.store.DeleteArticleAudio(ctx, p.Owner(), articleID); err != nil {
		return err
	}
	s.audio.Stop(a.ID)
	return nil
}

// ArticleAudioSegment returns one generated segment's MP3, or
// sqlite.ErrNotFound before it is generated.
func (s *Service) ArticleAudioSegment(ctx context.Context, p identity.Principal, articleID string, index int) ([]byte, error) {
	if err := s.authorizeAudio(ctx, p, identity.Read); err != nil {
		return nil, err
	}
	a, err := s.store.ArticleAudio(ctx, p.Owner(), articleID)
	if err != nil {
		return nil, err
	}
	segments, err := s.store.AudioSegments(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(segments) || !segments[index].Done {
		return nil, sqlite.ErrNotFound
	}
	return s.audio.Segment(a.ID, index)
}

// sweepAudioFiles removes the files of audio deleted with its article's move
// to Trash.
func (s *Service) sweepAudioFiles(ctx context.Context) {
	if s.audio != nil {
		s.audio.Sweep(ctx)
	}
}
