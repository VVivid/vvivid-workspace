CREATE TABLE opportunities (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    canonical_url TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('job', 'internship')),
    title TEXT NOT NULL CHECK (length(CAST(title AS BLOB)) BETWEEN 1 AND 300),
    company TEXT NOT NULL CHECK (length(CAST(company AS BLOB)) BETWEEN 1 AND 200),
    location TEXT NOT NULL DEFAULT '' CHECK (length(CAST(location AS BLOB)) <= 200),
    workplace TEXT NOT NULL DEFAULT '' CHECK (workplace IN ('', 'remote', 'hybrid', 'on_site')),
    employment_type TEXT NOT NULL DEFAULT '' CHECK (employment_type IN ('', 'full_time', 'part_time', 'contract', 'temporary', 'seasonal', 'other')),
    employment_period TEXT NOT NULL DEFAULT '' CHECK (length(CAST(employment_period AS BLOB)) <= 160),
    compensation TEXT NOT NULL DEFAULT '' CHECK (length(CAST(compensation AS BLOB)) <= 300),
    deadline TEXT NOT NULL DEFAULT '' CHECK (length(deadline) IN (0, 10)),
    tags_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags_json) AND json_type(tags_json) = 'array' AND json_array_length(tags_json) <= 12),
    source_description TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_description AS BLOB)) <= 65536),
    agent_note TEXT NOT NULL DEFAULT '' CHECK (length(CAST(agent_note AS BLOB)) <= 4096),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    archived_at INTEGER,
    trashed_at INTEGER,
    PRIMARY KEY (owner_id, id),
    UNIQUE (owner_id, canonical_url)
) STRICT;
CREATE INDEX opportunities_by_owner_saved ON opportunities(owner_id, created_at DESC, id DESC) WHERE archived_at IS NULL AND trashed_at IS NULL;
CREATE INDEX opportunities_by_owner_archive ON opportunities(owner_id, archived_at DESC, id DESC) WHERE archived_at IS NOT NULL AND trashed_at IS NULL;
CREATE INDEX opportunities_by_owner_trash ON opportunities(owner_id, trashed_at DESC, id DESC) WHERE trashed_at IS NOT NULL;

-- A separate retry namespace leaves every existing article retry untouched.
CREATE TABLE opportunity_idempotency_results (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    key TEXT NOT NULL,
    operation TEXT NOT NULL,
    payload_hash BLOB NOT NULL CHECK (length(payload_hash) = 32),
    result BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, key)
) STRICT;
