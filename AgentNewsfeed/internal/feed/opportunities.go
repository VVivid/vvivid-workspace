package feed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"time"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/urls"
)

var ErrInvalidOpportunity = errors.New("invalid opportunity request")

const (
	MaxSourceDescriptionBytes = 64 << 10
	MaxAgentNoteBytes         = 4 << 10
	MaxOpportunityCursorBytes = 512
	OpportunityCollection     = "opportunities/v1"
)

type OpportunityContent = sqlite.OpportunityContent

type AddOpportunityInput struct {
	IdempotencyKey string             `json:"idempotency_key"`
	Opportunity    OpportunityContent `json:"opportunity"`
}

type UpdateOpportunityInput struct {
	IdempotencyKey   string             `json:"idempotency_key"`
	OpportunityID    string             `json:"opportunity_id"`
	ExpectedRevision int64              `json:"expected_revision"`
	Opportunity      OpportunityContent `json:"opportunity"`
}

type GetOpportunityInput struct {
	OpportunityID string `json:"opportunity_id"`
	Trashed       bool   `json:"trashed,omitzero"`
}

type ListOpportunitiesInput struct {
	Limit    int    `json:"limit,omitzero"`
	Cursor   string `json:"cursor,omitempty"`
	Archived bool   `json:"archived,omitzero"`
	Trashed  bool   `json:"trashed,omitzero"`
}

type OpportunitiesPage struct {
	Opportunities []sqlite.Opportunity `json:"opportunities"`
	NextCursor    string               `json:"next_cursor,omitempty"`
}

type BrowserOpportunitiesPage struct {
	OpportunitiesPage
	PreviousCursor string `json:"previous_cursor,omitempty"`
}

func optionalOpportunityText(value string, max int) bool {
	return value == "" || boundedText(value, max)
}

func normalizeOpportunity(c OpportunityContent) (OpportunityContent, error) {
	canonical, err := urls.Canonical(c.URL)
	if err != nil || !slices.Contains([]string{"job", "internship"}, c.Kind) ||
		!boundedText(c.Title, 300) || !boundedText(c.Company, 200) ||
		!optionalOpportunityText(c.Location, 200) || !optionalOpportunityText(c.EmploymentPeriod, 160) ||
		!optionalOpportunityText(c.Compensation, 300) ||
		!slices.Contains([]string{"", "remote", "hybrid", "on_site"}, c.Workplace) ||
		!slices.Contains([]string{"", "full_time", "part_time", "contract", "temporary", "seasonal", "other"}, c.EmploymentType) ||
		len(c.SourceDescription) > MaxSourceDescriptionBytes || !validMarkdown(c.SourceDescription) ||
		len(c.AgentNote) > MaxAgentNoteBytes || !validMarkdown(c.AgentNote) || len(c.Tags) > 12 {
		return OpportunityContent{}, ErrInvalidOpportunity
	}
	if c.Deadline != "" {
		if _, err := time.Parse(time.DateOnly, c.Deadline); err != nil {
			return OpportunityContent{}, ErrInvalidOpportunity
		}
	}
	seen := make(map[string]bool, len(c.Tags))
	for _, tag := range c.Tags {
		if !boundedText(tag, 40) || seen[tag] {
			return OpportunityContent{}, ErrInvalidOpportunity
		}
		seen[tag] = true
	}
	c.URL = canonical
	c.Tags = slices.Clone(c.Tags)
	if len(c.Tags) == 0 {
		c.Tags = nil
	}
	return c, nil
}

func (s *Service) AddOpportunity(ctx context.Context, p identity.Principal, in AddOpportunityInput) (sqlite.OpportunityMutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.OpportunityMutationResult{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) {
		return sqlite.OpportunityMutationResult{}, ErrInvalidOpportunity
	}
	content, err := normalizeOpportunity(in.Opportunity)
	if err != nil {
		return sqlite.OpportunityMutationResult{}, err
	}
	payload, _ := json.Marshal(content)
	return s.store.MutateOpportunity(ctx, p.Owner(), in.IdempotencyKey, "add_opportunity", payload, sqlite.Opportunity{OpportunityContent: content}, 0, false)
}

func (s *Service) UpdateOpportunity(ctx context.Context, p identity.Principal, in UpdateOpportunityInput) (sqlite.OpportunityMutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.OpportunityMutationResult{}, err
	}
	if !validMutation(in.IdempotencyKey, in.OpportunityID, in.ExpectedRevision) {
		return sqlite.OpportunityMutationResult{}, ErrInvalidOpportunity
	}
	content, err := normalizeOpportunity(in.Opportunity)
	if err != nil {
		return sqlite.OpportunityMutationResult{}, err
	}
	in.Opportunity = content
	payload, _ := json.Marshal(in)
	return s.store.MutateOpportunity(ctx, p.Owner(), in.IdempotencyKey, "update_opportunity", payload,
		sqlite.Opportunity{ID: in.OpportunityID, OpportunityContent: content}, in.ExpectedRevision, false)
}

func (s *Service) GetOpportunity(ctx context.Context, p identity.Principal, in GetOpportunityInput) (sqlite.Opportunity, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.Opportunity{}, err
	}
	if !boundedText(in.OpportunityID, 64) {
		return sqlite.Opportunity{}, ErrInvalidOpportunity
	}
	return s.store.OpportunityInView(ctx, p.Owner(), in.OpportunityID, in.Trashed)
}

func (s *Service) ListOpportunities(ctx context.Context, p identity.Principal, in ListOpportunitiesInput) (OpportunitiesPage, error) {
	page, err := s.listOpportunities(ctx, p, in, forwardOnly)
	return OpportunitiesPage{Opportunities: page.rows, NextCursor: page.next}, err
}

func (s *Service) BrowseOpportunities(ctx context.Context, p identity.Principal, in ListOpportunitiesInput, before bool) (BrowserOpportunitiesPage, error) {
	page, err := s.listOpportunities(ctx, p, in, browseMode(before))
	return BrowserOpportunitiesPage{
		OpportunitiesPage: OpportunitiesPage{Opportunities: page.rows, NextCursor: page.next},
		PreviousCursor:    page.previous,
	}, err
}

func (s *Service) listOpportunities(ctx context.Context, p identity.Principal, in ListOpportunitiesInput, mode pageMode) (listPage[sqlite.Opportunity], error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return listPage[sqlite.Opportunity]{}, err
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Limit < 1 || in.Limit > 50 || len(in.Cursor) > MaxOpportunityCursorBytes || (in.Archived && in.Trashed) {
		return listPage[sqlite.Opportunity]{}, ErrInvalidOpportunity
	}
	view := "active"
	if in.Archived {
		view = "archive"
	} else if in.Trashed {
		view = "trash"
	}
	owner := p.Owner()
	cursor := sqlite.OpportunityCursor{Collection: OpportunityCollection, Owner: owner.ID(), View: view}
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil {
			return listPage[sqlite.Opportunity]{}, ErrInvalidOpportunity
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		cursor = sqlite.OpportunityCursor{}
		if err := decoder.Decode(&cursor); err != nil || cursor.Collection != OpportunityCollection || cursor.Owner != owner.ID() ||
			cursor.View != view || cursor.Timestamp < 1 || !boundedText(cursor.ID, 64) {
			return listPage[sqlite.Opportunity]{}, ErrInvalidOpportunity
		}
		if _, err := decoder.Token(); err != io.EOF {
			return listPage[sqlite.Opportunity]{}, ErrInvalidOpportunity
		}
	}
	return loadPage(in.Limit, mode, in.Cursor != "", cursor,
		func(boundary sqlite.OpportunityCursor, before bool, limit int) ([]sqlite.Opportunity, error) {
			boundary.Before = before
			return s.store.ListOpportunities(ctx, owner, boundary, limit)
		},
		func(opportunity sqlite.Opportunity) sqlite.OpportunityCursor {
			timestamp := opportunity.CreatedAt
			if in.Archived {
				timestamp = opportunity.ArchivedAt
			} else if in.Trashed {
				timestamp = opportunity.TrashedAt
			}
			return sqlite.OpportunityCursor{Collection: OpportunityCollection, Owner: owner.ID(), View: view, Timestamp: timestamp, ID: opportunity.ID}
		})
}

type OpportunityStateInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	OpportunityID    string `json:"opportunity_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type ArchiveOpportunityInput struct {
	IdempotencyKey   string `json:"idempotency_key"`
	OpportunityID    string `json:"opportunity_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Archived         bool   `json:"archived"`
}

func (s *Service) mutateOpportunityState(ctx context.Context, p identity.Principal, in OpportunityStateInput, operation string, archived bool) (sqlite.OpportunityMutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.OpportunityMutationResult{}, err
	}
	if !validMutation(in.IdempotencyKey, in.OpportunityID, in.ExpectedRevision) {
		return sqlite.OpportunityMutationResult{}, ErrInvalidOpportunity
	}
	payload, _ := json.Marshal(struct {
		OpportunityStateInput
		Archived bool `json:"archived"`
	}{in, archived})
	return s.store.MutateOpportunity(ctx, p.Owner(), in.IdempotencyKey, operation, payload, sqlite.Opportunity{ID: in.OpportunityID}, in.ExpectedRevision, archived)
}

func (s *Service) TrashOpportunity(ctx context.Context, p identity.Principal, in OpportunityStateInput) (sqlite.OpportunityMutationResult, error) {
	return s.mutateOpportunityState(ctx, p, in, "trash_opportunity", false)
}

func (s *Service) RestoreOpportunity(ctx context.Context, p identity.Principal, in OpportunityStateInput) (sqlite.OpportunityMutationResult, error) {
	return s.mutateOpportunityState(ctx, p, in, "restore_opportunity", false)
}

func (s *Service) SetOpportunityArchived(ctx context.Context, p identity.Principal, in ArchiveOpportunityInput) (sqlite.OpportunityMutationResult, error) {
	return s.mutateOpportunityState(ctx, p, OpportunityStateInput{IdempotencyKey: in.IdempotencyKey, OpportunityID: in.OpportunityID, ExpectedRevision: in.ExpectedRevision}, "set_opportunity_archived", in.Archived)
}

func OpportunitySafeError(err error) string {
	switch {
	case errors.Is(err, ErrInvalidOpportunity):
		return "Invalid opportunity request. Supply job or internship, a public HTTPS source/application URL, title and company; keep optional facts unknown when unavailable. Use the documented field bounds and enum values."
	case errors.Is(err, sqlite.ErrNotFound):
		return "Opportunity not found."
	case errors.Is(err, sqlite.ErrConflict):
		return "Opportunity changed or URL already exists. Read the current opportunity before retrying with a new key."
	default:
		return SafeError(err)
	}
}
