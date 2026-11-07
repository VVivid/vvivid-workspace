// Package sqlite implements memory.Store using one SQLite writer and per-space
// journal tables. It accepts trusted internal account IDs, not external claims.
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
	_ "modernc.org/sqlite"
)

const instantFormat = "2006-01-02T15:04:05.000000000Z"

type Store struct {
	db        *sql.DB
	cursorKey []byte
}

var _ memory.Store = (*Store)(nil)

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("%w: a disk database path is required, not a SQLite URI", memory.ErrInvalid)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else if !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("database must be a regular private file (0600 or stricter)")
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	for _, pragma := range []string{"foreign_keys(1)", "recursive_triggers(1)", "busy_timeout(5000)", "synchronous(FULL)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// A single connection also serializes concurrent remembers before replay
	// lookup. Readers use a transaction for a consistent per-call snapshot.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open journal: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	} // crypto/rand guarantees success.
	return hex.EncodeToString(id[:])
}

var physicalTable = regexp.MustCompile(`^memory_[0-9a-f]{32}$`)

func quotedTable(name string) (string, error) {
	if !physicalTable.MatchString(name) {
		return "", fmt.Errorf("invalid stored physical table identifier")
	}
	return `"` + name + `"`, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func formatInstant(t time.Time) string { return t.UTC().Format(instantFormat) }

func (s *Store) ListSpaces(ctx context.Context, account string) ([]memory.Space, error) {
	if err := validIdentity(account); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT name, created_at FROM spaces WHERE account_id=? ORDER BY name LIMIT ?", account, memory.MaxSpaces+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	spaces := []memory.Space{}
	for rows.Next() {
		var name, created string
		if err := rows.Scan(&name, &created); err != nil {
			return nil, err
		}
		at, err := time.Parse(instantFormat, created)
		if err != nil {
			return nil, err
		}
		spaces = append(spaces, memory.Space{Name: name, CreatedAt: at})
	}
	if len(spaces) > memory.MaxSpaces {
		return nil, fmt.Errorf("space limit exceeded in database")
	}
	return spaces, rows.Err()
}
