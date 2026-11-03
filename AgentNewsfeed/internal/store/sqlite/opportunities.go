package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// OpportunityContent keeps source facts separate from the external agent's
// commentary. Empty optional values mean unknown, not an inferred fact.
type OpportunityContent struct {
	Kind              string   `json:"kind"`
	URL               string   `json:"url"`
	Title             string   `json:"title"`
	Company           string   `json:"company"`
	Location          string   `json:"location,omitempty"`
	Workplace         string   `json:"workplace,omitempty"`
	EmploymentType    string   `json:"employment_type,omitempty"`
	EmploymentPeriod  string   `json:"employment_period,omitempty"`
	Compensation      string   `json:"compensation,omitempty"`
	Deadline          string   `json:"deadline,omitempty"`
	Tags              []string `json:"tags,omitempty"`
	SourceDescription string   `json:"source_description,omitempty"`
	AgentNote         string   `json:"agent_note,omitempty"`
}

type Opportunity struct {
	OpportunityContent
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	ArchivedAt int64  `json:"archived_at,omitzero"`
	TrashedAt  int64  `json:"trashed_at,omitzero"`
}

type OpportunityMutationResult struct {
	Opportunity Opportunity `json:"opportunity"`
	Created     bool        `json:"created"`
}

// Field names intentionally differ from article cursors in both directions.
// The service also verifies the collection, account and selected view.
type OpportunityCursor struct {
	Collection string `json:"collection"`
	Owner      string `json:"account"`
	View       string `json:"view"`
	Timestamp  int64  `json:"before_time"`
	ID         string `json:"before_id"`
}

const opportunitySummaryColumns = `id, canonical_url, kind, title, company, location, workplace,
 employment_type, employment_period, compensation, deadline, tags_json, revision, created_at, updated_at, archived_at, trashed_at`
const opportunityColumns = opportunitySummaryColumns + `, source_description, agent_note`

func scanOpportunity(row scanner) (Opportunity, error) {
	var o Opportunity
	var tags string
	var archived, trashed sql.NullInt64
	err := row.Scan(&o.ID, &o.URL, &o.Kind, &o.Title, &o.Company, &o.Location, &o.Workplace,
		&o.EmploymentType, &o.EmploymentPeriod, &o.Compensation, &o.Deadline, &tags, &o.Revision,
		&o.CreatedAt, &o.UpdatedAt, &archived, &trashed, &o.SourceDescription, &o.AgentNote)
	if errors.Is(err, sql.ErrNoRows) {
		return Opportunity{}, ErrNotFound
	}
	if err != nil {
		return Opportunity{}, err
	}
	o.ArchivedAt, o.TrashedAt = archived.Int64, trashed.Int64
	if err := json.Unmarshal([]byte(tags), &o.Tags); err != nil {
		return Opportunity{}, err
	}
	if len(o.Tags) == 0 {
		o.Tags = nil
	}
	return o, nil
}

func (s *Store) OpportunityInView(ctx context.Context, owner Owner, id string, trashed bool) (Opportunity, error) {
	if owner.id == "" {
		return Opportunity{}, ErrOwnerRequired
	}
	return scanOpportunity(s.read.QueryRowContext(ctx, "SELECT "+opportunityColumns+` FROM opportunities
 WHERE owner_id=? AND id=? AND (trashed_at IS NOT NULL)=?
 AND owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)`, owner.id, id, trashed))
}

func (s *Store) ListOpportunities(ctx context.Context, owner Owner, cursor OpportunityCursor, limit int) ([]Opportunity, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	if limit < 1 || limit > 51 {
		return nil, ErrLimit
	}
	var viewFilter, timestampColumn string
	switch cursor.View {
	case "active":
		viewFilter, timestampColumn = "trashed_at IS NULL AND archived_at IS NULL", "created_at"
	case "archive":
		viewFilter, timestampColumn = "trashed_at IS NULL AND archived_at IS NOT NULL", "archived_at"
	case "trash":
		viewFilter, timestampColumn = "trashed_at IS NOT NULL", "trashed_at"
	default:
		return nil, ErrLimit
	}
	// Empty detail columns let the shared scanner serve list results without
	// loading the long source description or the agent note from storage.
	rows, err := s.read.QueryContext(ctx, "SELECT "+opportunitySummaryColumns+`, '', '' FROM opportunities
 WHERE owner_id=? AND owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL) AND `+viewFilter+
		" AND (?=0 OR "+timestampColumn+"<? OR ("+timestampColumn+"=? AND id<?)) ORDER BY "+timestampColumn+" DESC, id DESC LIMIT ?",
		owner.id, cursor.Timestamp, cursor.Timestamp, cursor.Timestamp, cursor.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	opportunities := []Opportunity{}
	for rows.Next() {
		o, err := scanOpportunity(rows)
		if err != nil {
			return nil, err
		}
		opportunities = append(opportunities, o)
	}
	return opportunities, rows.Err()
}

func replayOpportunityMutation(ctx context.Context, db rowQueryer, owner Owner, key, operation string, payload []byte) (OpportunityMutationResult, bool, error) {
	var storedOperation string
	var storedHash, result []byte
	err := db.QueryRowContext(ctx, `SELECT operation,payload_hash,result FROM opportunity_idempotency_results
 WHERE owner_id=? AND key=?`, owner.id, key).Scan(&storedOperation, &storedHash, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return OpportunityMutationResult{}, false, nil
	}
	if err != nil {
		return OpportunityMutationResult{}, false, err
	}
	hash := sha256.Sum256(payload)
	if storedOperation != operation || string(storedHash) != string(hash[:]) {
		return OpportunityMutationResult{}, false, ErrIdempotencyConflict
	}
	var out OpportunityMutationResult
	err = json.Unmarshal(result, &out)
	return out, true, err
}

// ReplayOpportunityMutation precedes the current-record lookup needed by patch.
// MutateOpportunity rechecks owner and retry identity under its write lock.
func (s *Store) ReplayOpportunityMutation(ctx context.Context, owner Owner, key, operation string, payload []byte) (OpportunityMutationResult, bool, error) {
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return OpportunityMutationResult{}, false, err
	}
	return replayOpportunityMutation(ctx, s.read, owner, key, operation, payload)
}

// MutateOpportunity commits the listing, reading state and exact successful
// retry result together. No source URL is fetched by this operation.
func (s *Store) MutateOpportunity(ctx context.Context, owner Owner, key, operation string, payload []byte, o Opportunity, expectedRevision int64, archiveTarget bool) (OpportunityMutationResult, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return OpportunityMutationResult{}, err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return OpportunityMutationResult{}, err
	}
	// A successful retry returns its original snapshot before current-state or
	// revision checks: later edits and Trash changes must not invalidate replay.
	if out, found, err := replayOpportunityMutation(ctx, tx, owner, key, operation, payload); found || err != nil {
		return out, err
	}
	now := time.Now().UnixMilli()
	created := false
	switch operation {
	case "add_opportunity":
		// Deduplicate across all views, retaining the saved content and state.
		existing, err := scanOpportunity(tx.QueryRowContext(ctx, "SELECT "+opportunityColumns+` FROM opportunities WHERE owner_id=? AND canonical_url=?`, owner.id, o.URL))
		if err == nil {
			o = existing
		} else if errors.Is(err, ErrNotFound) {
			o.ID, o.Revision, o.CreatedAt, o.UpdatedAt = rand.Text(), 1, now, now
			if err := insertOpportunity(ctx, tx, owner, o); err != nil {
				return OpportunityMutationResult{}, err
			}
			created = true
		} else {
			return OpportunityMutationResult{}, err
		}
	case "update_opportunity", "patch_opportunity", "trash_opportunity", "restore_opportunity", "set_opportunity_archived":
		current, err := scanOpportunity(tx.QueryRowContext(ctx, "SELECT "+opportunityColumns+` FROM opportunities WHERE owner_id=? AND id=?`, owner.id, o.ID))
		if err != nil {
			return OpportunityMutationResult{}, err
		}
		if current.TrashedAt != 0 && operation != "restore_opportunity" {
			return OpportunityMutationResult{}, ErrNotFound
		}
		if expectedRevision != current.Revision {
			return OpportunityMutationResult{}, ErrConflict
		}
		if operation == "update_opportunity" || operation == "patch_opportunity" {
			var duplicate bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM opportunities WHERE owner_id=? AND canonical_url=? AND id!=?)`, owner.id, o.URL, o.ID).Scan(&duplicate); err != nil {
				return OpportunityMutationResult{}, err
			}
			if duplicate {
				return OpportunityMutationResult{}, ErrConflict
			}
			o.Revision, o.CreatedAt, o.UpdatedAt = current.Revision+1, current.CreatedAt, now
			o.ArchivedAt = current.ArchivedAt
			if err := updateOpportunityContent(ctx, tx, owner, o); err != nil {
				return OpportunityMutationResult{}, err
			}
		} else {
			o = current
			stateChanged := false
			switch operation {
			case "trash_opportunity":
				o.TrashedAt, stateChanged = now, true
			case "restore_opportunity":
				stateChanged = o.TrashedAt != 0
				o.TrashedAt = 0
			case "set_opportunity_archived":
				stateChanged = (o.ArchivedAt != 0) != archiveTarget
				if stateChanged {
					o.ArchivedAt = 0
					if archiveTarget {
						o.ArchivedAt = now
					}
				}
			}
			if stateChanged {
				o.Revision++
				o.UpdatedAt = now
				if _, err := tx.ExecContext(ctx, `UPDATE opportunities SET archived_at=?,trashed_at=?,revision=?,updated_at=? WHERE owner_id=? AND id=?`, nullableTime(o.ArchivedAt), nullableTime(o.TrashedAt), o.Revision, o.UpdatedAt, owner.id, o.ID); err != nil {
					return OpportunityMutationResult{}, err
				}
			}
		}
	default:
		return OpportunityMutationResult{}, ErrConflict
	}
	// Even a duplicate add or no-op state target is a successful intent whose
	// exact result must be recorded in this same transaction.
	out := OpportunityMutationResult{Opportunity: o, Created: created}
	result, err := json.Marshal(out)
	if err != nil {
		return OpportunityMutationResult{}, err
	}
	hash := sha256.Sum256(payload)
	if _, err := tx.ExecContext(ctx, `INSERT INTO opportunity_idempotency_results(owner_id,key,operation,payload_hash,result,created_at)
 VALUES (?,?,?,?,?,?)`, owner.id, key, operation, hash[:], result, now); err != nil {
		return OpportunityMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return OpportunityMutationResult{}, err
	}
	return out, nil
}

func opportunityTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	body, _ := json.Marshal(tags)
	return string(body)
}

func insertOpportunity(ctx context.Context, tx *sql.Tx, owner Owner, o Opportunity) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO opportunities(owner_id,id,canonical_url,kind,title,company,location,workplace,
 employment_type,employment_period,compensation,deadline,tags_json,source_description,agent_note,revision,created_at,updated_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, owner.id, o.ID, o.URL, o.Kind, o.Title, o.Company, o.Location, o.Workplace,
		o.EmploymentType, o.EmploymentPeriod, o.Compensation, o.Deadline, opportunityTags(o.Tags), o.SourceDescription, o.AgentNote, o.Revision, o.CreatedAt, o.UpdatedAt)
	return err
}

func updateOpportunityContent(ctx context.Context, tx *sql.Tx, owner Owner, o Opportunity) error {
	_, err := tx.ExecContext(ctx, `UPDATE opportunities SET canonical_url=?,kind=?,title=?,company=?,location=?,workplace=?,
 employment_type=?,employment_period=?,compensation=?,deadline=?,tags_json=?,source_description=?,agent_note=?,revision=?,updated_at=?
 WHERE owner_id=? AND id=?`, o.URL, o.Kind, o.Title, o.Company, o.Location, o.Workplace, o.EmploymentType, o.EmploymentPeriod,
		o.Compensation, o.Deadline, opportunityTags(o.Tags), o.SourceDescription, o.AgentNote, o.Revision, o.UpdatedAt, owner.id, o.ID)
	return err
}
