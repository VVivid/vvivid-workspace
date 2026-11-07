package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

func (s *Store) Remember(ctx context.Context, actor memory.Actor, request memory.RememberRequest) (memory.RememberResult, error) {
	input, canonical, err := canonicalRemember(actor, request)
	if err != nil {
		return memory.RememberResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.RememberResult{}, err
	}
	defer tx.Rollback()
	var previous, saved []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical_request,result FROM idempotency WHERE account_id=? AND request_key=?", actor.AccountID, input.Key).Scan(&previous, &saved)
	if err == nil {
		if !bytes.Equal(previous, canonical) {
			return memory.RememberResult{}, memory.ErrConflict
		}
		var result memory.RememberResult
		if err := json.Unmarshal(saved, &result); err != nil {
			return result, err
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return memory.RememberResult{}, err
	}
	now := time.Now().UTC()
	spaceID, table, err := ensureSpace(ctx, tx, actor.AccountID, input.Space, now)
	if err != nil {
		return memory.RememberResult{}, err
	}
	topicID := input.TopicID
	if topicID != "" {
		var existingSpace string
		err := tx.QueryRowContext(ctx, "SELECT space_id FROM nodes WHERE account_id=? AND span_id=? AND kind='topic'", actor.AccountID, topicID).Scan(&existingSpace)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && existingSpace != spaceID) {
			return memory.RememberResult{}, memory.ErrNotFound
		}
		if err != nil {
			return memory.RememberResult{}, err
		}
	}
	result := memory.RememberResult{Records: make([]memory.Record, 0, len(input.Entries))}
	locals := map[string]string{}
	for i, e := range input.Entries {
		id := newID()
		if topicID == "" {
			topicID = id
		}
		parent := ""
		if e.Kind != memory.Topic {
			parent, err = resolveReference(e.Parent, locals)
			if err != nil {
				return memory.RememberResult{}, err
			}
			if parent == "" {
				parent = topicID
			}
			var found int
			err = tx.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE account_id=? AND span_id=? AND topic_id=? AND space_id=?", actor.AccountID, parent, topicID, spaceID).Scan(&found)
			if errors.Is(err, sql.ErrNoRows) {
				return memory.RememberResult{}, memory.ErrNotFound
			}
			if err != nil {
				return memory.RememberResult{}, err
			}
		}
		occurred := e.OccurredAt
		if occurred == nil && !e.TimeUnknown {
			occurred = new(now)
		}
		r := memory.Record{
			SpanID:        id,
			TopicID:       topicID,
			ParentID:      parent,
			Space:         input.Space,
			Kind:          e.Kind,
			SchemaVersion: e.SchemaVersion,
			Title:         e.Title,
			Context:       e.Context,
			ExternalRef:   e.ExternalRef,
			Payload:       e.Payload,
			OccurredAt:    occurred,
			RecordedAt:    now,
			Historical:    e.Historical,
			TimeUnknown:   e.TimeUnknown,
			Provenance:    e.Provenance,
			Tags:          e.Tags,
			Writer:        actor,
			RequestKey:    input.Key,
		}
		var parentSQL, occurredSQL any
		if parent != "" {
			parentSQL = parent
		}
		if occurred != nil {
			occurredSQL = formatInstant(*occurred)
		}
		insert, err := tx.ExecContext(ctx, `INSERT INTO nodes(span_id,account_id,space_id,topic_id,parent_id,kind,external_ref,occurred_at,recorded_at) VALUES (?,?,?,?,?,?,?,?,?)`, id, actor.AccountID, spaceID, topicID, parentSQL, e.Kind, e.ExternalRef, occurredSQL, formatInstant(now))
		if err != nil {
			return memory.RememberResult{}, fmt.Errorf("append node %d: %w", i, err)
		}
		r.Sequence, err = insert.LastInsertId()
		if err != nil {
			return memory.RememberResult{}, err
		}
		provenance, _ := json.Marshal(e.Provenance)
		_, err = tx.ExecContext(ctx, `INSERT INTO `+table+`(seq,span_id,topic_id,parent_id,kind,schema_version,title,context,external_ref,payload,occurred_at,recorded_at,historical,time_unknown,provenance,account_id,client_id,request_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.Sequence, id, topicID, parentSQL, e.Kind, e.SchemaVersion, e.Title, e.Context, e.ExternalRef, string(e.Payload), occurredSQL, formatInstant(now), e.Historical, e.TimeUnknown, string(provenance), actor.AccountID, actor.ClientID, input.Key)
		if err != nil {
			return memory.RememberResult{}, err
		}
		for _, tag := range e.Tags {
			if _, err := tx.ExecContext(ctx, "INSERT INTO tags(account_id,span_id,tag) VALUES (?,?,?)", actor.AccountID, id, tag); err != nil {
				return memory.RememberResult{}, err
			}
		}
		for _, link := range e.Links {
			target, err := resolveReference(link.Target, locals)
			if err != nil {
				return memory.RememberResult{}, err
			}
			var found int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM nodes WHERE account_id=? AND span_id=?", actor.AccountID, target).Scan(&found); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return memory.RememberResult{}, memory.ErrNotFound
				}
				return memory.RememberResult{}, err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO links(account_id,source_id,type,target_id) VALUES (?,?,?,?)", actor.AccountID, id, link.Type, target); err != nil {
				return memory.RememberResult{}, err
			}
		}
		reasoning := ""
		if e.Kind == memory.Decision {
			reasoning, _ = decisionText(e.Payload)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO search(rowid,span_id,account_id,title,context,reasoning,external_ref) VALUES (?,?,?,?,?,?,?)", r.Sequence, id, actor.AccountID, e.Title, e.Context, reasoning, e.ExternalRef); err != nil {
			return memory.RememberResult{}, err
		}
		if e.LocalID != "" {
			locals[e.LocalID] = id
		}
		result.Records = append(result.Records, r)
	}
	result.TopicID = topicID
	response, err := json.Marshal(result)
	if err != nil {
		return memory.RememberResult{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO idempotency(account_id,request_key,canonical_request,result) VALUES (?,?,?,?)", actor.AccountID, input.Key, canonical, response); err != nil {
		return memory.RememberResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return memory.RememberResult{}, err
	}
	return result, nil
}

func resolveReference(ref memory.Reference, locals map[string]string) (string, error) {
	if ref.LocalID != "" {
		id, ok := locals[ref.LocalID]
		if !ok {
			return "", invalid("local reference must name an earlier entry")
		}
		return id, nil
	}
	return ref.SpanID, nil
}

func ensureSpace(ctx context.Context, tx *sql.Tx, account, name string, now time.Time) (string, string, error) {
	var id, table string
	err := tx.QueryRowContext(ctx, "SELECT id,table_name FROM spaces WHERE account_id=? AND name=?", account, name).Scan(&id, &table)
	if err == nil {
		quoted, err := quotedTable(table)
		return id, quoted, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM spaces WHERE account_id=?", account).Scan(&count); err != nil {
		return "", "", err
	}
	if count >= memory.MaxSpaces {
		return "", "", invalid("account space limit reached")
	}
	id = newID()
	table = "memory_" + id
	if _, err := tx.ExecContext(ctx, "INSERT INTO spaces(id,account_id,name,table_name,created_at) VALUES (?,?,?,?,?)", id, account, name, table, formatInstant(now)); err != nil {
		return "", "", err
	}
	if err := createArea(ctx, tx, table); err != nil {
		return "", "", err
	}
	quoted, err := quotedTable(table)
	return id, quoted, err
}
