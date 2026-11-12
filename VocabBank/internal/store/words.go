package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	rows, err := tx.QueryContext(ctx, `SELECT `+wordColumns+where+` ORDER BY term_key,id LIMIT ? OFFSET ?`, a.Owner, f.Query, f.Status, f.Status, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		word, e := scanWord(rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		result.Items = append(result.Items, word)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
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
		term := normalizedTerm(in.Term)
		if err := duplicate(ctx, tx, a.Owner, termKey(term), ""); err != nil {
			return Word{}, err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		word := Word{ID: ID(), Term: term, OriginalSentence: in.OriginalSentence, SourceURL: in.SourceURL, PersonalNote: in.PersonalNote, Senses: []Sense{}, Status: NeedsDetails, Revision: 1, CreatedAt: now, UpdatedAt: now}
		_, err := tx.ExecContext(ctx, `INSERT INTO words(id,owner,term,term_key,original_sentence,source_url,personal_note,senses,status,revision,created_at,updated_at)VALUES(?,?,?,?,?,?,?,'[]',?,1,?,?)`, word.ID, a.Owner, word.Term, termKey(term), word.OriginalSentence, word.SourceURL, word.PersonalNote, word.Status, now, now)
		return word, err
	})
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
