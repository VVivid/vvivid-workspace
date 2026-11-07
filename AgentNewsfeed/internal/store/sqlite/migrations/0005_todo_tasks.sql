CREATE TABLE todo_tasks (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL CHECK (length(id) BETWEEN 1 AND 64),
    title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 180),
    tags_json TEXT NOT NULL CHECK (json_valid(tags_json) AND json_type(tags_json) = 'array'),
    estimate_minutes INTEGER NOT NULL CHECK (estimate_minutes IN (0, 15, 30, 45, 60, 90, 120, 180, 240, 360, 480)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    completed_at INTEGER,
    removal_state TEXT NOT NULL DEFAULT 'visible' CHECK (removal_state IN ('visible', 'undoable', 'removed')),
    PRIMARY KEY (owner_id, id)
) STRICT;

CREATE INDEX todo_tasks_by_owner ON todo_tasks(owner_id, completed_at DESC, created_at DESC, id DESC)
    WHERE removal_state != 'removed';
CREATE UNIQUE INDEX todo_tasks_one_undo ON todo_tasks(owner_id) WHERE removal_state = 'undoable';
