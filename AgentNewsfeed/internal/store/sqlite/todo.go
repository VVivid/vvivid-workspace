package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	rows, err := s.read.QueryContext(ctx, `SELECT id, title, tags_json, estimate_minutes, revision, completed_at, removal_state
		FROM todo_tasks WHERE owner_id = ? AND removal_state != 'removed'
		ORDER BY completed_at DESC, created_at DESC, id DESC`, owner.id)
	if err != nil {
		return TodoList{}, err
	}
	defer rows.Close()
	list := TodoList{Tasks: []TodoTask{}}
	for rows.Next() {
		var task TodoTask
		var tagsJSON, removalState string
		var completedAt sql.NullInt64
		if err := rows.Scan(&task.ID, &task.Title, &tagsJSON, &task.EstimateMinutes, &task.Revision, &completedAt, &removalState); err != nil {
			return TodoList{}, err
		}
		if err := json.Unmarshal([]byte(tagsJSON), &task.Tags); err != nil {
			return TodoList{}, err
		}
		task.CompletedAt = completedAt.Int64
		if removalState == "undoable" {
			list.Undo = &task
		} else {
			list.Tasks = append(list.Tasks, task)
		}
	}
	return list, rows.Err()
}

// AddTodo uses the draft's ID as its retry key. Even after removal the row
// retains its immutable content, so replaying Add cannot resurrect it.
func (s *Store) AddTodo(ctx context.Context, owner Owner, task TodoTask) error {
	tagsJSON, err := json.Marshal(task.Tags)
	if err != nil {
		return err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return err
	}
	var existing TodoTask
	var existingTags string
	err = tx.QueryRowContext(ctx, `SELECT title, tags_json, estimate_minutes
		FROM todo_tasks WHERE owner_id = ? AND id = ?`, owner.id, task.ID).
		Scan(&existing.Title, &existingTags, &existing.EstimateMinutes)
	switch {
	case err == nil:
		if existing.Title != task.Title || existingTags != string(tagsJSON) || existing.EstimateMinutes != task.EstimateMinutes {
			return ErrIdempotencyConflict
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO todo_tasks (owner_id, id, title, tags_json, estimate_minutes, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, owner.id, task.ID, task.Title, string(tagsJSON), task.EstimateMinutes, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
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
	var revision int64
	var completedAt sql.NullInt64
	var removalState string
	err = tx.QueryRowContext(ctx, `SELECT revision, completed_at, removal_state FROM todo_tasks
		WHERE owner_id = ? AND id = ? AND removal_state != 'removed'`, owner.id, id).Scan(&revision, &completedAt, &removalState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Undo requires a pending removal. All other actions require a visible task.
	undoRequested := action == TodoUndo
	removalPending := removalState == "undoable"
	if revision != expectedRevision || undoRequested != removalPending {
		return ErrConflict
	}
	switch action {
	case TodoComplete:
		if completedAt.Valid {
			return ErrConflict
		}
		completedAt = sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}
	case TodoReopen:
		if !completedAt.Valid {
			return ErrConflict
		}
		completedAt = sql.NullInt64{}
	case TodoRemove:
		// A new removal replaces the one pending Undo for this owner. Keep
		// the prior ID reserved for exact Add retries.
		if _, err := tx.ExecContext(ctx, `UPDATE todo_tasks SET removal_state = 'removed'
			WHERE owner_id = ? AND removal_state = 'undoable'`, owner.id); err != nil {
			return err
		}
		removalState = "undoable"
	case TodoUndo:
		removalState = "visible"
	default:
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE todo_tasks SET completed_at = ?, removal_state = ?, revision = revision + 1
		WHERE owner_id = ? AND id = ?`, completedAt, removalState, owner.id, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
