package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

const schema = `
CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK(id=1), cursor_key BLOB NOT NULL CHECK(length(cursor_key)=32));
CREATE TABLE spaces (
 id TEXT PRIMARY KEY, account_id TEXT NOT NULL, name TEXT NOT NULL,
 table_name TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL,
 UNIQUE(account_id,name), UNIQUE(account_id,id)
);
CREATE TABLE nodes (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 span_id TEXT NOT NULL UNIQUE, account_id TEXT NOT NULL, space_id TEXT NOT NULL,
 topic_id TEXT NOT NULL, parent_id TEXT, kind TEXT NOT NULL,
 external_ref TEXT NOT NULL, occurred_at TEXT, recorded_at TEXT NOT NULL,
 UNIQUE(account_id,span_id),
 FOREIGN KEY(account_id,space_id) REFERENCES spaces(account_id,id),
 FOREIGN KEY(account_id,topic_id) REFERENCES nodes(account_id,span_id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(account_id,parent_id) REFERENCES nodes(account_id,span_id),
 CHECK(kind IN ('topic','context','proposal','decision','note','retraction')),
 CHECK((kind='topic' AND span_id=topic_id AND parent_id IS NULL) OR
       (kind!='topic' AND span_id!=topic_id AND parent_id IS NOT NULL AND external_ref=''))
);
CREATE INDEX nodes_account_seq ON nodes(account_id,seq);
CREATE INDEX nodes_external ON nodes(account_id,space_id,external_ref) WHERE kind='topic';
CREATE INDEX nodes_topic_time ON nodes(account_id,topic_id,occurred_at,seq);
CREATE INDEX nodes_parent ON nodes(account_id,parent_id,seq);
CREATE INDEX nodes_kind_time ON nodes(account_id,kind,occurred_at,seq);
CREATE INDEX nodes_recorded ON nodes(account_id,recorded_at,seq);
CREATE TRIGGER nodes_parent_valid BEFORE INSERT ON nodes WHEN NEW.parent_id IS NOT NULL BEGIN
 SELECT RAISE(ABORT,'parent must be an earlier node in the same topic and space') WHERE NOT EXISTS(
  SELECT 1 FROM nodes WHERE account_id=NEW.account_id AND span_id=NEW.parent_id
   AND topic_id=NEW.topic_id AND space_id=NEW.space_id
 );
END;
CREATE TABLE tags (
 account_id TEXT NOT NULL, span_id TEXT NOT NULL, tag TEXT NOT NULL,
 PRIMARY KEY(account_id,tag,span_id),
 FOREIGN KEY(account_id,span_id) REFERENCES nodes(account_id,span_id)
);
CREATE INDEX tags_node ON tags(account_id,span_id,tag);
CREATE TABLE links (
 account_id TEXT NOT NULL, source_id TEXT NOT NULL, type TEXT NOT NULL, target_id TEXT NOT NULL,
 PRIMARY KEY(account_id,source_id,type,target_id),
 FOREIGN KEY(account_id,source_id) REFERENCES nodes(account_id,span_id),
 FOREIGN KEY(account_id,target_id) REFERENCES nodes(account_id,span_id),
 CHECK(source_id!=target_id),
 CHECK(type IN ('depends_on','related_to','supersedes','corrects','retracts'))
);
CREATE INDEX links_incoming ON links(account_id,target_id,type,source_id);
CREATE TRIGGER links_earlier BEFORE INSERT ON links BEGIN
 SELECT RAISE(ABORT,'link target must be earlier') WHERE
 (SELECT seq FROM nodes WHERE span_id=NEW.target_id) >= (SELECT seq FROM nodes WHERE span_id=NEW.source_id);
END;
CREATE TABLE idempotency (
 account_id TEXT NOT NULL, request_key TEXT NOT NULL, canonical_request BLOB NOT NULL,
 result BLOB NOT NULL, PRIMARY KEY(account_id,request_key)
);
CREATE VIRTUAL TABLE search USING fts5(span_id UNINDEXED, account_id UNINDEXED, title, context, reasoning, external_ref,
 tokenize='unicode61');
`

func (s *Store) migrate(ctx context.Context) error {
	const applicationID = 0x414d454d // AMEM; do not adopt another application's database.
	var version, appID int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return err
	}
	if version == 0 && appID == 0 {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("refusing to adopt an existing non-journal database")
		}
	} else if appID != applicationID || version != 1 {
		return fmt.Errorf("unsupported journal application/schema %x/%d", appID, version)
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return fmt.Errorf("SQLite WAL mode unavailable: %s", mode)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version == 0 {
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings(id,cursor_key) VALUES (1,randomblob(32))"); err != nil {
			return err
		}
		for _, table := range []string{"settings", "spaces", "nodes", "tags", "links", "idempotency"} {
			if err := immutable(ctx, tx, table); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA application_id=1095583053; PRAGMA user_version=1"); err != nil {
			return err
		}
	}
	if err := tx.QueryRowContext(ctx, "SELECT cursor_key FROM settings WHERE id=1").Scan(&s.cursorKey); err != nil {
		return err
	}
	if len(s.cursorKey) != 32 {
		return fmt.Errorf("invalid persisted continuation key")
	}
	return tx.Commit()
}

// Names passed here are constants or server-generated, validated table names.
func immutable(ctx context.Context, tx *sql.Tx, table string) error {
	for _, action := range []string{"UPDATE", "DELETE"} {
		query := fmt.Sprintf(`CREATE TRIGGER "%s_no_%s" BEFORE %s ON "%s" BEGIN SELECT RAISE(ABORT,'immutable journal'); END`, table, action, action, table)
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func createArea(ctx context.Context, tx *sql.Tx, table string) error {
	quoted, err := quotedTable(table)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`CREATE TABLE %s (
 seq INTEGER NOT NULL UNIQUE REFERENCES nodes(seq),
 span_id TEXT PRIMARY KEY REFERENCES nodes(span_id),
 topic_id TEXT NOT NULL REFERENCES nodes(span_id), parent_id TEXT REFERENCES nodes(span_id),
 kind TEXT NOT NULL, schema_version INTEGER NOT NULL CHECK(schema_version>0),
 title TEXT NOT NULL, context TEXT NOT NULL, external_ref TEXT NOT NULL,
 payload TEXT NOT NULL CHECK(json_valid(payload)),
 occurred_at TEXT, recorded_at TEXT NOT NULL,
 historical INTEGER NOT NULL, time_unknown INTEGER NOT NULL,
 provenance TEXT NOT NULL CHECK(json_valid(provenance)),
 account_id TEXT NOT NULL, client_id TEXT NOT NULL, request_key TEXT NOT NULL,
 FOREIGN KEY(account_id,span_id) REFERENCES nodes(account_id,span_id)
);
CREATE INDEX "%s_topic_time" ON %s(topic_id,occurred_at,seq);
CREATE INDEX "%s_parent" ON %s(parent_id,seq);
CREATE INDEX "%s_kind_time" ON %s(kind,occurred_at,seq);
CREATE INDEX "%s_recorded" ON %s(recorded_at,seq);`, quoted, table, quoted, table, quoted, table, quoted, table, quoted)
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return err
	}
	return immutable(ctx, tx, table)
}
