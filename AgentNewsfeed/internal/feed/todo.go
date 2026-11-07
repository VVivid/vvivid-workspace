package feed

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

func (s *Service) ListTodo(ctx context.Context, p identity.Principal) (sqlite.TodoList, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.TodoList{}, err
	}
	return s.store.ListTodo(ctx, p.Owner())
}

func (s *Service) AddTodo(ctx context.Context, p identity.Principal, id, title, tagInput string, estimateMinutes int) error {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if !boundedText(id, 64) || !boundedText(title, 180*utf8.UTFMax) || utf8.RuneCountInString(title) > 180 ||
		!utf8.ValidString(tagInput) || utf8.RuneCountInString(tagInput) > 125 || strings.ContainsFunc(tagInput, unicode.IsControl) ||
		!slices.Contains([]int{0, 15, 30, 45, 60, 90, 120, 180, 240, 360, 480}, estimateMinutes) {
		return ErrInvalid
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
	return s.store.AddTodo(ctx, p.Owner(), sqlite.TodoTask{ID: id, Title: title, Tags: tags, EstimateMinutes: estimateMinutes})
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
