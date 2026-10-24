package sqlite

import (
	"bytes"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	driversqlite "modernc.org/sqlite"
)

func TestSQLiteFullRollsBackAcceptanceAndRetainsPageBoundOnReconnect(t *testing.T) {
	s, clock, _, _ := openTest(t)
	var pages int64
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA max_page_count = " + strconv.FormatInt(pages+2, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TABLE capacity_probe(value BLOB)"); err != nil {
		t.Fatal(err)
	}
	_, err := s.db.Exec("INSERT INTO capacity_probe VALUES (zeroblob(1048576))")
	full, ok := errors.AsType[*driversqlite.Error](err)
	if !ok || full.Code() != 13 {
		t.Fatalf("expected actual SQLITE_FULL, got %v", err)
	}
	request := requestAt(clock.Now(), "full", 60, 60)
	request.Original = append([]byte(`{"padding":"`), bytes.Repeat([]byte{'x'}, 1<<20)...)
	request.Original = append(request.Original, []byte(`"}`)...)
	if _, err := s.Accept(t.Context(), request); err == nil {
		t.Fatal("capacity exhaustion accepted")
	}
	assertEmpty(t, s)
	// The trusted runtime option also belongs in the DSN, so closing an idle
	// connection cannot silently remove the database capacity bound.
	other, err := Open(filepath.Join(t.TempDir(), "bounded.db"), bytes.Repeat([]byte{4}, 32), WithMaxPageCount(128))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.db.SetMaxIdleConns(0)
	var limit int64
	if err := other.db.QueryRow("PRAGMA max_page_count").Scan(&limit); err != nil || limit != 128 {
		t.Fatalf("reconnected page limit=%d err=%v", limit, err)
	}
}
