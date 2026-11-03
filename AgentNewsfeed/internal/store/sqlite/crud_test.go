package sqlite

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"testing"
	"time"
)

func thumbnailStore(t *testing.T) (*Store, string, Owner, Owner) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "thumbnail.sqlite")
	s, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	owner, err := s.EnrollAccount(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.EnrollAccount(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	return s, path, owner, other
}

func thumbnailJPEG(t *testing.T, red uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	for x := range 80 {
		for y := range 80 {
			img.SetRGBA(x, y, color.RGBA{R: red, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func storeArticle(t *testing.T, s *Store, owner Owner, key string, imageBytes []byte) MutationResult {
	t.Helper()
	a := Article{URL: "https://example.com/" + key, Title: "Source title", Summary: "Source introduction", ThumbnailURL: "https://images.example.com/source.jpg", ContentMarkdown: "# Original source"}
	payload, _ := json.Marshal(a)
	out, err := s.MutateArticle(t.Context(), owner, key, "add_article", payload, a, 0, ThumbnailChange{Replace: true, JPEG: imageBytes})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countThumbnails(t *testing.T, s *Store, owner Owner) int {
	t.Helper()
	var count int
	if err := s.read.QueryRowContext(t.Context(), "SELECT count(*) FROM thumbnail_blobs WHERE owner_id=?", owner.id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestThumbnailDedupCleanupOwnerIsolationRestartAndNoJSONBytes(t *testing.T) {
	s, path, owner, other := thumbnailStore(t)
	body := thumbnailJPEG(t, 150)
	first := storeArticle(t, s, owner, "one", body)
	second := storeArticle(t, s, owner, "two", body)
	foreign := storeArticle(t, s, other, "other", body)
	if countThumbnails(t, s, owner) != 1 || countThumbnails(t, s, other) != 1 || first.Article.ThumbnailHash != second.Article.ThumbnailHash {
		t.Fatal("owner-scoped payload deduplication failed")
	}
	for _, id := range []string{first.Article.ID, "missing"} {
		if _, err := s.Thumbnail(t.Context(), other, id, false); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign/missing thumbnail disclosed")
		}
	}
	var result []byte
	if err := s.read.QueryRowContext(t.Context(), "SELECT result FROM idempotency_results WHERE owner_id=? AND key='one'", owner.id).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result, body) || bytes.Contains(result, []byte(base64.StdEncoding.EncodeToString(body))) || bytes.Contains(result, []byte(`"jpeg"`)) {
		t.Fatal("durable result leaked image bytes")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if saved, err := s.Thumbnail(t.Context(), owner, first.Article.ID, false); err != nil || !bytes.Equal(saved, body) {
		t.Fatal("restart lost SQLite thumbnail")
	}
	clear := first.Article
	clear.ThumbnailURL = ""
	if _, err := s.MutateArticle(t.Context(), owner, "clear-one", "patch_article", []byte("clear-one"), clear, 1, ThumbnailChange{Replace: true}); err != nil {
		t.Fatal(err)
	}
	if countThumbnails(t, s, owner) != 1 {
		t.Fatal("clearing one reference removed shared payload")
	}
	trash, err := s.MutateArticle(t.Context(), owner, "trash-two", "trash_article", []byte("trash-two"), Article{ID: second.Article.ID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := s.Thumbnail(t.Context(), owner, second.Article.ID, true); err != nil || !bytes.Equal(saved, body) {
		t.Fatal("Trash did not retain thumbnail")
	}
	if _, err := s.Thumbnail(t.Context(), owner, second.Article.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("thumbnail ordinary read included Trash")
	}
	restored, err := s.MutateArticle(t.Context(), owner, "restore-two", "restore_article", []byte("restore-two"), Article{ID: second.Article.ID}, trash.Article.Revision)
	if err != nil {
		t.Fatal(err)
	}
	clear = restored.Article
	clear.ThumbnailURL = ""
	if _, err := s.MutateArticle(t.Context(), owner, "clear-two", "patch_article", []byte("clear-two"), clear, restored.Article.Revision, ThumbnailChange{Replace: true}); err != nil {
		t.Fatal(err)
	}
	if countThumbnails(t, s, owner) != 0 || countThumbnails(t, s, other) != 1 {
		t.Fatal("last-reference cleanup crossed owner or retained orphan")
	}
	if saved, err := s.Thumbnail(t.Context(), other, foreign.Article.ID, false); err != nil || !bytes.Equal(saved, body) {
		t.Fatal("cleanup affected other owner")
	}
	var original MutationResult
	if err := json.Unmarshal(result, &original); err != nil {
		t.Fatal(err)
	}
	a := Article{URL: "https://example.com/one", Title: "Source title", Summary: "Source introduction", ThumbnailURL: "https://images.example.com/source.jpg", ContentMarkdown: "# Original source"}
	payload, _ := json.Marshal(a)
	if replay, err := s.MutateArticle(t.Context(), owner, "one", "add_article", payload, a, 0); err != nil || replay != original {
		t.Fatal("thumbnail cleanup changed historical retry result")
	}
}

func TestPreparedThumbnailRollbackAndDedupRaceLeaveNoOrphans(t *testing.T) {
	s, _, owner, _ := thumbnailStore(t)
	one := storeArticle(t, s, owner, "one", thumbnailJPEG(t, 30))
	two := storeArticle(t, s, owner, "two", thumbnailJPEG(t, 60))
	candidate := one.Article
	candidate.URL = two.Article.URL
	newImage := ThumbnailChange{Replace: true, JPEG: thumbnailJPEG(t, 90)}
	if _, err := s.MutateArticle(t.Context(), owner, "collision", "patch_article", []byte("collision"), candidate, 1, newImage); !errors.Is(err, ErrConflict) {
		t.Fatal("collision accepted")
	}
	candidate.URL = one.Article.URL
	if _, err := s.MutateArticle(t.Context(), owner, "stale", "patch_article", []byte("stale"), candidate, 2, newImage); !errors.Is(err, ErrConflict) {
		t.Fatal("stale image accepted")
	}
	// Models a canonical duplicate that committed while the candidate fetched.
	duplicate := Article{URL: one.Article.URL, Title: "Do not replace", Summary: "Do not replace", ThumbnailURL: "https://images.example.com/new.jpg"}
	out, err := s.MutateArticle(t.Context(), owner, "racing-add", "add_article", []byte("racing-add"), duplicate, 0, newImage)
	if err != nil || out.Created || out.Article != one.Article || countThumbnails(t, s, owner) != 2 {
		t.Fatal("dedup race replaced article or persisted orphan")
	}
	if _, err := s.MutateArticle(t.Context(), owner, "collision", "patch_article", []byte("fixed"), candidate, 1, newImage); err != nil {
		t.Fatal("failure reserved key")
	}
	if countThumbnails(t, s, owner) != 2 {
		t.Fatal("replacement retained old unreferenced payload")
	}
}

func TestThumbnailBudgetIncludesTrashAllowsSharingAndReplacement(t *testing.T) {
	s, _, owner, other := thumbnailStore(t)
	// A full owner budget with retained references. Payload padding makes the
	// fixture exactly 512 * 100 KiB without generating 512 expensive images.
	body := thumbnailJPEG(t, 100)
	body = append(body, make([]byte, MaxThumbnailBytes-len(body))...)
	sum := sha256.Sum256(body)
	sharedHash := hex.EncodeToString(sum[:])
	tx, err := s.write.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 512 {
		id, hash := fmt.Sprintf("budget-%d", i), fmt.Sprintf("%064d", i)
		if i == 0 {
			hash = sharedHash
		}
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO articles(owner_id,id,canonical_url,title,summary,thumbnail_url,revision,created_at,updated_at,trashed_at,thumbnail_status)
 VALUES (?,?,?,'Budget fixture','Budget fixture','https://images.example.com/a.jpg',1,1,1,1,'stored')`, owner.id, id, "https://example.com/"+id); err != nil {
			t.Fatal(err)
		}
		payload := make([]byte, MaxThumbnailBytes)
		if i == 0 {
			payload = body
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO thumbnail_blobs VALUES (?,?,?,?,?)", owner.id, hash, payload, 80, 80); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO article_thumbnails VALUES (?,?,?)", owner.id, id, hash); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	newBody := thumbnailJPEG(t, 200)
	blocked := storeArticle(t, s, owner, "over-budget", newBody)
	if blocked.Article.ThumbnailStatus != "budget_exceeded" || blocked.Article.ThumbnailHash != "" || countThumbnails(t, s, owner) != 512 {
		t.Fatal("full retained Trash budget was not enforced")
	}
	shared := storeArticle(t, s, owner, "shared-at-limit", body)
	if shared.Article.ThumbnailStatus != "stored" || shared.Article.ThumbnailHash != sharedHash || countThumbnails(t, s, owner) != 512 {
		t.Fatal("shared payload consumed budget twice")
	}
	if out := storeArticle(t, s, other, "independent", newBody); out.Article.ThumbnailStatus != "stored" {
		t.Fatal("one owner's budget blocked another owner")
	}
	// Restore then replace one uniquely referenced full-size image. Accounting
	// must credit its released bytes before considering the new payload.
	restored, err := s.MutateArticle(t.Context(), owner, "restore-fixture", "restore_article", []byte("restore-fixture"), Article{ID: "budget-1"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	a := restored.Article
	out, err := s.MutateArticle(t.Context(), owner, "replace-at-limit", "patch_article", []byte("replace-at-limit"), a, a.Revision, ThumbnailChange{Replace: true, JPEG: newBody})
	if err != nil || out.Article.ThumbnailStatus != "stored" {
		t.Fatalf("replacement failed to credit old payload: %v", err)
	}
	var used int64
	if err := s.read.QueryRowContext(t.Context(), "SELECT sum(length(jpeg)) FROM thumbnail_blobs WHERE owner_id=?", owner.id).Scan(&used); err != nil || used > ThumbnailBudgetBytes {
		t.Fatal("owner payload budget exceeded")
	}
	// An exact retry retains its original soft failure after capacity changes.
	a = Article{URL: "https://example.com/over-budget", Title: "Source title", Summary: "Source introduction", ThumbnailURL: "https://images.example.com/source.jpg", ContentMarkdown: "# Original source"}
	payload, _ := json.Marshal(a)
	if replay, err := s.MutateArticle(t.Context(), owner, "over-budget", "add_article", payload, a, 0, ThumbnailChange{Replace: true, JPEG: newBody}); err != nil || replay != blocked {
		t.Fatal("capacity change altered durable budget outcome")
	}
}

func TestV2MigrationPreservesBodiesArchiveURLsAndRetryBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO accounts VALUES ('legacy',1,NULL);
 INSERT INTO identities VALUES ('https://issuer.example','subject','legacy');
 INSERT INTO articles(owner_id,id,canonical_url,title,summary,thumbnail_url,revision,created_at,updated_at,content_markdown,archived_at)
 VALUES ('legacy','article','https://example.com/old','Original','Introduction','https://images.example.com/old.jpg',2,1,2,'# Full source',2)`); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"id":"","url":"https://example.com/old","title":"Original","summary":"Introduction","thumbnail_url":"https://images.example.com/old.jpg","revision":0,"created_at":0,"updated_at":0,"content_markdown":"# Full source"}`)
	hash := sha256.Sum256(payload)
	result := []byte(`{"article":{"id":"article","url":"https://example.com/old","title":"Original","summary":"Introduction","thumbnail_url":"https://images.example.com/old.jpg","revision":1,"created_at":1,"updated_at":1,"content_markdown":"# Full source"},"created":true}`)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO idempotency_results VALUES ('legacy','saved','add_article',?,?,1)", hash[:], result); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner, err := s.OwnerForIdentity(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Article(t.Context(), owner, "article")
	if err != nil || a.ContentMarkdown != "# Full source" || a.ArchivedAt != 2 || a.TrashedAt != 0 || a.ThumbnailStatus != "legacy" || a.ThumbnailHash != "" {
		t.Fatalf("migration changed existing source/state: %#v %v", a, err)
	}
	var gotHash, gotResult []byte
	if err := s.read.QueryRowContext(t.Context(), "SELECT payload_hash,result FROM idempotency_results WHERE key='saved'").Scan(&gotHash, &gotResult); err != nil || !bytes.Equal(gotHash, hash[:]) || !bytes.Equal(gotResult, result) {
		t.Fatal("migration rewrote retry bytes/hash")
	}
	var expected MutationResult
	if err := json.Unmarshal(result, &expected); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.MutateArticle(t.Context(), owner, "saved", "add_article", payload, Article{}, 0); err != nil || replay != expected {
		t.Fatal("v2 durable retry lost")
	}
	if countThumbnails(t, s, owner) != 0 {
		t.Fatal("migration created thumbnail payloads")
	}
}
