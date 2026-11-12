package store

import "fmt"

// New versions must add explicit migrations here. A newer database is never
// opened by older code; schema and user_version change in one transaction.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("database schema %d is newer than this application", version)
	}
	if version == 0 {
		if _, err = tx.Exec(schemaV1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const schemaV1 = `
CREATE TABLE owners(id TEXT PRIMARY KEY,issuer TEXT NOT NULL,subject TEXT NOT NULL,label TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),UNIQUE(issuer,subject));
CREATE TABLE words(id TEXT PRIMARY KEY,owner TEXT NOT NULL REFERENCES owners(id),term TEXT NOT NULL,term_key TEXT NOT NULL,original_sentence TEXT NOT NULL,source_url TEXT NOT NULL,personal_note TEXT NOT NULL,senses TEXT NOT NULL DEFAULT '[]',status TEXT NOT NULL CHECK(status IN('needs_details','complete')),revision INTEGER NOT NULL CHECK(revision>0),created_at TEXT NOT NULL,updated_at TEXT NOT NULL,UNIQUE(owner,term_key));
CREATE INDEX words_owner_term ON words(owner,term_key,id);
CREATE INDEX words_owner_status ON words(owner,status,term_key,id);
CREATE TABLE idempotency(owner TEXT NOT NULL REFERENCES owners(id),operation TEXT NOT NULL,key TEXT NOT NULL,request_hash TEXT NOT NULL,result BLOB NOT NULL,PRIMARY KEY(owner,operation,key));
CREATE TABLE sessions(hash BLOB PRIMARY KEY,owner TEXT NOT NULL REFERENCES owners(id),host TEXT NOT NULL,cipher BLOB NOT NULL,expires_at INTEGER NOT NULL);
CREATE TABLE logins(hash BLOB PRIMARY KEY,host TEXT NOT NULL,cipher BLOB NOT NULL,expires_at INTEGER NOT NULL);
PRAGMA user_version=1;
`
