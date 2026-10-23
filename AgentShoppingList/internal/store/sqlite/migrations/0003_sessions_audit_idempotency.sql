CREATE TABLE web_sessions (
    token_hash BLOB PRIMARY KEY NOT NULL CHECK (length(token_hash) = 32),
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    client_id TEXT NOT NULL,
    encrypted_state BLOB NOT NULL,
    csrf_hash BLOB NOT NULL CHECK (length(csrf_hash) = 32),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER,
    CHECK (expires_at > created_at)
) STRICT;

CREATE INDEX web_sessions_by_owner ON web_sessions(owner_id, expires_at);

CREATE TABLE audit_events (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    actor_issuer TEXT NOT NULL,
    actor_subject TEXT NOT NULL,
    actor_client TEXT NOT NULL,
    operation TEXT NOT NULL,
    affected_records_json TEXT NOT NULL CHECK (json_valid(affected_records_json)),
    request_id TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, id)
) STRICT;

CREATE INDEX audit_events_by_time ON audit_events(owner_id, occurred_at, id);

CREATE TRIGGER audit_events_no_update BEFORE UPDATE ON audit_events
BEGIN SELECT RAISE(ABORT, 'audit event is immutable'); END;

CREATE TRIGGER audit_events_no_delete BEFORE DELETE ON audit_events
BEGIN SELECT RAISE(ABORT, 'audit event is immutable'); END;

CREATE TABLE idempotency_results (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    key TEXT NOT NULL,
    operation TEXT NOT NULL,
    payload_hash BLOB NOT NULL CHECK (length(payload_hash) = 32),
    result BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, key)
) STRICT;
