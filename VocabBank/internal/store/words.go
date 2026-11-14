package store

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func (s *Store) authorize(ctx context.Context, a Actor, scope string) error {
	if err := a.Require(scope); err != nil {
		return err
	}
	var enabled int
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM owners WHERE id=?`, a.Owner).Scan(&enabled); err != nil || enabled != 1 {
		return ErrForbidden
	}
	return nil
}

type scanner interface{ Scan(...any) error }

const wordColumns = `id,term,original_sentence,source_url,personal_note,senses,status,revision,created_at,updated_at`

func scanWord(row scanner) (Word, error) {
	var word Word
	var senses string
	err := row.Scan(&word.ID, &word.Term, &word.OriginalSentence, &word.SourceURL, &word.PersonalNote, &senses, &word.Status, &word.Revision, &word.CreatedAt, &word.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return word, ErrNotFound
	}
	if err != nil {
		return word, err
	}
	err = json.Unmarshal([]byte(senses), &word.Senses)
	return word, err
}
func (s *Store) Word(ctx context.Context, a Actor, id string) (Word, error) {
	if err := s.authorize(ctx, a, Read); err != nil {
		return Word{}, err
	}
	if !recordID(id) {
		return Word{}, ErrInvalid
	}
	return scanWord(s.db.QueryRowContext(ctx, `SELECT `+wordColumns+` FROM words WHERE owner=? AND id=?`, a.Owner, id))
}
func (s *Store) Words(ctx context.Context, a Actor, f Filter) (Page, error) {
	if err := s.authorize(ctx, a, Read); err != nil {
		return Page{}, err
	}
	if err := normalizeFilter(&f); err != nil {
		return Page{}, err
	}
	result := Page{Items: []Word{}, Page: f.Page, PageSize: f.PageSize}
	// instr treats %, _ and backslashes literally. Parameters never become SQL.
	where := ` FROM words WHERE owner=? AND instr(term_key,?)>0 AND (?='' OR status=?)`
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT count(*)`+where, a.Owner, f.Query, f.Status, f.Status).Scan(&result.Total); err != nil {
		return result, err
	}
	if f.ShuffleSeed != 0 {
		result.Items, err = shuffledPage(ctx, tx, where, a.Owner, f)
	} else {
		result.Items, err = queryWords(ctx, tx, `SELECT `+wordColumns+where+` ORDER BY term_key,id LIMIT ? OFFSET ?`, a.Owner, f.Query, f.Status, f.Status, f.PageSize, (f.Page-1)*f.PageSize)
	}
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// shuffledPage returns one page of the matching words in the order fixed by
// f.ShuffleSeed. Every page of the same seed sorts the full set of matching
// IDs identically, so paging never repeats or skips a word. Only IDs are read
// for the whole set; full rows are loaded for the requested page alone.
func shuffledPage(ctx context.Context, tx *sql.Tx, where, owner string, f Filter) ([]Word, error) {
	orderedIDs, err := shuffledIDs(ctx, tx, where, owner, f)
	if err != nil {
		return nil, err
	}
	start := min((f.Page-1)*f.PageSize, len(orderedIDs))
	pageIDs := orderedIDs[start:min(start+f.PageSize, len(orderedIDs))]
	if len(pageIDs) == 0 {
		return []Word{}, nil
	}
	// SQLite returns IN (...) rows in its own order; restore the shuffled one.
	args := []any{owner}
	position := make(map[string]int, len(pageIDs))
	for i, id := range pageIDs {
		args = append(args, id)
		position[id] = i
	}
	placeholders := strings.Repeat(",?", len(pageIDs))[1:]
	words, err := queryWords(ctx, tx, `SELECT `+wordColumns+` FROM words WHERE owner=? AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(words, func(x, y Word) int { return cmp.Compare(position[x.ID], position[y.ID]) })
	return words, nil
}

// shuffledIDs returns the IDs of every word matching the filter, sorted by
// their seeded rank with the ID as a tie-breaker.
func shuffledIDs(ctx context.Context, tx *sql.Tx, where, owner string, f Filter) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id`+where, owner, f.Query, f.Status, f.Status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type rankedID struct {
		id   string
		rank uint64
	}
	var ranked []rankedID
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ranked = append(ranked, rankedID{id, shuffleRank(f.ShuffleSeed, id)})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(ranked, func(x, y rankedID) int {
		return cmp.Or(cmp.Compare(x.rank, y.rank), cmp.Compare(x.id, y.id))
	})
	ids := make([]string, len(ranked))
	for i, r := range ranked {
		ids[i] = r.id
	}
	return ids, nil
}

// shuffleRank gives a word a stable pseudo-random position for one seed. It
// only orders the owner's own words for display and is not a security control.
func shuffleRank(seed int, id string) uint64 {
	h := fnv.New64a()
	_ = binary.Write(h, binary.BigEndian, uint32(seed))
	h.Write([]byte(id))
	return h.Sum64()
}

// queryWords scans every row of a wordColumns query; it never returns nil items.
func queryWords(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]Word, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	words := []Word{}
	for rows.Next() {
		word, err := scanWord(rows)
		if err != nil {
			return nil, err
		}
		words = append(words, word)
	}
	return words, rows.Err()
}

// write replays an exact successful intent before testing its old revision.
// The request hash is computed before term normalization: changing any input
// under the same owner/operation/key is a conflict, even after later edits.
func (s *Store) write(ctx context.Context, a Actor, operation, key string, input any, apply func(*sql.Tx) (Word, error)) (Word, error) {
	if err := s.authorize(ctx, a, Write); err != nil {
		return Word{}, err
	}
	if !keyValid(key) {
		return Word{}, ErrInvalid
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Word{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Word{}, err
	}
	defer tx.Rollback()
	var previousHash string
	var result []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash,result FROM idempotency WHERE owner=? AND operation=? AND key=?`, a.Owner, operation, key).Scan(&previousHash, &result)
	if err == nil {
		if previousHash != digest {
			return Word{}, ErrRetryKey
		}
		var word Word
		err = json.Unmarshal(result, &word)
		return word, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Word{}, err
	}
	word, err := apply(tx)
	if err != nil {
		return Word{}, err
	}
	result, err = json.Marshal(word)
	if err != nil {
		return Word{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO idempotency(owner,operation,key,request_hash,result)VALUES(?,?,?,?,?)`, a.Owner, operation, key, digest, result)
	if err != nil {
		return Word{}, err
	}
	if err = tx.Commit(); err != nil {
		return Word{}, err
	}
	return word, nil
}
func duplicate(ctx context.Context, tx *sql.Tx, owner, key, except string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM words WHERE owner=? AND term_key=? AND id<>?`, owner, key, except).Scan(&id)
	if err == nil {
		return ErrDuplicate
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
func (s *Store) AddWord(ctx context.Context, a Actor, in AddInput) (Word, error) {
	if err := validateCapture(in.Capture); err != nil {
		return Word{}, err
	}
	return s.write(ctx, a, "add_word", in.IdempotencyKey, in, func(tx *sql.Tx) (Word, error) {
		return s.insertWord(ctx, tx, a.Owner, in.Capture, nil)
	})
}

// AddWords validates the envelope before any write, then commits each item and
// its retry snapshot in a separate transaction. Batch order is not part of an
// item's intent: its owner, add_words operation, and key identify exact retries.
func (s *Store) AddWords(ctx context.Context, a Actor, in AddWordsInput) ([]AddWordsItemResult, error) {
	if err := s.authorize(ctx, a, Write); err != nil {
		return nil, err
	}
	if len(in.Words) < 1 || len(in.Words) > 100 {
		return nil, fmt.Errorf("%w: provide 1–100 words", ErrInvalid)
	}
	results := make([]AddWordsItemResult, len(in.Words))
	for i, item := range in.Words {
		if err := validateCapture(item.Capture); err != nil {
			results[i].Err = err
			continue
		}
		if len(item.Senses) > 0 {
			if err := validateSenses(item.Senses); err != nil {
				results[i].Err = err
				continue
			}
		}
		results[i].Word, results[i].Err = s.write(ctx, a, "add_words", item.IdempotencyKey, item, func(tx *sql.Tx) (Word, error) {
			return s.insertWord(ctx, tx, a.Owner, item.Capture, item.Senses)
		})
	}
	return results, nil
}

// DeleteWords removes only explicit owned IDs at their expected revisions.
// Each deletion commits with its retry snapshot; replay happens before looking
// for the deleted ID, so an old retry cannot remove a later same-term creation.
func (s *Store) DeleteWords(ctx context.Context, a Actor, in DeleteWordsInput) ([]DeleteWordsItemResult, error) {
	if err := s.authorize(ctx, a, Write); err != nil {
		return nil, err
	}
	if len(in.Words) < 1 || len(in.Words) > 100 {
		return nil, fmt.Errorf("%w: provide 1–100 words", ErrInvalid)
	}
	results := make([]DeleteWordsItemResult, len(in.Words))
	for i, item := range in.Words {
		if !recordID(item.ID) || item.ExpectedRevision < 1 {
			results[i].Err = ErrInvalid
			continue
		}
		results[i].DeletedWord, results[i].Err = s.write(ctx, a, "delete_words", item.IdempotencyKey, item, func(tx *sql.Tx) (Word, error) {
			word, err := scanWord(tx.QueryRowContext(ctx, `SELECT `+wordColumns+` FROM words WHERE owner=? AND id=?`, a.Owner, item.ID))
			if err != nil {
				return Word{}, err
			}
			if word.Revision != item.ExpectedRevision {
				return Word{}, ErrStale
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM words WHERE owner=? AND id=?`, a.Owner, word.ID)
			return word, err
		})
	}
	return results, nil
}

// insertWord creates the final state directly; complete batch entries never
// exist as drafts, and both creation paths keep revision 1 and the same captures.
func (s *Store) insertWord(ctx context.Context, tx *sql.Tx, owner string, capture Capture, senses []Sense) (Word, error) {
	term := normalizedTerm(capture.Term)
	if err := duplicate(ctx, tx, owner, termKey(term), ""); err != nil {
		return Word{}, err
	}
	status := Complete
	if len(senses) == 0 {
		senses = []Sense{}
		status = NeedsDetails
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	word := Word{ID: ID(), Term: term, OriginalSentence: capture.OriginalSentence, SourceURL: capture.SourceURL, PersonalNote: capture.PersonalNote, Senses: senses, Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now}
	raw, err := json.Marshal(senses)
	if err != nil {
		return Word{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO words(id,owner,term,term_key,original_sentence,source_url,personal_note,senses,status,revision,created_at,updated_at)VALUES(?,?,?,?,?,?,?,?,?,1,?,?)`, word.ID, owner, word.Term, termKey(term), word.OriginalSentence, word.SourceURL, word.PersonalNote, string(raw), word.Status, now, now)
	return word, err
}

func (s *Store) UpdateWord(ctx context.Context, a Actor, in UpdateInput) (Word, error) {
	if err := validateCapture(in.Capture); err != nil {
		return Word{}, err
	}
	if !recordID(in.ID) || in.ExpectedRevision < 1 {
		return Word{}, ErrInvalid
	}
	return s.write(ctx, a, "update_word", in.IdempotencyKey, in, func(tx *sql.Tx) (Word, error) {
		word, err := scanWord(tx.QueryRowContext(ctx, `SELECT `+wordColumns+` FROM words WHERE owner=? AND id=?`, a.Owner, in.ID))
		if err != nil {
			return Word{}, err
		}
		if word.Revision != in.ExpectedRevision {
			return Word{}, ErrStale
		}
		if err = duplicate(ctx, tx, a.Owner, termKey(in.Term), word.ID); err != nil {
			return Word{}, err
		}
		// A case/spacing correction keeps senses; changing the actual term
		// clears content about the old term rather than falsely marking it complete.
		if termKey(word.Term) != termKey(in.Term) {
			word.Senses = []Sense{}
			word.Status = NeedsDetails
		}
		word.Term = normalizedTerm(in.Term)
		word.OriginalSentence = in.OriginalSentence
		word.SourceURL = in.SourceURL
		word.PersonalNote = in.PersonalNote
		word.Revision++
		word.UpdatedAt = s.now().UTC().Format(time.RFC3339Nano)
		senses, err := json.Marshal(word.Senses)
		if err != nil {
			return Word{}, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE words SET term=?,term_key=?,original_sentence=?,source_url=?,personal_note=?,senses=?,status=?,revision=?,updated_at=? WHERE owner=? AND id=?`, word.Term, termKey(word.Term), word.OriginalSentence, word.SourceURL, word.PersonalNote, string(senses), word.Status, word.Revision, word.UpdatedAt, a.Owner, word.ID)
		return word, err
	})
}
func (s *Store) CompleteWord(ctx context.Context, a Actor, in CompleteInput) (Word, error) {
	if !recordID(in.ID) || in.ExpectedRevision < 1 {
		return Word{}, ErrInvalid
	}
	if err := validateSenses(in.Senses); err != nil {
		return Word{}, err
	}
	return s.write(ctx, a, "complete_word", in.IdempotencyKey, in, func(tx *sql.Tx) (Word, error) {
		word, err := scanWord(tx.QueryRowContext(ctx, `SELECT `+wordColumns+` FROM words WHERE owner=? AND id=?`, a.Owner, in.ID))
		if err != nil {
			return Word{}, err
		}
		if word.Revision != in.ExpectedRevision {
			return Word{}, ErrStale
		}
		word.Senses = in.Senses
		word.Status = Complete
		word.Revision++
		word.UpdatedAt = s.now().UTC().Format(time.RFC3339Nano)
		senses, err := json.Marshal(in.Senses)
		if err != nil {
			return Word{}, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE words SET senses=?,status=?,revision=?,updated_at=? WHERE owner=? AND id=?`, string(senses), word.Status, word.Revision, word.UpdatedAt, a.Owner, word.ID)
		return word, err
	})
}
