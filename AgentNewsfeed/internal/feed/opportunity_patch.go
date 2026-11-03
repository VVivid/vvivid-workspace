package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/urls"
)

// OpportunityPatch distinguishes omitted fields from explicit clears. Required
// facts cannot be null; every optional field may be cleared with null or empty.
type OpportunityPatch struct {
	Kind              *string   `json:"kind,omitempty"`
	URL               *string   `json:"url,omitempty"`
	Title             *string   `json:"title,omitempty"`
	Company           *string   `json:"company,omitempty"`
	Location          *string   `json:"location,omitempty"`
	Workplace         *string   `json:"workplace,omitempty"`
	EmploymentType    *string   `json:"employment_type,omitempty"`
	EmploymentPeriod  *string   `json:"employment_period,omitempty"`
	Compensation      *string   `json:"compensation,omitempty"`
	Deadline          *string   `json:"deadline,omitempty"`
	Tags              *[]string `json:"tags,omitempty"`
	SourceDescription *string   `json:"source_description,omitempty"`
	AgentNote         *string   `json:"agent_note,omitempty"`
}

func (p *OpportunityPatch) UnmarshalJSON(body []byte) error {
	*p = OpportunityPatch{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrInvalidOpportunity
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return ErrInvalidOpportunity
		}
		seen[name] = true
		if name == "tags" {
			var tags []string
			if err := decoder.Decode(&tags); err != nil {
				return ErrInvalidOpportunity
			}
			if len(tags) == 0 {
				tags = []string{}
			}
			p.Tags = new(tags)
			continue
		}
		var target **string
		clearable := true
		switch name {
		case "kind":
			target, clearable = &p.Kind, false
		case "url":
			target, clearable = &p.URL, false
		case "title":
			target, clearable = &p.Title, false
		case "company":
			target, clearable = &p.Company, false
		case "location":
			target = &p.Location
		case "workplace":
			target = &p.Workplace
		case "employment_type":
			target = &p.EmploymentType
		case "employment_period":
			target = &p.EmploymentPeriod
		case "compensation":
			target = &p.Compensation
		case "deadline":
			target = &p.Deadline
		case "source_description":
			target = &p.SourceDescription
		case "agent_note":
			target = &p.AgentNote
		default:
			return ErrInvalidOpportunity
		}
		var value *string
		if err := decoder.Decode(&value); err != nil || (value == nil && !clearable) {
			return ErrInvalidOpportunity
		}
		if value == nil {
			value = new("")
		}
		*target = value
	}
	if _, err := decoder.Token(); err != nil {
		return ErrInvalidOpportunity
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidOpportunity
	}
	return nil
}

type PatchOpportunityInput struct {
	IdempotencyKey   string           `json:"idempotency_key"`
	OpportunityID    string           `json:"opportunity_id"`
	ExpectedRevision int64            `json:"expected_revision"`
	Patch            OpportunityPatch `json:"patch"`
}

func (p OpportunityPatch) apply(c OpportunityContent) OpportunityContent {
	for _, field := range []struct {
		provided *string
		target   *string
	}{
		{p.Kind, &c.Kind}, {p.URL, &c.URL}, {p.Title, &c.Title}, {p.Company, &c.Company},
		{p.Location, &c.Location}, {p.Workplace, &c.Workplace}, {p.EmploymentType, &c.EmploymentType},
		{p.EmploymentPeriod, &c.EmploymentPeriod}, {p.Compensation, &c.Compensation}, {p.Deadline, &c.Deadline},
		{p.SourceDescription, &c.SourceDescription}, {p.AgentNote, &c.AgentNote},
	} {
		if field.provided != nil {
			*field.target = *field.provided
		}
	}
	if p.Tags != nil {
		c.Tags = *p.Tags
	}
	return c
}

func (s *Service) PatchOpportunity(ctx context.Context, p identity.Principal, in PatchOpportunityInput) (sqlite.OpportunityMutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.OpportunityMutationResult{}, err
	}
	if !validMutation(in.IdempotencyKey, in.OpportunityID, in.ExpectedRevision) || in.Patch == (OpportunityPatch{}) {
		return sqlite.OpportunityMutationResult{}, ErrInvalidOpportunity
	}
	if in.Patch.URL != nil {
		canonical, err := urls.Canonical(*in.Patch.URL)
		if err != nil {
			return sqlite.OpportunityMutationResult{}, ErrInvalidOpportunity
		}
		in.Patch.URL = new(canonical)
	}
	if in.Patch.Tags != nil && len(*in.Patch.Tags) == 0 {
		in.Patch.Tags = new([]string{})
	}
	// Fill omitted required facts with valid placeholders solely to validate
	// the supplied fields. Retry intent must not depend on the current record,
	// which may have changed since the original successful patch.
	validationContent := in.Patch.apply(OpportunityContent{Kind: "job", URL: "https://example.com/", Title: "Valid", Company: "Valid"})
	if _, err := normalizeOpportunity(validationContent); err != nil {
		return sqlite.OpportunityMutationResult{}, err
	}
	payload, _ := json.Marshal(in)
	if out, found, err := s.store.ReplayOpportunityMutation(ctx, p.Owner(), in.IdempotencyKey, "patch_opportunity", payload); found || err != nil {
		return out, err
	}
	current, err := s.store.OpportunityInView(ctx, p.Owner(), in.OpportunityID, false)
	if err == nil && current.Revision != in.ExpectedRevision {
		err = sqlite.ErrConflict
	}
	var content OpportunityContent
	if err == nil {
		content, err = normalizeOpportunity(in.Patch.apply(current.OpportunityContent))
	}
	if err != nil {
		// Another call may have committed this key after the first replay lookup.
		// Its saved result takes precedence over errors from the newer record.
		if out, found, replayErr := s.store.ReplayOpportunityMutation(ctx, p.Owner(), in.IdempotencyKey, "patch_opportunity", payload); found || replayErr != nil {
			return out, replayErr
		}
		return sqlite.OpportunityMutationResult{}, err
	}
	return s.store.MutateOpportunity(ctx, p.Owner(), in.IdempotencyKey, "patch_opportunity", payload,
		sqlite.Opportunity{ID: in.OpportunityID, OpportunityContent: content}, in.ExpectedRevision, false)
}
