CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE runs (
 id INTEGER PRIMARY KEY, command TEXT NOT NULL, started_at TEXT NOT NULL,
 finished_at TEXT, status TEXT NOT NULL, error_code TEXT NOT NULL DEFAULT '',
 completed_windows INTEGER NOT NULL DEFAULT 0, unavailable_windows INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE windows (
 id TEXT PRIMARY KEY, stream TEXT NOT NULL, since_date TEXT NOT NULL, until_date TEXT NOT NULL,
 state TEXT NOT NULL, attempt_run INTEGER REFERENCES runs(id), next_page_token TEXT NOT NULL DEFAULT '',
 pages INTEGER NOT NULL DEFAULT 0, success_at TEXT, record_count INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT '', UNIQUE(stream, since_date, until_date)
);
CREATE INDEX windows_stream_coverage ON windows(stream, since_date, until_date, state);
CREATE TABLE records (
 stream TEXT NOT NULL, record_key TEXT NOT NULL, data_type TEXT NOT NULL, kind TEXT NOT NULL,
 anchor TEXT NOT NULL, start_time TEXT NOT NULL, end_time TEXT NOT NULL,
 observation_date TEXT NOT NULL, source_json TEXT NOT NULL, source_family TEXT NOT NULL,
 original_json TEXT NOT NULL, has_value INTEGER NOT NULL,
 imported_at TEXT NOT NULL, PRIMARY KEY(stream, record_key)
);
CREATE INDEX records_type_time ON records(data_type, start_time, end_time);
CREATE INDEX records_stream_anchor ON records(stream, anchor);
CREATE INDEX records_date ON records(observation_date, data_type);
CREATE INDEX records_source ON records(source_json, data_type);
CREATE TABLE staged_records (
 window_id TEXT NOT NULL REFERENCES windows(id) ON DELETE CASCADE,
 record_key TEXT NOT NULL, anchor TEXT NOT NULL, start_time TEXT NOT NULL,
 end_time TEXT NOT NULL, observation_date TEXT NOT NULL, source_json TEXT NOT NULL,
 original_json TEXT NOT NULL, has_value INTEGER NOT NULL, PRIMARY KEY(window_id, record_key)
);
CREATE TABLE page_tokens (
 window_id TEXT NOT NULL REFERENCES windows(id) ON DELETE CASCADE,
 token_hash TEXT NOT NULL, PRIMARY KEY(window_id, token_hash)
);
CREATE TABLE stream_state (stream TEXT PRIMARY KEY, sync_until TEXT NOT NULL);
PRAGMA user_version=1;
