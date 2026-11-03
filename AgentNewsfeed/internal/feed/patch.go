package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/urls"
)

type GetInput struct {
	ArticleID string `json:"article_id"`
	Trashed   bool   `json:"trashed,omitzero"`
}

func (s *Service) GetArticleTool(ctx context.Context, p identity.Principal, in GetInput) (sqlite.Article, error) {
	return s.GetArticleInView(ctx, p, in.ArticleID, in.Trashed)
}

// Patch distinguishes omission from an explicit empty string or null. Only the
// optional body and thumbnail may be cleared; null and empty clear identically.
type Patch struct {
	URL             *string `json:"url,omitempty"`
	Title           *string `json:"title,omitempty"`
	Summary         *string `json:"summary,omitempty"`
	ThumbnailURL    *string `json:"thumbnail_url,omitempty"`
	ContentMarkdown *string `json:"content_markdown,omitempty"`
}

func (p *Patch) UnmarshalJSON(body []byte) error {
	*p = Patch{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrInvalid
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
		var target **string
		clearable := false
		switch name {
		case "url":
			target = &p.URL
		case "title":
			target = &p.Title
		case "summary":
			target = &p.Summary
		case "thumbnail_url":
			target, clearable = &p.ThumbnailURL, true
		case "content_markdown":
			target, clearable = &p.ContentMarkdown, true
		default:
			return ErrInvalid
		}
		var value *string
		if err := decoder.Decode(&value); err != nil || (value == nil && !clearable) {
			return ErrInvalid
		}
		if value == nil {
			value = new("")
		}
		*target = value
	}
	if _, err := decoder.Token(); err != nil {
		return ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

type PatchInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	ArticleID        string `json:"article_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Patch            Patch  `json:"patch"`
}

func validMutation(key, id string, expected int64) bool {
	return boundedText(key, 128) && boundedText(id, 64) && expected > 0
}

func (s *Service) PatchArticle(ctx context.Context, p identity.Principal, in PatchInput) (sqlite.MutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.MutationResult{}, err
	}
	if !validMutation(in.IdempotencyKey, in.ArticleID, in.ExpectedRevision) || in.Patch == (Patch{}) {
		return sqlite.MutationResult{}, ErrInvalid
	}
	// Hash the supplied intent, independent of the current article, so replay
	// still works after later edits or Trash/Restore. Null and empty normalize
	// to the same explicit clear; omission remains a different intent.
	patch := in.Patch
	if patch.URL != nil {
		canonical, err := urls.Canonical(*patch.URL)
		if err != nil {
			return sqlite.MutationResult{}, ErrInvalid
		}
		patch.URL = new(canonical)
	}
	if (patch.Title != nil && !boundedText(*patch.Title, 300)) || (patch.Summary != nil && !boundedText(*patch.Summary, 1200)) ||
		(patch.ContentMarkdown != nil && !validMarkdown(*patch.ContentMarkdown)) || (patch.ThumbnailURL != nil && s.thumbnails.Validate(*patch.ThumbnailURL) != nil) {
		return sqlite.MutationResult{}, ErrInvalid
	}
	in.Patch = patch
	payload, _ := json.Marshal(in)
	if out, found, err := s.store.ReplayMutation(ctx, p.Owner(), in.IdempotencyKey, "patch_article", payload); found || err != nil {
		return out, err
	}
	current, err := s.store.Article(ctx, p.Owner(), in.ArticleID)
	if err != nil {
		return sqlite.MutationResult{}, err
	}
	if current.Revision != in.ExpectedRevision {
		return sqlite.MutationResult{}, sqlite.ErrConflict
	}
	content := Content{URL: current.URL, Title: current.Title, Summary: current.Summary, ThumbnailURL: current.ThumbnailURL, ContentMarkdown: current.ContentMarkdown}
	if patch.URL != nil {
		content.URL = *patch.URL
	}
	if patch.Title != nil {
		content.Title = *patch.Title
	}
	if patch.Summary != nil {
		content.Summary = *patch.Summary
	}
	if patch.ThumbnailURL != nil {
		content.ThumbnailURL = *patch.ThumbnailURL
	}
	if patch.ContentMarkdown != nil {
		content.ContentMarkdown = *patch.ContentMarkdown
	}
	a, err := normalizeContent(content, s.thumbnails)
	if err != nil {
		return sqlite.MutationResult{}, err
	}
	a.ID = current.ID
	if duplicate, err := s.store.ArticleForURL(ctx, p.Owner(), a.URL); err == nil && duplicate.ID != a.ID {
		return sqlite.MutationResult{}, sqlite.ErrConflict
	} else if err != nil && !errors.Is(err, sqlite.ErrNotFound) {
		return sqlite.MutationResult{}, err
	}
	change := sqlite.ThumbnailChange{}
	// Explicitly supplying the same URL refreshes a legacy or failed image.
	if patch.ThumbnailURL != nil {
		change = s.prepareThumbnail(ctx, content.ThumbnailURL)
	}
	return s.store.MutateArticle(ctx, p.Owner(), in.IdempotencyKey, "patch_article", payload, a, in.ExpectedRevision, change)
}

type StateInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	ArticleID        string `json:"article_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type ArchiveInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	ArticleID        string `json:"article_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Archived         bool   `json:"archived"`
}

func (s *Service) mutateState(ctx context.Context, p identity.Principal, in StateInput, operation string, archived bool) (sqlite.MutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.MutationResult{}, err
	}
	if !validMutation(in.IdempotencyKey, in.ArticleID, in.ExpectedRevision) {
		return sqlite.MutationResult{}, ErrInvalid
	}
	payload, _ := json.Marshal(struct {
		StateInput
		Archived bool `json:"archived,omitzero"`
	}{in, archived})
	state := sqlite.Article{ID: in.ArticleID}
	if archived {
		// This is a target-state flag, not a timestamp. The store preserves or
		// assigns the actual archive time inside the mutation transaction.
		state.ArchivedAt = 1
	}
	return s.store.MutateArticle(ctx, p.Owner(), in.IdempotencyKey, operation, payload, state, in.ExpectedRevision)
}

func (s *Service) TrashArticle(ctx context.Context, p identity.Principal, in StateInput) (sqlite.MutationResult, error) {
	return s.mutateState(ctx, p, in, "trash_article", false)
}
func (s *Service) RestoreArticle(ctx context.Context, p identity.Principal, in StateInput) (sqlite.MutationResult, error) {
	return s.mutateState(ctx, p, in, "restore_article", false)
}
func (s *Service) SetArticleArchived(ctx context.Context, p identity.Principal, in ArchiveInput) (sqlite.MutationResult, error) {
	state := StateInput{IdempotencyKey: in.IdempotencyKey, ArticleID: in.ArticleID, ExpectedRevision: in.ExpectedRevision}
	return s.mutateState(ctx, p, state, "set_article_archived", in.Archived)
}
