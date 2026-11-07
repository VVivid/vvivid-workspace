-- Preserve all existing article, opportunity and browser draft retry records.
CREATE TABLE todo_idempotency_results (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    key TEXT NOT NULL,
    operation TEXT NOT NULL,
    payload_hash BLOB NOT NULL CHECK (length(payload_hash) = 32),
    result BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, key)
) STRICT;

CREATE INDEX todo_tasks_visible_by_owner_created ON todo_tasks(owner_id, created_at DESC, id DESC)
    WHERE removal_state = 'visible';
