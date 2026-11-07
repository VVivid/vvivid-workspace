package feed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

const (
	TodoCollection = "todos"
	// A 64-byte browser ID can expand to 384 JSON bytes. Allow room for
	// owner, status and timestamp metadata plus the base64 encoding.
	MaxTodoCursorBytes = 1024
)

type ListTodosInput struct {
	Limit  int    `json:"limit,omitzero"`
	Cursor string `json:"cursor,omitempty"`
	Status string `json:"status,omitempty"`
}

type TodosPage struct {
	Tasks      []sqlite.TodoTask `json:"tasks"`
	Undo       *sqlite.TodoTask  `json:"undo,omitempty"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type GetTodoInput struct {
	TaskID  string `json:"task_id"`
	Removed bool   `json:"removed,omitzero"`
}

// Tags uses the same comma-separated, bounded input as the browser composer.
type AddTodoInput struct {
	IdempotencyKey  string `json:"idempotency_key"`
	Title           string `json:"title"`
	Tags            string `json:"tags,omitempty"`
	EstimateMinutes int    `json:"estimate_minutes,omitzero"`
}

type TodoStateInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	TaskID           string `json:"task_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type SetTodoCompletedInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	TaskID           string `json:"task_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Completed        bool   `json:"completed"`
}

// ListTodo retains the complete browser checklist and its existing order.
func (s *Service) ListTodo(ctx context.Context, p identity.Principal) (sqlite.TodoList, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.TodoList{}, err
	}
	return s.store.ListTodo(ctx, p.Owner())
}

func normalizeTodo(title, tagInput string, estimateMinutes int) (sqlite.TodoTask, error) {
	title = strings.TrimSpace(title)
	if !boundedText(title, 180*utf8.UTFMax) || utf8.RuneCountInString(title) > 180 {
		return sqlite.TodoTask{}, ErrInvalid
	}
	if !utf8.ValidString(tagInput) || utf8.RuneCountInString(tagInput) > 125 || strings.ContainsFunc(tagInput, unicode.IsControl) {
		return sqlite.TodoTask{}, ErrInvalid
	}
	if !slices.Contains([]int{0, 15, 30, 45, 60, 90, 120, 180, 240, 360, 480}, estimateMinutes) {
		return sqlite.TodoTask{}, ErrInvalid
	}
	tags := []string{}
	seen := map[string]bool{}
	for value := range strings.SplitSeq(tagInput, ",") {
		tag := strings.Join(strings.Fields(value), " ")
		key := strings.ToLower(tag)
		if tag != "" && !seen[key] {
			tags = append(tags, tag)
			seen[key] = true
		}
	}
	return sqlite.TodoTask{Title: title, Tags: tags, EstimateMinutes: estimateMinutes}, nil
}

func (s *Service) AddTodo(ctx context.Context, p identity.Principal, id, title, tagInput string, estimateMinutes int) error {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return err
	}
	if !boundedText(id, 64) {
		return ErrInvalid
	}
	task, err := normalizeTodo(title, tagInput, estimateMinutes)
	if err != nil {
		return err
	}
	task.ID = id
	return s.store.AddTodo(ctx, p.Owner(), task)
}

func (s *Service) ChangeTodoState(ctx context.Context, p identity.Principal, id string, revision int64, action sqlite.TodoAction) error {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return err
	}
	if !boundedText(id, 64) || revision < 1 {
		return ErrInvalid
	}
	switch action {
	case sqlite.TodoComplete, sqlite.TodoReopen, sqlite.TodoRemove, sqlite.TodoUndo:
		return s.store.ChangeTodoState(ctx, p.Owner(), id, revision, action)
	default:
		return ErrInvalid
	}
}

// ListTodos is a bounded read for agents. Undo is independent of the selected
// completion status; only the owner's latest pending removal can be undone.
func (s *Service) ListTodos(ctx context.Context, p identity.Principal, in ListTodosInput) (TodosPage, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return TodosPage{}, err
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Status == "" {
		in.Status = "open"
	}
	if in.Limit < 1 || in.Limit > 50 || len(in.Cursor) > MaxTodoCursorBytes || !slices.Contains([]string{"open", "completed", "all"}, in.Status) {
		return TodosPage{}, ErrInvalid
	}
	owner := p.Owner()
	cursor := sqlite.TodoCursor{Collection: TodoCollection, Owner: owner.ID(), Status: in.Status}
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil {
			return TodosPage{}, ErrInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		cursor = sqlite.TodoCursor{}
		if err := decoder.Decode(&cursor); err != nil || cursor.Collection != TodoCollection || cursor.Owner != owner.ID() ||
			cursor.Status != in.Status || cursor.Timestamp < 1 || !boundedText(cursor.ID, 64) {
			return TodosPage{}, ErrInvalid
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return TodosPage{}, ErrInvalid
		}
	}
	page, err := s.store.ListTodos(ctx, owner, cursor, in.Limit)
	if err != nil {
		return TodosPage{}, err
	}
	out := TodosPage{Tasks: page.Tasks, Undo: page.Undo}
	if page.Next != nil {
		out.NextCursor = encodeCursor(page.Next)
	}
	return out, nil
}

func (s *Service) GetTodo(ctx context.Context, p identity.Principal, in GetTodoInput) (sqlite.TodoTask, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.TodoTask{}, err
	}
	if !boundedText(in.TaskID, 64) {
		return sqlite.TodoTask{}, ErrInvalid
	}
	return s.store.Todo(ctx, p.Owner(), in.TaskID, in.Removed)
}

// AddTodoTask uses an intent key rather than the browser's client draft ID.
// Separate keys always create separate tasks, even when content is identical.
func (s *Service) AddTodoTask(ctx context.Context, p identity.Principal, in AddTodoInput) (sqlite.TodoMutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.TodoMutationResult{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) {
		return sqlite.TodoMutationResult{}, ErrInvalid
	}
	task, err := normalizeTodo(in.Title, in.Tags, in.EstimateMinutes)
	if err != nil {
		return sqlite.TodoMutationResult{}, err
	}
	payload, _ := json.Marshal(task)
	return s.store.MutateTodo(ctx, p.Owner(), in.IdempotencyKey, "add_todo", payload, task, 0, false)
}

func (s *Service) mutateTodoState(ctx context.Context, p identity.Principal, in TodoStateInput, operation string, completed bool) (sqlite.TodoMutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.TodoMutationResult{}, err
	}
	if !validMutation(in.IdempotencyKey, in.TaskID, in.ExpectedRevision) {
		return sqlite.TodoMutationResult{}, ErrInvalid
	}
	payload, _ := json.Marshal(struct {
		TodoStateInput
		Completed bool `json:"completed"`
	}{TodoStateInput: in, Completed: completed})
	return s.store.MutateTodo(ctx, p.Owner(), in.IdempotencyKey, operation, payload, sqlite.TodoTask{ID: in.TaskID}, in.ExpectedRevision, completed)
}

// Adapters must require completed explicitly; false selects reopen.
func (s *Service) SetTodoCompleted(ctx context.Context, p identity.Principal, in SetTodoCompletedInput) (sqlite.TodoMutationResult, error) {
	state := TodoStateInput{IdempotencyKey: in.IdempotencyKey, TaskID: in.TaskID, ExpectedRevision: in.ExpectedRevision}
	return s.mutateTodoState(ctx, p, state, "set_todo_completed", in.Completed)
}

func (s *Service) RemoveTodo(ctx context.Context, p identity.Principal, in TodoStateInput) (sqlite.TodoMutationResult, error) {
	return s.mutateTodoState(ctx, p, in, "remove_todo", false)
}

func (s *Service) UndoTodoRemoval(ctx context.Context, p identity.Principal, in TodoStateInput) (sqlite.TodoMutationResult, error) {
	return s.mutateTodoState(ctx, p, in, "undo_todo_removal", false)
}
