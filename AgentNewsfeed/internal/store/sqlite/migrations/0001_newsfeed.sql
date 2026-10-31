CREATE TABLE accounts (
    id TEXT PRIMARY KEY NOT NULL,
    created_at INTEGER NOT NULL,
    disabled_at INTEGER
) STRICT;

CREATE TABLE identities (
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    owner_id TEXT NOT NULL UNIQUE REFERENCES accounts(id),
    PRIMARY KEY (issuer, subject)
) STRICT;

CREATE TABLE articles (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    summary TEXT NOT NULL CHECK (length(summary) BETWEEN 1 AND 1200),
    thumbnail_url TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, id),
    UNIQUE (owner_id, canonical_url)
) STRICT;
CREATE INDEX articles_by_owner_time ON articles(owner_id, created_at DESC, id DESC);

CREATE TABLE idempotency_results (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    key TEXT NOT NULL,
    operation TEXT NOT NULL,
    payload_hash BLOB NOT NULL CHECK (length(payload_hash) = 32),
    result BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, key)
) STRICT;

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

CREATE TABLE login_attempts (
    token_hash BLOB PRIMARY KEY NOT NULL CHECK (length(token_hash) = 32),
    encrypted_state BLOB NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;
