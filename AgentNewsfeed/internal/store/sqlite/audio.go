package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrLocked rejects a change to the text of an article that has audio: the
	// audio speaks that text, so the owner deletes the audio first.
	ErrLocked = fmt.Errorf("article text is locked while it has audio: %w", ErrConflict)
	// ErrAudioExists rejects new audio for an article that already has some
	// that has not failed.
	ErrAudioExists = fmt.Errorf("article already has audio: %w", ErrConflict)
)

// Audio statuses, as stored and as the API reports them.
const (
	AudioGenerating = "generating"
	AudioReady      = "ready"
	AudioFailed     = "failed"
)

// ArticleAudio is the spoken audio of one article, without its segments.
type ArticleAudio struct {
	ID            string
	OwnerID       string
	ArticleID     string
	Voice         string
	Status        string
	Error         string
	Characters    int
	CreatedAt     int64
	UpdatedAt     int64
	SegmentsTotal int
	SegmentsDone  int
	DurationMS    int64
}

// AudioSegment is one spoken piece of an article, in reading order. Text is
// what the reader shows; the voice says Spoken, or Text when Spoken is empty,
// and Sync maps one onto the other. CharStartsMS holds, once Done, the start
// time of each Unicode code point of Text.
type AudioSegment struct {
	Index        int
	Block        int
	Text         string
	Spoken       string
	Sync         []SyncPoint
	Done         bool
	DurationMS   int64
	CharStartsMS []int64
}

// SpokenText is what the voice says for the segment.
func (s AudioSegment) SpokenText() string {
	if s.Spoken == "" {
		return s.Text
	}
	return s.Spoken
}

// NewAudioSegment is what the owner's app asks to have spoken.
type NewAudioSegment struct {
	Block  int
	Text   string
	Spoken string
	Sync   []SyncPoint
}

// SyncPoint pairs a code point offset in a segment's shown text with the
// offset in its spoken text where the same words start.
type SyncPoint [2]int

const audioColumns = `au.id, au.owner_id, au.article_id, au.voice, au.status, au.error, au.characters, au.created_at, au.updated_at,
  (SELECT count(*) FROM article_audio_segments s WHERE s.audio_id = au.id),
  (SELECT count(*) FROM article_audio_segments s WHERE s.audio_id = au.id AND s.done = 1),
  (SELECT coalesce(sum(duration_ms), 0) FROM article_audio_segments s WHERE s.audio_id = au.id)`

func scanAudio(row scanner) (ArticleAudio, error) {
	var a ArticleAudio
	err := row.Scan(&a.ID, &a.OwnerID, &a.ArticleID, &a.Voice, &a.Status, &a.Error, &a.Characters, &a.CreatedAt, &a.UpdatedAt, &a.SegmentsTotal, &a.SegmentsDone, &a.DurationMS)
	if errors.Is(err, sql.ErrNoRows) {
		return ArticleAudio{}, ErrNotFound
	}
	return a, err
}

// ArticleAudio returns the owner's audio for an article, or ErrNotFound.
func (s *Store) ArticleAudio(ctx context.Context, owner Owner, articleID string) (ArticleAudio, error) {
	return scanAudio(s.read.QueryRowContext(ctx, "SELECT "+audioColumns+" FROM article_audio au WHERE au.owner_id = ? AND au.article_id = ?", owner.id, articleID))
}

// AudioByID returns audio by its ID, for the generator, which works without an
// owner's request.
func (s *Store) AudioByID(ctx context.Context, id string) (ArticleAudio, error) {
	return scanAudio(s.read.QueryRowContext(ctx, "SELECT "+audioColumns+" FROM article_audio au WHERE au.id = ?", id))
}

// AudioSegments returns every segment of the audio, in order.
func (s *Store) AudioSegments(ctx context.Context, audioID string) ([]AudioSegment, error) {
	rows, err := s.read.QueryContext(ctx, "SELECT idx, block, text, spoken, sync, done, duration_ms, char_starts_ms FROM article_audio_segments WHERE audio_id = ? ORDER BY idx", audioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var segments []AudioSegment
	for rows.Next() {
		var seg AudioSegment
		var sync, starts string
		if err := rows.Scan(&seg.Index, &seg.Block, &seg.Text, &seg.Spoken, &sync, &seg.Done, &seg.DurationMS, &starts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sync), &seg.Sync); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(starts), &seg.CharStartsMS); err != nil {
			return nil, err
		}
		segments = append(segments, seg)
	}
	return segments, rows.Err()
}

// CreateArticleAudio records new audio for the article to generate, if the
// article is still at revision expected and outside Trash. Audio that failed is
// replaced; any other audio is ErrAudioExists. The caller has validated the
// segments; characters is their total length.
func (s *Store) CreateArticleAudio(ctx context.Context, owner Owner, articleID string, expected int64, voice string, characters int, segments []NewAudioSegment) (ArticleAudio, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return ArticleAudio{}, err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return ArticleAudio{}, err
	}
	var revision int64
	var trashed sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT revision, trashed_at FROM articles WHERE owner_id = ? AND id = ?", owner.id, articleID).Scan(&revision, &trashed)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && trashed.Valid) {
		return ArticleAudio{}, ErrNotFound
	}
	if err != nil {
		return ArticleAudio{}, err
	}
	if revision != expected {
		return ArticleAudio{}, ErrConflict
	}
	var status string
	err = tx.QueryRowContext(ctx, "SELECT status FROM article_audio WHERE owner_id = ? AND article_id = ?", owner.id, articleID).Scan(&status)
	switch {
	case err == nil && status != AudioFailed:
		return ArticleAudio{}, ErrAudioExists
	case err == nil:
		if err := deleteArticleAudioTx(ctx, tx, owner.id, articleID); err != nil {
			return ArticleAudio{}, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return ArticleAudio{}, err
	}
	now := time.Now().UnixMilli()
	id := rand.Text()
	if _, err := tx.ExecContext(ctx, `INSERT INTO article_audio(owner_id, article_id, id, voice, status, characters, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?)`, owner.id, articleID, id, voice, AudioGenerating, characters, now, now); err != nil {
		return ArticleAudio{}, err
	}
	for i, seg := range segments {
		sync := []byte("[]")
		if len(seg.Sync) > 0 {
			sync, _ = json.Marshal(seg.Sync)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO article_audio_segments(audio_id, idx, block, text, spoken, sync) VALUES (?,?,?,?,?,?)", id, i, seg.Block, seg.Text, seg.Spoken, string(sync)); err != nil {
			return ArticleAudio{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ArticleAudio{}, err
	}
	return ArticleAudio{
		ID: id, OwnerID: owner.id, ArticleID: articleID, Voice: voice, Status: AudioGenerating,
		Characters: characters, CreatedAt: now, UpdatedAt: now, SegmentsTotal: len(segments),
	}, nil
}

// DeleteArticleAudio deletes the article's audio, if it has any, which unlocks
// its text. The generator removes the audio files.
func (s *Store) DeleteArticleAudio(ctx context.Context, owner Owner, articleID string) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return err
	}
	if err := deleteArticleAudioTx(ctx, tx, owner.id, articleID); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteArticleAudioTx deletes an article's audio row; its segments go with it.
func deleteArticleAudioTx(ctx context.Context, tx *sql.Tx, ownerID, articleID string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM article_audio WHERE owner_id = ? AND article_id = ?", ownerID, articleID)
	return err
}

// articleHasAudio reports whether the article's text is locked by its audio.
func articleHasAudio(ctx context.Context, tx *sql.Tx, ownerID, articleID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM article_audio WHERE owner_id = ? AND article_id = ?)", ownerID, articleID).Scan(&exists)
	return exists, err
}

// GeneratingAudioIDs lists audio still being generated, oldest first.
func (s *Store) GeneratingAudioIDs(ctx context.Context) ([]string, error) {
	return s.audioIDs(ctx, "SELECT id FROM article_audio WHERE status = ? ORDER BY created_at", AudioGenerating)
}

// AllAudioIDs lists every audio ID, so the generator can remove files that no
// audio owns any more.
func (s *Store) AllAudioIDs(ctx context.Context) ([]string, error) {
	return s.audioIDs(ctx, "SELECT id FROM article_audio")
}

func (s *Store) audioIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CompleteAudioSegment records a generated segment, and marks the audio ready
// once every segment is done. ErrNotFound means the audio was deleted meanwhile.
func (s *Store) CompleteAudioSegment(ctx context.Context, audioID string, index int, durationMS int64, charStartsMS []int64) error {
	starts, err := json.Marshal(charStartsMS)
	if err != nil {
		return err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE article_audio_segments SET done = 1, duration_ms = ?, char_starts_ms = ? WHERE audio_id = ? AND idx = ?", durationMS, string(starts), audioID, index)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE article_audio SET updated_at = ?,
  status = CASE WHEN EXISTS(SELECT 1 FROM article_audio_segments WHERE audio_id = ? AND done = 0) THEN status ELSE ? END
  WHERE id = ?`, time.Now().UnixMilli(), audioID, AudioReady, audioID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailArticleAudio marks audio that could not be generated, with a message the
// owner may read. The owner can then delete it or create it again.
func (s *Store) FailArticleAudio(ctx context.Context, audioID, message string) error {
	_, err := s.write.ExecContext(ctx, "UPDATE article_audio SET status = ?, error = ?, updated_at = ? WHERE id = ? AND status = ?", AudioFailed, message, time.Now().UnixMilli(), audioID, AudioGenerating)
	return err
}
