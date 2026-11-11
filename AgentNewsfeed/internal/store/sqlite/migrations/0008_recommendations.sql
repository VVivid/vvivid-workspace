-- Additive: existing content, order and durable retry bytes stay unchanged.
CREATE TABLE recommendation_preferences (
    owner_id TEXT PRIMARY KEY NOT NULL REFERENCES accounts(id),
    revision INTEGER NOT NULL CHECK (revision > 1),
    preferences BLOB NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE recommendation_suggestions (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    status TEXT NOT NULL CHECK (status IN ('pending','accepted','dismissed')),
    suggestion BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    resolved_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (owner_id,id)
) STRICT;
CREATE INDEX recommendation_suggestions_by_owner ON recommendation_suggestions(owner_id,created_at DESC,id DESC);
CREATE TABLE discovery_runs (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    hour_slot INTEGER NOT NULL,
    preferences_revision INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active','completed','failed','expired')),
    started_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0,
    saved_count INTEGER NOT NULL DEFAULT 0,
    note TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (owner_id,id),
    UNIQUE (owner_id,hour_slot)
) STRICT;
CREATE INDEX discovery_runs_by_owner ON discovery_runs(owner_id,started_at DESC);
CREATE TABLE discovery_articles (
    owner_id TEXT NOT NULL,
    article_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    source_url TEXT NOT NULL CHECK (length(source_url) BETWEEN 1 AND 2048),
    source_title TEXT NOT NULL CHECK (length(source_title) BETWEEN 1 AND 300),
    why_match TEXT NOT NULL,
    published_at INTEGER NOT NULL,
    evergreen INTEGER NOT NULL CHECK (evergreen IN (0,1)),
    saved_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id,article_id),
    FOREIGN KEY (owner_id,article_id) REFERENCES articles(owner_id,id),
    FOREIGN KEY (owner_id,run_id) REFERENCES discovery_runs(owner_id,id)
) STRICT;
CREATE INDEX discovery_articles_by_owner_saved ON discovery_articles(owner_id,saved_at);
CREATE TABLE recommendation_idempotency_results (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    key TEXT NOT NULL,
    operation TEXT NOT NULL,
    payload_hash BLOB NOT NULL CHECK (length(payload_hash) = 32),
    result BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id,key)
) STRICT;
