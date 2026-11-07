package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type TodoTask struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Tags            []string `json:"tags,omitempty"`
	EstimateMinutes int      `json:"estimate_minutes,omitzero"`
	Revision        int64    `json:"revision"`
	CompletedAt     int64    `json:"completed_at,omitzero"`
}

type TodoList struct {
	Tasks []TodoTask `json:"tasks"`
	Undo  *TodoTask  `json:"undo,omitempty"`
}

type TodoMutationResult struct {
	Task    TodoTask `json:"task"`
	Removed bool     `json:"removed"`
}

type TodoCursor struct {
	Collection string `json:"collection"`
	Owner      string `json:"owner"`
	Status     string `json:"status"`
	Timestamp  int64  `json:"before_created_at"`
	ID         string `json:"before_task_id"`
}

type TodoPage struct {
	Tasks []TodoTask
	Undo  *TodoTask
	Next  *TodoCursor
}

type todoRecord struct {
	task         TodoTask
	createdAt    int64
	completed    bool
	removalState string
}

const todoColumns = `id, title, tags_json, estimate_minutes, revision, completed_at, created_at, removal_state`

func scanTodo(row scanner) (todoRecord, error) {
	var record todoRecord
	var tagsJSON string
	var completedAt sql.NullInt64
	err := row.Scan(&record.task.ID, &record.task.Title, &tagsJSON, &record.task.EstimateMinutes,
		&record.task.Revision, &completedAt, &record.createdAt, &record.removalState)
	if errors.Is(err, sql.ErrNoRows) {
		return todoRecord{}, ErrNotFound
	}
	if err != nil {
		return todoRecord{}, err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &record.task.Tags); err != nil {
		return todoRecord{}, err
	}
	record.task.CompletedAt = completedAt.Int64
	record.completed = completedAt.Valid
	return record, nil
}

type TodoAction string

const (
	TodoComplete TodoAction = "complete"
	TodoReopen   TodoAction = "reopen"
	TodoRemove   TodoAction = "remove"
	TodoUndo     TodoAction = "undo"
)

func (s *Store) ListTodo(ctx context.Context, owner Owner) (TodoList, error) {
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return TodoList{}, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT `+todoColumns+`
		FROM todo_tasks WHERE owner_id = ? AND removal_state != 'removed'
		ORDER BY completed_at DESC, created_at DESC, id DESC`, owner.id)
	if err != nil {
		return TodoList{}, err
	}
	defer rows.Close()
	list := TodoList{Tasks: []TodoTask{}}
	for rows.Next() {
		record, err := scanTodo(rows)
		if err != nil {
			return TodoList{}, err
		}
		if record.removalState == "undoable" {
			list.Undo = &record.task
		} else {
			list.Tasks = append(list.Tasks, record.task)
		}
	}
	return list, rows.Err()
}

// AddTodo uses the draft's ID as its retry key. Even after removal the row
// retains its immutable content, so replaying Add cannot resurrect it.
func (s *Store) AddTodo(ctx context.Context, owner Owner, task TodoTask) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return err
	}
	existing, err := scanTodo(tx.QueryRowContext(ctx, `SELECT `+todoColumns+`
		FROM todo_tasks WHERE owner_id = ? AND id = ?`, owner.id, task.ID))
	switch {
	case err == nil:
		if existing.task.Title != task.Title || !slices.Equal(existing.task.Tags, task.Tags) || existing.task.EstimateMinutes != task.EstimateMinutes {
			return ErrIdempotencyConflict
		}
		return nil
	case !errors.Is(err, ErrNotFound):
		return err
	}
	if _, err := insertTodo(ctx, tx, owner, task); err != nil {
		return err
	}
	return tx.Commit()
}

func insertTodo(ctx context.Context, tx *sql.Tx, owner Owner, task TodoTask) (TodoTask, error) {
	tagsJSON, err := json.Marshal(task.Tags)
	if err != nil {
		return TodoTask{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO todo_tasks (owner_id, id, title, tags_json, estimate_minutes, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, owner.id, task.ID, task.Title, string(tagsJSON), task.EstimateMinutes, time.Now().UnixMilli())
	if err != nil {
		return TodoTask{}, err
	}
	task.Revision, task.CompletedAt = 1, 0
	return task, nil
}

func (s *Store) ChangeTodoState(ctx context.Context, owner Owner, id string, expectedRevision int64, action TodoAction) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return err
	}
	if _, err := changeTodoState(ctx, tx, owner, id, expectedRevision, action, false); err != nil {
		return err
	}
	return tx.Commit()
}

// Browser actions reject an already selected completion state. Agent target
// setters instead record a successful no-op, after checking the revision.
func changeTodoState(ctx context.Context, tx *sql.Tx, owner Owner, id string, expectedRevision int64, action TodoAction, allowCompletionNoop bool) (TodoMutationResult, error) {
	record, err := scanTodo(tx.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todo_tasks
		WHERE owner_id = ? AND id = ? AND removal_state != 'removed'`, owner.id, id))
	if err != nil {
		return TodoMutationResult{}, err
	}
	// Undo requires a pending removal. All other actions require a visible task.
	undoRequested := action == TodoUndo
	removalPending := record.removalState == "undoable"
	if record.task.Revision != expectedRevision || undoRequested != removalPending {
		return TodoMutationResult{}, ErrConflict
	}
	switch action {
	case TodoComplete, TodoReopen:
		completed := action == TodoComplete
		if record.completed == completed {
			if allowCompletionNoop {
				return TodoMutationResult{Task: record.task}, nil
			}
			return TodoMutationResult{}, ErrConflict
		}
		record.task.CompletedAt = 0
		if completed {
			record.task.CompletedAt = time.Now().UnixMilli()
		}
	case TodoRemove:
		// A new removal replaces the one pending Undo for this owner. Keep
		// the prior ID reserved for exact Add retries.
		if _, err := tx.ExecContext(ctx, `UPDATE todo_tasks SET removal_state = 'removed'
			WHERE owner_id = ? AND removal_state = 'undoable'`, owner.id); err != nil {
			return TodoMutationResult{}, err
		}
		record.removalState = "undoable"
	case TodoUndo:
		record.removalState = "visible"
	default:
		return TodoMutationResult{}, ErrConflict
	}
	record.task.Revision++
	_, err = tx.ExecContext(ctx, `UPDATE todo_tasks SET completed_at = ?, removal_state = ?, revision = revision + 1
		WHERE owner_id = ? AND id = ?`, nullableTime(record.task.CompletedAt), record.removalState, owner.id, id)
	if err != nil {
		return TodoMutationResult{}, err
	}
	return TodoMutationResult{Task: record.task, Removed: record.removalState == "undoable"}, nil
}

// Todo requires an explicit removed view for the latest undoable removal.
// Superseded removals are retained only to reserve IDs and are never readable.
func (s *Store) Todo(ctx context.Context, owner Owner, id string, removed bool) (TodoTask, error) {
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return TodoTask{}, err
	}
	state := "visible"
	if removed {
		state = "undoable"
	}
	record, err := scanTodo(s.read.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todo_tasks
		WHERE owner_id = ? AND id = ? AND removal_state = ?`, owner.id, id, state))
	return record.task, err
}

// ListTodos orders every completion view by immutable creation time and ID.
// The page and latest Undo are read from the same database snapshot.
func (s *Store) ListTodos(ctx context.Context, owner Owner, cursor TodoCursor, limit int) (TodoPage, error) {
	if limit < 1 || limit > 50 {
		return TodoPage{}, ErrLimit
	}
	var filter string
	switch cursor.Status {
	case "open":
		filter = " AND completed_at IS NULL"
	case "completed":
		filter = " AND completed_at IS NOT NULL"
	case "all":
	default:
		return TodoPage{}, ErrLimit
	}
	tx, err := s.read.BeginTx(ctx, nil)
	if err != nil {
		return TodoPage{}, err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return TodoPage{}, err
	}
	pageSQL, args := pageRange("created_at", "id", cursor.Timestamp, cursor.ID, false, limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT `+todoColumns+` FROM todo_tasks
		WHERE owner_id = ? AND removal_state = 'visible'`+filter+pageSQL, append([]any{owner.id}, args...)...)
	if err != nil {
		return TodoPage{}, err
	}
	defer rows.Close()
	page := TodoPage{Tasks: []TodoTask{}}
	var last todoRecord
	for rows.Next() {
		record, err := scanTodo(rows)
		if err != nil {
			return TodoPage{}, err
		}
		if len(page.Tasks) == limit {
			next := cursor
			next.Timestamp, next.ID = last.createdAt, last.task.ID
			page.Next = &next
			break
		}
		page.Tasks = append(page.Tasks, record.task)
		last = record
	}
	if err := rows.Err(); err != nil {
		return TodoPage{}, err
	}
	if err := rows.Close(); err != nil {
		return TodoPage{}, err
	}
	undo, err := scanTodo(tx.QueryRowContext(ctx, `SELECT `+todoColumns+` FROM todo_tasks
		WHERE owner_id = ? AND removal_state = 'undoable'`, owner.id))
	if err == nil {
		page.Undo = &undo.task
	} else if !errors.Is(err, ErrNotFound) {
		return TodoPage{}, err
	}
	if err := tx.Commit(); err != nil {
		return TodoPage{}, err
	}
	return page, nil
}

func replayTodoMutation(ctx context.Context, tx *sql.Tx, owner Owner, key, operation string, payload []byte) (TodoMutationResult, bool, error) {
	var storedOperation string
	var storedHash, result []byte
	err := tx.QueryRowContext(ctx, `SELECT operation, payload_hash, result FROM todo_idempotency_results
		WHERE owner_id = ? AND key = ?`, owner.id, key).Scan(&storedOperation, &storedHash, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return TodoMutationResult{}, false, nil
	}
	if err != nil {
		return TodoMutationResult{}, false, err
	}
	hash := sha256.Sum256(payload)
	if storedOperation != operation || string(storedHash) != string(hash[:]) {
		return TodoMutationResult{}, false, ErrIdempotencyConflict
	}
	var out TodoMutationResult
	err = json.Unmarshal(result, &out)
	return out, true, err
}

// MutateTodo commits the task state and the exact successful result together.
// Replay precedes task/revision checks so it cannot repeat a later removal or
// Undo a different task after the owner's latest pending removal has changed.
func (s *Store) MutateTodo(ctx context.Context, owner Owner, key, operation string, payload []byte, task TodoTask, expectedRevision int64, completed bool) (TodoMutationResult, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return TodoMutationResult{}, err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return TodoMutationResult{}, err
	}
	if out, found, err := replayTodoMutation(ctx, tx, owner, key, operation, payload); found || err != nil {
		return out, err
	}
	var out TodoMutationResult
	switch operation {
	case "add_todo":
		task.ID = rand.Text()
		out.Task, err = insertTodo(ctx, tx, owner, task)
	case "set_todo_completed":
		action := TodoReopen
		if completed {
			action = TodoComplete
		}
		out, err = changeTodoState(ctx, tx, owner, task.ID, expectedRevision, action, true)
	case "remove_todo":
		out, err = changeTodoState(ctx, tx, owner, task.ID, expectedRevision, TodoRemove, false)
	case "undo_todo_removal":
		out, err = changeTodoState(ctx, tx, owner, task.ID, expectedRevision, TodoUndo, false)
	default:
		return TodoMutationResult{}, ErrConflict
	}
	if err != nil {
		return TodoMutationResult{}, err
	}
	// Empty tags are omitted from retry JSON; return the same representation
	// on the original call that decoding the persisted snapshot will produce.
	if len(out.Task.Tags) == 0 {
		out.Task.Tags = nil
	}
	result, err := json.Marshal(out)
	if err != nil {
		return TodoMutationResult{}, err
	}
	hash := sha256.Sum256(payload)
	if _, err := tx.ExecContext(ctx, `INSERT INTO todo_idempotency_results (owner_id, key, operation, payload_hash, result, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, owner.id, key, operation, hash[:], result, time.Now().UnixMilli()); err != nil {
		return TodoMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TodoMutationResult{}, err
	}
	return out, nil
}
