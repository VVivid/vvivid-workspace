-- Keep code hashes permanently, including expired shares: an old URL must never
-- resolve to a different owner's newer share.
CREATE TABLE temporary_shares (
    code_hash BLOB PRIMARY KEY NOT NULL CHECK (length(code_hash) = 32),
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL UNIQUE,
    retailer TEXT,
    write_token TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at = created_at + 21600000),
    UNIQUE (owner_id, id)
) STRICT;
CREATE INDEX temporary_shares_by_owner ON temporary_shares(owner_id, created_at);

CREATE TABLE shared_items (
    share_id TEXT NOT NULL REFERENCES temporary_shares(id),
    position INTEGER NOT NULL CHECK (position BETWEEN 1 AND 500),
    name TEXT NOT NULL,
    category TEXT NOT NULL CHECK (category IN ('produce','meat','drinks','pantry','dairy','other')),
    package_count INTEGER,
    package_size TEXT,
    package_unit TEXT,
    quantity TEXT NOT NULL,
    retailer TEXT,
    picked_up INTEGER NOT NULL DEFAULT 0 CHECK (picked_up IN (0,1)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    PRIMARY KEY (share_id, position)
) STRICT;
CREATE TABLE shared_item_results (
    share_id TEXT NOT NULL REFERENCES temporary_shares(id),
    key TEXT NOT NULL,
    payload_hash BLOB NOT NULL CHECK (length(payload_hash) = 32),
    result BLOB NOT NULL,
    PRIMARY KEY (share_id, key)
) STRICT;
-- Guest activity is explicitly anonymous. Never impersonate the owner's OAuth
-- identity in the personal domain audit log.
CREATE TABLE shared_item_events (
    share_id TEXT NOT NULL REFERENCES temporary_shares(id),
    id TEXT PRIMARY KEY NOT NULL,
    position INTEGER NOT NULL,
    before_picked_up INTEGER NOT NULL,
    after_picked_up INTEGER NOT NULL,
    revision INTEGER NOT NULL,
    request_id TEXT NOT NULL,
    occurred_at INTEGER NOT NULL
) STRICT;
CREATE TRIGGER shared_item_events_no_update BEFORE UPDATE ON shared_item_events
BEGIN SELECT RAISE(ABORT, 'shared event is immutable'); END;
CREATE TRIGGER shared_item_events_no_delete BEFORE DELETE ON shared_item_events
BEGIN SELECT RAISE(ABORT, 'shared event is immutable'); END;
