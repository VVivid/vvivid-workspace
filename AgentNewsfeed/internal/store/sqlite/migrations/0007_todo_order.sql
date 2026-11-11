-- Give existing tasks their previous newest-first order without changing task
-- revisions, completion/removal states, or historical retry snapshots.
ALTER TABLE todo_tasks ADD COLUMN sort_position INTEGER NOT NULL DEFAULT 0;

WITH ordered AS (
    SELECT owner_id, id, row_number() OVER (
        PARTITION BY owner_id ORDER BY created_at DESC, id DESC
    ) AS position
    FROM todo_tasks
)
UPDATE todo_tasks SET sort_position = (
    SELECT position FROM ordered
    WHERE ordered.owner_id = todo_tasks.owner_id AND ordered.id = todo_tasks.id
);

CREATE INDEX todo_tasks_visible_by_owner_position ON todo_tasks(owner_id, sort_position, id)
    WHERE removal_state = 'visible';
