package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/url"
	"time"
	_ "time/tzdata" // Daily limits keep their meaning in the distroless image.
)

type DiscoveryRun struct {
	ID                  string `json:"id"`
	PreferencesRevision int64  `json:"preferences_revision"`
	Status              string `json:"status"`
	StartedAt           int64  `json:"started_at"`
	ExpiresAt           int64  `json:"expires_at"`
	FinishedAt          int64  `json:"finished_at"`
	SavedCount          int    `json:"saved_count"`
	Note                string `json:"note"`
}

// DiscoveryItem preserves the source identity as saved by discovery. Later
// edits to the linked article do not rewrite this history.
type DiscoveryItem struct {
	ArticleID   string `json:"article_id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	WhyMatch    string `json:"why_match"`
	PublishedAt int64  `json:"published_at"`
	Evergreen   bool   `json:"evergreen"`
	SavedAt     int64  `json:"saved_at"`
}
type DiscoveryRunDetail struct {
	Run      DiscoveryRun    `json:"run"`
	Articles []DiscoveryItem `json:"articles"`
}
type DiscoveryContext struct {
	Preferences    PreferencesState `json:"preferences"`
	RecentURLs     []string         `json:"recent_urls"`
	Runs           []DiscoveryRun   `json:"runs"`
	Day            string           `json:"day"`
	DayTimezone    string           `json:"day_timezone"`
	SavedToday     int              `json:"saved_today"`
	RemainingToday int              `json:"remaining_today"`
}

var discoveryLocation = func() *time.Location {
	v, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return v
}()

func discoveryDay(now time.Time) (string, int64, int64) {
	local := now.In(discoveryLocation)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, discoveryLocation)
	return start.Format("2006-01-02"), start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli()
}
func discoverySavedToday(ctx context.Context, db rowQueryer, owner Owner, now time.Time) (int, error) {
	_, start, end := discoveryDay(now)
	var count int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM discovery_articles WHERE owner_id=? AND saved_at>=? AND saved_at<?", owner.id, start, end).Scan(&count)
	return count, err
}

const runColumns = "id,preferences_revision,status,started_at,expires_at,finished_at,saved_count,note"

func scanRun(row scanner) (DiscoveryRun, error) {
	var out DiscoveryRun
	err := row.Scan(&out.ID, &out.PreferencesRevision, &out.Status, &out.StartedAt, &out.ExpiresAt, &out.FinishedAt, &out.SavedCount, &out.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if out.Status == "active" && out.ExpiresAt <= time.Now().UnixMilli() {
		out.Status = "expired"
	}
	return out, err
}
func (s *Store) ListDiscoveryRuns(ctx context.Context, owner Owner, limit int, before int64) ([]DiscoveryRun, error) {
	if limit < 1 || limit > 50 || before < 0 {
		return nil, ErrLimit
	}
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, "SELECT "+runColumns+" FROM discovery_runs WHERE owner_id=? AND (?=0 OR started_at<?) ORDER BY started_at DESC LIMIT ?", owner.id, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoveryRun{}
	for rows.Next() {
		v, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) DiscoveryRun(ctx context.Context, owner Owner, id string) (DiscoveryRunDetail, error) {
	out := DiscoveryRunDetail{Articles: []DiscoveryItem{}}
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return out, err
	}
	run, err := scanRun(s.read.QueryRowContext(ctx, "SELECT "+runColumns+" FROM discovery_runs WHERE owner_id=? AND id=?", owner.id, id))
	if err != nil {
		return out, err
	}
	out.Run = run
	rows, err := s.read.QueryContext(ctx, `SELECT article_id,source_url,source_title,why_match,published_at,evergreen,saved_at
 FROM discovery_articles WHERE owner_id=? AND run_id=? ORDER BY saved_at,article_id`, owner.id, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item DiscoveryItem
		if err := rows.Scan(&item.ArticleID, &item.URL, &item.Title, &item.WhyMatch, &item.PublishedAt, &item.Evergreen, &item.SavedAt); err != nil {
			return out, err
		}
		out.Articles = append(out.Articles, item)
	}
	return out, rows.Err()
}
func (s *Store) DiscoveryContext(ctx context.Context, owner Owner) (DiscoveryContext, error) {
	out := DiscoveryContext{RecentURLs: []string{}, DayTimezone: "America/New_York"}
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return out, err
	}
	var err error
	out.Preferences, err = preferencesState(ctx, s.read, owner)
	if err != nil {
		return out, err
	}
	out.Runs, err = s.ListDiscoveryRuns(ctx, owner, 5, 0)
	if err != nil {
		return out, err
	}
	out.Day, _, _ = discoveryDay(time.Now())
	out.SavedToday, err = discoverySavedToday(ctx, s.read, owner, time.Now())
	if err != nil {
		return out, err
	}
	out.RemainingToday = max(0, out.Preferences.Preferences.Discovery.MaxPerDay-out.SavedToday)
	rows, err := s.read.QueryContext(ctx, "SELECT canonical_url FROM articles WHERE owner_id=? ORDER BY created_at DESC,id DESC LIMIT 200", owner.id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		out.RecentURLs = append(out.RecentURLs, raw)
	}
	return out, rows.Err()
}
func (s *Store) StartDiscoveryRun(ctx context.Context, owner Owner, key string, payload []byte, expected int64) (DiscoveryRun, error) {
	return recommendationMutation(ctx, s, owner, key, "start_discovery_run", payload, func(tx *sql.Tx) (DiscoveryRun, error) {
		var out DiscoveryRun
		now := time.Now()
		prefs, err := preferencesState(ctx, tx, owner)
		if err != nil {
			return out, err
		}
		if prefs.Revision != expected {
			return out, ErrConflict
		}
		if !prefs.Preferences.Discovery.Enabled {
			return out, ErrLimit
		}
		count, err := discoverySavedToday(ctx, tx, owner, now)
		if err != nil {
			return out, err
		}
		if count >= prefs.Preferences.Discovery.MaxPerDay {
			return out, ErrLimit
		}
		if _, err = tx.ExecContext(ctx, "UPDATE discovery_runs SET status='expired',finished_at=expires_at WHERE owner_id=? AND status='active' AND expires_at<=?", owner.id, now.UnixMilli()); err != nil {
			return out, err
		}
		var blocked bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM discovery_runs WHERE owner_id=? AND (status='active' OR hour_slot=?))", owner.id, now.Unix()/3600).Scan(&blocked); err != nil {
			return out, err
		}
		if blocked {
			return out, ErrLimit
		}
		out = DiscoveryRun{ID: rand.Text(), PreferencesRevision: expected, Status: "active", StartedAt: now.UnixMilli(), ExpiresAt: now.Add(55 * time.Minute).UnixMilli()}
		_, err = tx.ExecContext(ctx, "INSERT INTO discovery_runs(owner_id,id,hour_slot,preferences_revision,status,started_at,expires_at) VALUES (?,?,?,?,?,?,?)", owner.id, out.ID, now.Unix()/3600, expected, out.Status, out.StartedAt, out.ExpiresAt)
		return out, err
	})
}
func (s *Store) FinishDiscoveryRun(ctx context.Context, owner Owner, key string, payload []byte, id, status, note string) (DiscoveryRun, error) {
	return recommendationMutation(ctx, s, owner, key, "finish_discovery_run", payload, func(tx *sql.Tx) (DiscoveryRun, error) {
		run, err := scanRun(tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM discovery_runs WHERE owner_id=? AND id=?", owner.id, id))
		if err != nil {
			return run, err
		}
		if run.Status != "active" {
			return run, ErrConflict
		}
		run.Status, run.Note, run.FinishedAt = status, note, time.Now().UnixMilli()
		_, err = tx.ExecContext(ctx, "UPDATE discovery_runs SET status=?,note=?,finished_at=? WHERE owner_id=? AND id=?", run.Status, run.Note, run.FinishedAt, owner.id, id)
		return run, err
	})
}

// CheckDiscoveryArticle runs before fetching a thumbnail and again under the
// article transaction. User text never causes an outbound source request.
func checkDiscoveryArticle(ctx context.Context, db rowQueryer, owner Owner, runID string, expected int64, a Article, item DiscoveryItem) error {
	prefs, err := preferencesState(ctx, db, owner)
	if err != nil {
		return err
	}
	if prefs.Revision != expected {
		return ErrConflict
	}
	p := prefs.Preferences
	run, err := scanRun(db.QueryRowContext(ctx, "SELECT "+runColumns+" FROM discovery_runs WHERE owner_id=? AND id=?", owner.id, runID))
	if err != nil {
		return err
	}
	if run.Status != "active" || run.PreferencesRevision != expected {
		return ErrConflict
	}
	if !p.Discovery.Enabled || run.SavedCount >= p.Discovery.MaxPerRun {
		return ErrLimit
	}
	saved, err := discoverySavedToday(ctx, db, owner, time.Now())
	if err != nil {
		return err
	}
	if saved >= p.Discovery.MaxPerDay {
		return ErrLimit
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		return ErrConflict
	}
	allowed := p.AllowNewSources
	for _, source := range p.Sources {
		if source.Domain == u.Hostname() {
			if source.Preference == "block" {
				return ErrLimit
			}
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrLimit
	}
	now := time.Now().UnixMilli()
	if item.PublishedAt < 0 || item.PublishedAt > now+5*60*1000 {
		return ErrConflict
	}
	if p.Discovery.PublicationDays > 0 && (item.PublishedAt == 0 || item.PublishedAt < now-int64(p.Discovery.PublicationDays)*24*60*60*1000) {
		if !p.Discovery.IncludeEvergreen || !item.Evergreen {
			return ErrLimit
		}
	}
	var duplicate bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM articles WHERE owner_id=? AND canonical_url=?)", owner.id, a.URL).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return ErrConflict
	}
	return nil
}
func (s *Store) CheckDiscoveryArticle(ctx context.Context, owner Owner, runID string, expected int64, a Article, item DiscoveryItem) error {
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return err
	}
	return checkDiscoveryArticle(ctx, s.read, owner, runID, expected, a, item)
}
func (s *Store) IngestDiscoveryArticle(ctx context.Context, owner Owner, key string, payload []byte, runID string, expected int64, a Article, item DiscoveryItem, change ThumbnailChange) (MutationResult, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return MutationResult{}, err
	}
	if out, found, err := replayMutation(ctx, tx, owner, key, "ingest_discovery_article", payload); found || err != nil {
		return out, err
	}
	if err := checkDiscoveryArticle(ctx, tx, owner, runID, expected, a, item); err != nil {
		return MutationResult{}, err
	}
	out, err := mutateArticleTx(ctx, tx, owner, key, "ingest_discovery_article", payload, a, 0, change)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO discovery_articles(owner_id,article_id,run_id,source_url,source_title,why_match,published_at,evergreen,saved_at) VALUES (?,?,?,?,?,?,?,?,?)", owner.id, out.Article.ID, runID, out.Article.URL, out.Article.Title, item.WhyMatch, item.PublishedAt, item.Evergreen, out.Article.CreatedAt); err != nil {
		return MutationResult{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE discovery_runs SET saved_count=saved_count+1 WHERE owner_id=? AND id=?", owner.id, runID); err != nil {
		return MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult{}, err
	}
	return out, nil
}
