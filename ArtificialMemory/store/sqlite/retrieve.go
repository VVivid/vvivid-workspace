package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

type rowRef struct{ ID, Space, Table string }

const refColumns = "n.span_id,s.name,s.table_name"
const registryJoin = " FROM nodes n JOIN spaces s ON s.id=n.space_id "

func (s *Store) Retrieve(ctx context.Context, account string, input memory.Query) (memory.Page, error) {
	if err := validIdentity(account); err != nil {
		return memory.Page{}, err
	}
	q, err := normalizeQuery(input)
	if err != nil {
		return memory.Page{}, err
	}
	hash := queryHash(account, q)
	c := continuation{Version: 1, QueryHash: hash}
	if q.Cursor != "" {
		c, err = s.decodeCursor(q.Cursor, hash)
		if err != nil {
			return memory.Page{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return memory.Page{}, err
	}
	defer tx.Rollback()
	if q.Cursor == "" {
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM nodes WHERE account_id=?", account).Scan(&c.Snapshot); err != nil {
			return memory.Page{}, err
		}
	}
	if err := validateQueryReferences(ctx, tx, account, q, c.Snapshot); err != nil {
		return memory.Page{}, err
	}
	statement, args := matchQuery(account, q, c)
	matched, err := selectRefs(ctx, tx, statement, args...)
	if err != nil {
		return memory.Page{}, err
	}
	moreMatches := len(matched) > q.Limit
	if moreMatches {
		matched = matched[:q.Limit]
	}
	ids := make([]string, 0, len(matched))
	for _, ref := range matched {
		ids = append(ids, ref.ID)
	}
	page := memory.Page{
		Matches:               []memory.Record{},
		MatchIDs:              ids,
		Context:               []memory.Record{},
		Relationships:         []memory.Relationship{},
		SnapshotSequence:      c.Snapshot,
		AppliedOccurred:       q.Occurred,
		AppliedRecorded:       q.Recorded,
		MatchesComplete:       !moreMatches,
		ContextComplete:       true,
		RelationshipsComplete: true,
	}
	var contextual []rowRef
	if len(ids) > 0 {
		if c.ContextOffset >= 0 {
			query, args := ancestorQuery(account, ids, q.ContextLimit+1, c.ContextOffset)
			contextual, err = selectRefs(ctx, tx, query, args...)
			if err != nil {
				return memory.Page{}, err
			}
			page.ContextComplete = len(contextual) <= q.ContextLimit
			if !page.ContextComplete {
				contextual = contextual[:q.ContextLimit]
			}
		}
		if c.LinkOffset >= 0 {
			page.Relationships, err = selectRelationships(ctx, tx, account, ids, c.Snapshot, q.LinkLimit+1, c.LinkOffset)
			if err != nil {
				return memory.Page{}, err
			}
			page.RelationshipsComplete = len(page.Relationships) <= q.LinkLimit
			if !page.RelationshipsComplete {
				page.Relationships = page.Relationships[:q.LinkLimit]
			}
		}
	}
	toLoad := slices.Clone(contextual)
	if !c.SupportOnly {
		toLoad = append(toLoad, matched...)
	}
	records, err := loadRecords(ctx, tx, account, toLoad)
	if err != nil {
		return memory.Page{}, err
	}
	if !c.SupportOnly {
		for _, ref := range matched {
			page.Matches = append(page.Matches, records[ref.ID])
		}
	}
	for _, ref := range contextual {
		page.Context = append(page.Context, records[ref.ID])
	}
	if !page.ContextComplete || !page.RelationshipsComplete {
		// Keep the same match page until both support streams are exhausted.
		c.SupportOnly = true
		if page.ContextComplete {
			c.ContextOffset = exhaustedSupport
		} else {
			c.ContextOffset += int64(len(contextual))
		}
		if page.RelationshipsComplete {
			c.LinkOffset = exhaustedSupport
		} else {
			c.LinkOffset += int64(len(page.Relationships))
		}
		page.NextCursor = s.encodeCursor(c)
	} else if moreMatches {
		// Start both support streams afresh for the next match page.
		c.MatchOffset += int64(len(matched))
		c.ContextOffset, c.LinkOffset, c.SupportOnly = 0, 0, false
		page.NextCursor = s.encodeCursor(c)
	}
	page.Complete = page.NextCursor == ""
	if err := tx.Commit(); err != nil {
		return memory.Page{}, err
	}
	return page, nil
}

func validateQueryReferences(ctx context.Context, tx *sql.Tx, account string, q memory.Query, snapshot int64) error {
	for _, set := range []struct {
		ids   []string
		topic bool
	}{{q.TopicIDs, true}, {q.SpanIDs, false}} {
		if len(set.ids) == 0 {
			continue
		}
		query := "SELECT count(*)" + registryJoin + "WHERE n.account_id=? AND n.seq<=? AND n.span_id IN (" + placeholders(len(set.ids)) + ")"
		args := []any{account, snapshot}
		for _, id := range set.ids {
			args = append(args, id)
		}
		if set.topic {
			query += " AND n.kind='topic'"
		}
		if !q.AllSpaces {
			query += " AND s.name IN (" + placeholders(len(q.Spaces)) + ")"
			for _, space := range q.Spaces {
				args = append(args, space)
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			return err
		}
		if count != len(set.ids) {
			return memory.ErrNotFound
		}
	}
	return nil
}

func matchQuery(account string, q memory.Query, c continuation) (string, []any) {
	join := registryJoin
	whereArgs := []any{account, c.Snapshot}
	var orderArgs []any
	where := []string{"n.account_id=?", "n.seq<=?"}
	addSet := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		where = append(where, column+" IN ("+placeholders(len(values))+")")
		for _, value := range values {
			whereArgs = append(whereArgs, value)
		}
	}
	if !q.AllSpaces {
		addSet("s.name", q.Spaces)
	}
	addSet("n.topic_id", q.TopicIDs)
	addSet("n.span_id", q.SpanIDs)
	if len(q.Kinds) > 0 {
		kinds := make([]string, 0, len(q.Kinds))
		for _, k := range q.Kinds {
			kinds = append(kinds, string(k))
		}
		addSet("n.kind", kinds)
	}
	if q.ExternalRef != "" {
		where = append(where, `n.topic_id IN (SELECT span_id FROM nodes root WHERE root.account_id=n.account_id AND root.space_id=n.space_id AND root.kind='topic' AND root.external_ref=?)`)
		whereArgs = append(whereArgs, q.ExternalRef)
	}
	if len(q.Tags) > 0 {
		where = append(where, `n.span_id IN (SELECT span_id FROM tags WHERE account_id=? AND tag IN (`+placeholders(len(q.Tags))+`) GROUP BY span_id HAVING count(*)=?)`)
		whereArgs = append(whereArgs, account)
		for _, tag := range q.Tags {
			whereArgs = append(whereArgs, tag)
		}
		whereArgs = append(whereArgs, len(q.Tags))
	}
	for _, bound := range []struct {
		column string
		value  memory.TimeRange
	}{{"n.occurred_at", q.Occurred}, {"n.recorded_at", q.Recorded}} {
		if bound.value.From != nil {
			where = append(where, bound.column+">=?")
			whereArgs = append(whereArgs, formatInstant(*bound.value.From))
		}
		if bound.value.Before != nil {
			where = append(where, bound.column+"<?")
			whereArgs = append(whereArgs, formatInstant(*bound.value.Before))
		}
	}
	order := "COALESCE(n.occurred_at,n.recorded_at) DESC,n.seq DESC"
	if q.Order == memory.Chronological {
		order = "COALESCE(n.occurred_at,n.recorded_at),n.seq"
	}
	if q.Keywords != "" {
		join += " JOIN search ON search.rowid=n.seq "
		phrases := []string{}
		for term := range strings.FieldsSeq(q.Keywords) {
			phrases = append(phrases, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		}
		where = append(where, "search MATCH ?")
		whereArgs = append(whereArgs, strings.Join(phrases, " AND "))
		if q.Order == memory.Relevance {
			// Corpus-dependent BM25 would reorder old pages after an append.
			// Field term presence is stable for each immutable document.
			score := []string{}
			for term := range strings.FieldsSeq(q.Keywords) {
				for _, field := range []struct {
					name   string
					weight string
				}{{"title", "4"}, {"external_ref", "4"}, {"context", "2"}, {"reasoning", "1"}} {
					score = append(score, "CASE WHEN instr(lower(search."+field.name+"),lower(?))>0 THEN "+field.weight+" ELSE 0 END")
					orderArgs = append(orderArgs, term)
				}
			}
			order = "(" + strings.Join(score, "+") + ") DESC," + order
		}
	}
	// Placeholder order follows the SQL clauses, regardless of when a filter
	// or ordering expression was assembled above.
	args := append(whereArgs, orderArgs...)
	args = append(args, q.Limit+1, c.MatchOffset)
	return "SELECT " + refColumns + join + " WHERE " + strings.Join(where, " AND ") + " ORDER BY " + order + " LIMIT ? OFFSET ?", args
}

// One recursive query walks the union of all primary ancestor chains. UNION
// deduplicates shared ancestors; no application query is issued per node.
// Match IDs are snapshot-bounded; immutable parents precede their children.
func anchorCTE(ids []string) string {
	return `WITH RECURSIVE anchors(span_id) AS (
 SELECT span_id FROM nodes WHERE account_id=? AND span_id IN (` + placeholders(len(ids)) + `)
 UNION SELECT n.parent_id FROM nodes n JOIN anchors a ON n.span_id=a.span_id WHERE n.parent_id IS NOT NULL
) `
}

func anchorArgs(account string, ids []string) []any {
	args := []any{account}
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

func ancestorQuery(account string, ids []string, limit int, offset int64) (string, []any) {
	query := anchorCTE(ids) + "SELECT " + refColumns + registryJoin + "WHERE n.account_id=? AND n.span_id IN (SELECT span_id FROM anchors) AND n.span_id NOT IN (" + placeholders(len(ids)) + ") ORDER BY n.seq LIMIT ? OFFSET ?"
	args := append(anchorArgs(account, ids), account)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, limit, offset)
	return query, args
}

func selectRefs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]rowRef, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []rowRef{}
	for rows.Next() {
		var ref rowRef
		if err := rows.Scan(&ref.ID, &ref.Space, &ref.Table); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func selectRelationships(ctx context.Context, tx *sql.Tx, account string, ids []string, snapshot int64, limit int, offset int64) ([]memory.Relationship, error) {
	query, args := relationshipQuery(account, ids, snapshot, limit, offset)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	relations := []memory.Relationship{}
	for rows.Next() {
		var r memory.Relationship
		if err := rows.Scan(&r.Type, &r.Source.SpanID, &r.Source.TopicID, &r.Source.Space, &r.Target.SpanID, &r.Target.TopicID, &r.Target.Space); err != nil {
			return nil, err
		}
		relations = append(relations, r)
	}
	return relations, rows.Err()
}

func relationshipQuery(account string, ids []string, snapshot int64, limit int, offset int64) (string, []any) {
	// Both directions include corrections/retractions that do not match the
	// original tags/keywords/dates, plus explicit related/dependency references.
	// Separate indexed branches avoid scanning an account's complete link graph.
	// Pin the incoming index: UNION's sort preference can otherwise choose an
	// account-wide source-order scan rather than the target lookup.
	query := strings.TrimSuffix(anchorCTE(ids), " ") + `, relevant(source_id,type,target_id) AS (
 SELECT source_id,type,target_id FROM links WHERE account_id=? AND source_id IN (SELECT span_id FROM anchors)
 UNION SELECT source_id,type,target_id FROM links INDEXED BY links_incoming WHERE account_id=? AND target_id IN (SELECT span_id FROM anchors)
 ) SELECT l.type,src.span_id,src.topic_id,ss.name,dst.span_id,dst.topic_id,ds.name
 FROM relevant l JOIN nodes src ON src.span_id=l.source_id JOIN spaces ss ON ss.id=src.space_id
 JOIN nodes dst ON dst.span_id=l.target_id JOIN spaces ds ON ds.id=dst.space_id
 WHERE src.seq<=?
 ORDER BY src.seq,l.source_id,l.type,l.target_id LIMIT ? OFFSET ?`
	args := append(anchorArgs(account, ids), account, account, snapshot, limit, offset)
	return query, args
}

func loadRecords(ctx context.Context, tx *sql.Tx, account string, refs []rowRef) (map[string]memory.Record, error) {
	byArea := map[string][]rowRef{}
	for _, ref := range refs {
		byArea[ref.Table] = append(byArea[ref.Table], ref)
	}
	records := map[string]memory.Record{}
	for table, areaRefs := range byArea {
		quoted, err := quotedTable(table)
		if err != nil {
			return nil, err
		}
		args := []any{account}
		for _, ref := range areaRefs {
			args = append(args, ref.ID)
		}
		rows, err := tx.QueryContext(ctx, `SELECT seq,span_id,topic_id,parent_id,kind,schema_version,title,context,external_ref,payload,occurred_at,recorded_at,historical,time_unknown,provenance,client_id,request_key FROM `+quoted+` WHERE account_id=? AND span_id IN (`+placeholders(len(areaRefs))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r, err := scanRecord(rows, account, areaRefs[0].Space)
			if err != nil {
				rows.Close()
				return nil, err
			}
			records[r.SpanID] = r
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(refs) == 0 {
		return records, nil
	}
	args := []any{account}
	for _, ref := range refs {
		args = append(args, ref.ID)
	}
	rows, err := tx.QueryContext(ctx, "SELECT span_id,tag FROM tags WHERE account_id=? AND span_id IN ("+placeholders(len(refs))+") ORDER BY span_id,tag", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return nil, err
		}
		r, ok := records[id]
		if !ok {
			return nil, errors.New("tag references missing record")
		}
		if len(r.Tags) >= memory.MaxTags {
			return nil, errors.New("stored tags exceed journal bounds")
		}
		r.Tags = append(r.Tags, tag)
		records[id] = r
	}
	if len(records) != len(refs) {
		return nil, fmt.Errorf("registry references missing records")
	}
	return records, rows.Err()
}

func scanRecord(rows *sql.Rows, account, space string) (memory.Record, error) {
	r := memory.Record{Space: space, Writer: memory.Actor{AccountID: account}}
	var parent, occurred sql.NullString
	var recorded, payload, provenance string
	err := rows.Scan(&r.Sequence, &r.SpanID, &r.TopicID, &parent, &r.Kind, &r.SchemaVersion, &r.Title, &r.Context, &r.ExternalRef, &payload, &occurred, &recorded, &r.Historical, &r.TimeUnknown, &provenance, &r.Writer.ClientID, &r.RequestKey)
	if err != nil {
		return r, err
	}
	r.ParentID = parent.String
	r.Payload = json.RawMessage(payload)
	if err := json.Unmarshal([]byte(provenance), &r.Provenance); err != nil {
		return r, err
	}
	r.RecordedAt, err = time.Parse(instantFormat, recorded)
	if err != nil {
		return r, err
	}
	if occurred.Valid {
		at, err := time.Parse(instantFormat, occurred.String)
		if err != nil {
			return r, err
		}
		r.OccurredAt = new(at)
	}
	return r, nil
}
