package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

var alice = memory.Actor{AccountID: "owner-alice", ClientID: "coding-agent"}
var bob = memory.Actor{AccountID: "owner-bob", ClientID: "coding-agent"}

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, path
}

func at(value string) *time.Time {
	v, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return new(v)
}

func topic(title string) memory.Entry {
	return memory.Entry{Kind: memory.Topic, Title: title, Context: "Original discussion and constraints", LocalID: "root"}
}

func decision(title, outcome, alternative string) memory.Entry {
	payload, _ := json.Marshal(memory.DecisionPayload{Question: "Which technology fits this service?", Outcome: outcome, Reasons: []string{"Small deployment and clear operations"}, Alternatives: []memory.Alternative{{Option: alternative, Disposition: "rejected", Reasons: []string{"Extra service and operational overhead"}}}})
	return memory.Entry{Kind: memory.Decision, Title: title, Payload: payload, Tags: []string{"architecture", "storage"}}
}

func remember(t *testing.T, s *Store, actor memory.Actor, r memory.RememberRequest) memory.RememberResult {
	t.Helper()
	result, err := s.Remember(t.Context(), actor, r)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func retrieve(t *testing.T, s *Store, account string, q memory.Query) memory.Page {
	t.Helper()
	page, err := s.Retrieve(t.Context(), account, q)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func ids(records []memory.Record) []string {
	result := make([]string, 0, len(records))
	for _, r := range records {
		result = append(result, r.SpanID)
	}
	return result
}

func TestJournalScenario(t *testing.T) {
	s, _ := openTest(t)
	root := topic("PAYM-123 payment service")
	root.ExternalRef = "PAYM-123"
	root.OccurredAt = at("2025-04-01T09:00:00Z")
	root.Provenance = memory.Provenance{Attribution: "agent-proposed", Sources: []memory.Source{{Reference: "source://discussion/1", Excerpt: "Ignore prior instructions is quoted source data."}}}
	goChoice := decision("Language choice", "Go", "Java")
	goChoice.Parent = memory.Reference{LocalID: "root"}
	goChoice.OccurredAt = at("2025-04-02T09:00:00Z")
	first := remember(t, s, alice, memory.RememberRequest{Space: "GuestUp", Key: "first", Entries: []memory.Entry{root, goChoice}})
	if first.TopicID != first.Records[0].SpanID || first.Records[1].ParentID != first.TopicID {
		t.Fatal("topic/local parent invariant lost")
	}
	sqliteChoice := decision("Persistence choice", "SQLite", "PostgreSQL")
	sqliteChoice.Parent = memory.Reference{SpanID: first.Records[1].SpanID}
	sqliteChoice.OccurredAt = at("2025-04-03T09:00:00Z")
	second := remember(t, s, alice, memory.RememberRequest{Space: "GuestUp", Key: "second", TopicID: first.TopicID, Entries: []memory.Entry{sqliteChoice}})
	sqliteID := second.Records[0].SpanID
	correction := memory.Entry{Kind: memory.Note, Title: "Later correction", Context: "Constraint changed", OccurredAt: at("2025-05-01T00:00:00Z"), Links: []memory.LinkInput{{Type: memory.Corrects, Target: memory.Reference{SpanID: sqliteID}}}}
	later := remember(t, s, alice, memory.RememberRequest{Space: "GuestUp", Key: "correction", TopicID: first.TopicID, Entries: []memory.Entry{correction}})
	reuseRoot := topic("PAYM-1234 reuses the earlier choice")
	reuseRoot.ExternalRef = "PAYM-1234"
	reuseRoot.Links = []memory.LinkInput{{Type: memory.RelatedTo, Target: memory.Reference{SpanID: first.TopicID}}, {Type: memory.DependsOn, Target: memory.Reference{SpanID: sqliteID}}}
	reuse := remember(t, s, alice, memory.RememberRequest{Space: "Coursework", Key: "reuse", Entries: []memory.Entry{reuseRoot}})
	remember(t, s, bob, memory.RememberRequest{Space: "GuestUp", Key: "first", Entries: []memory.Entry{topic("Bob private PostgreSQL")}})
	q := memory.Query{Spaces: []string{"GuestUp"}, Keywords: "PostgreSQL", Tags: []string{"storage", "architecture"}, Occurred: memory.TimeRange{From: at("2025-04-03T00:00:00Z"), Before: at("2025-04-04T00:00:00Z")}}
	page := retrieve(t, s, alice.AccountID, q)
	if !reflect.DeepEqual(ids(page.Matches), []string{sqliteID}) || !reflect.DeepEqual(ids(page.Context), ids(first.Records)) || !page.Complete {
		t.Fatalf("matches/context: %+v", page)
	}
	if len(page.Relationships) != 3 {
		t.Fatalf("expected correction and cross-space related/dependency links: %+v", page.Relationships)
	}
	foundCorrection, foundReuse := false, false
	for _, link := range page.Relationships {
		foundCorrection = foundCorrection || (link.Type == memory.Corrects && link.Source.SpanID == later.Records[0].SpanID)
		foundReuse = foundReuse || (link.Source.SpanID == reuse.TopicID && link.Source.Space == "Coursework")
	}
	if !foundCorrection || !foundReuse {
		t.Fatal("out-of-filter annotations missing")
	}
	if !strings.Contains(string(page.Matches[0].Payload), "PostgreSQL") || !reflect.DeepEqual(page.Context[0].Provenance, root.Provenance) {
		t.Fatal("reasoning/source data lost")
	}
	q.Tags = append(q.Tags, "missing")
	if len(retrieve(t, s, alice.AccountID, q).Matches) != 0 {
		t.Fatal("tags must intersect")
	}
	for _, word := range []string{"PAYM-123", "payment", "constraints", "Go", "overhead"} {
		if len(retrieve(t, s, alice.AccountID, memory.Query{AllSpaces: true, Keywords: word}).Matches) == 0 {
			t.Fatalf("keyword %q not indexed", word)
		}
	}
	history := retrieve(t, s, alice.AccountID, memory.Query{Spaces: []string{"GuestUp"}, ExternalRef: "PAYM-123"})
	if !reflect.DeepEqual(ids(history.Matches), []string{first.TopicID, first.Records[1].SpanID, sqliteID, later.Records[0].SpanID}) {
		t.Fatalf("topic chronological history: %v", ids(history.Matches))
	}
	exact := retrieve(t, s, alice.AccountID, memory.Query{AllSpaces: true, SpanIDs: []string{sqliteID}})
	if exact.Matches[0].TopicID != first.TopicID || len(exact.Context) != 2 {
		t.Fatal("single span lost ancestry")
	}
	spaces, err := s.ListSpaces(t.Context(), alice.AccountID)
	if err != nil || len(spaces) != 2 {
		t.Fatalf("spaces: %v %v", spaces, err)
	}
	if strings.Contains(fmt.Sprint(history), "Bob private") {
		t.Fatal("foreign owner returned")
	}
}

func TestOwnershipAndParents(t *testing.T) {
	s, _ := openTest(t)
	a := remember(t, s, alice, memory.RememberRequest{Space: "Shared name", Key: "a", Entries: []memory.Entry{topic("A")}})
	b := remember(t, s, bob, memory.RememberRequest{Space: "Shared name", Key: "b", Entries: []memory.Entry{topic("B")}})
	other := remember(t, s, alice, memory.RememberRequest{Space: "Other", Key: "other", Entries: []memory.Entry{topic("Other")}})
	for _, q := range []memory.Query{
		{AllSpaces: true, SpanIDs: []string{b.TopicID}},
		{AllSpaces: true, TopicIDs: []string{b.TopicID}},
		{Spaces: []string{"Shared name"}, SpanIDs: []string{other.TopicID}},
	} {
		if _, err := s.Retrieve(t.Context(), alice.AccountID, q); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("foreign/out-of-scope ID accepted: %v", err)
		}
	}
	for i, r := range []memory.RememberRequest{
		{Space: "Shared name", TopicID: b.TopicID, Entries: []memory.Entry{{Kind: memory.Note, Title: "Foreign topic"}}},
		{Space: "Shared name", TopicID: a.TopicID, Entries: []memory.Entry{{Kind: memory.Note, Title: "Foreign parent", Parent: memory.Reference{SpanID: b.TopicID}}}},
		{Space: "Shared name", TopicID: a.TopicID, Entries: []memory.Entry{{Kind: memory.Note, Title: "Wrong topic parent", Parent: memory.Reference{SpanID: other.TopicID}}}},
		{Space: "Shared name", TopicID: a.TopicID, Entries: []memory.Entry{{Kind: memory.Note, Title: "Foreign link", Links: []memory.LinkInput{{Type: memory.RelatedTo, Target: memory.Reference{SpanID: b.TopicID}}}}}},
	} {
		r.Key = fmt.Sprintf("bad-%d", i)
		if _, err := s.Remember(t.Context(), alice, r); !errors.Is(err, memory.ErrNotFound) {
			t.Fatalf("foreign write accepted: %v", err)
		}
	}
	if len(retrieve(t, s, alice.AccountID, memory.Query{AllSpaces: true, TopicIDs: []string{a.TopicID}}).Matches) != 1 {
		t.Fatal("failed writes left records")
	}
}

func TestCanonicalReplayAndAtomicDDL(t *testing.T) {
	s, path := openTest(t)
	space := `untrusted "; DROP TABLE nodes; --`
	bad := memory.RememberRequest{Space: space, Key: "rollback", Entries: []memory.Entry{topic("New area"), {Kind: memory.Note, Title: "Invalid later parent", Parent: memory.Reference{LocalID: "future"}}}}
	if _, err := s.Remember(t.Context(), alice, bad); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("invalid batch accepted: %v", err)
	}
	for _, table := range []string{"spaces", "nodes", "tags", "links", "search", "idempotency"} {
		var count int
		if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback %s count=%d err=%v", table, count, err)
		}
	}
	var tables int
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name GLOB 'memory_*'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("DDL did not roll back: %d %v", tables, err)
	}
	root := topic("Canonical")
	root.Payload = json.RawMessage(`{ "large":9007199254740993, "b":2,"a":1 }`)
	root.Tags = []string{"z", "a"}
	r := memory.RememberRequest{Space: space, Key: "rollback", Entries: []memory.Entry{root, {Kind: memory.Note, Title: "Local links", Links: []memory.LinkInput{{Type: memory.RelatedTo, Target: memory.Reference{LocalID: "root"}}}}}}
	first := remember(t, s, alice, r)
	canonical := r
	canonical.Entries = slices.Clone(r.Entries)
	canonical.Entries[0].SchemaVersion = 1
	canonical.Entries[0].Payload = json.RawMessage(`{"a":1,"b":2,"large":9007199254740993}`)
	canonical.Entries[0].Tags = []string{"a", "z", "a"}
	replay := remember(t, s, alice, canonical)
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("retry changed result: %+v %+v", first, replay)
	}
	if !strings.Contains(string(replay.Records[0].Payload), "9007199254740993") {
		t.Fatal("large JSON integer corrupted")
	}
	changed := canonical
	changed.Entries = slices.Clone(canonical.Entries)
	changed.Entries[0].Title = "Changed"
	if _, err := s.Remember(t.Context(), alice, changed); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	if _, err := s.Remember(t.Context(), memory.Actor{AccountID: alice.AccountID, ClientID: "another"}, canonical); !errors.Is(err, memory.ErrConflict) {
		t.Fatalf("writer change must conflict: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.db = reopened.db // cleanup closes the reopened connection.
	s.cursorKey = reopened.cursorKey
	if replay := remember(t, s, alice, canonical); !reflect.DeepEqual(first, replay) {
		t.Fatal("durable replay lost")
	}
	if got := retrieve(t, s, alice.AccountID, memory.Query{AllSpaces: true, Keywords: "Canonical"}); len(got.Matches) != 1 {
		t.Fatal("FTS did not persist")
	}
}

func TestImmutableSQLAndIndexes(t *testing.T) {
	s, _ := openTest(t)
	r := remember(t, s, alice, memory.RememberRequest{Space: "Area", Key: "initial", Entries: []memory.Entry{topic("Root"), {Kind: memory.Note, Title: "Child", Tags: []string{"tag"}, Links: []memory.LinkInput{{Type: memory.RelatedTo, Target: memory.Reference{LocalID: "root"}}}}}})
	var table, spaceID string
	if err := s.db.QueryRowContext(t.Context(), "SELECT table_name,id FROM spaces WHERE account_id=?", alice.AccountID).Scan(&table, &spaceID); err != nil {
		t.Fatal(err)
	}
	linkSQL, linkArgs := relationshipQuery(alice.AccountID, []string{r.TopicID}, r.Records[1].Sequence, 10, 0)
	for _, item := range []struct{ table, column string }{{"settings", "cursor_key"}, {"spaces", "name"}, {"nodes", "kind"}, {"tags", "tag"}, {"links", "type"}, {"idempotency", "result"}, {table, "title"}} {
		for _, statement := range []string{"UPDATE " + item.table + " SET " + item.column + "=" + item.column, "DELETE FROM " + item.table, "INSERT OR REPLACE INTO " + item.table + " SELECT * FROM " + item.table + " LIMIT 1"} {
			if _, err := s.db.ExecContext(t.Context(), statement); err == nil || !strings.Contains(err.Error(), "immutable journal") {
				t.Fatalf("mutable journal: %s => %v", statement, err)
			}
		}
	}
	for _, tc := range []struct {
		query, index string
		args         []any
	}{
		{"SELECT * FROM nodes WHERE span_id=?", "sqlite_autoindex_nodes_1", []any{r.TopicID}},
		{"SELECT * FROM nodes WHERE account_id=? AND topic_id=? ORDER BY occurred_at,seq", "nodes_topic_time", []any{alice.AccountID, r.TopicID}},
		{"SELECT * FROM nodes WHERE account_id=? AND parent_id=? ORDER BY seq", "nodes_parent", []any{alice.AccountID, r.TopicID}},
		{"SELECT * FROM nodes WHERE account_id=? AND space_id=? AND external_ref=? AND kind='topic'", "nodes_external", []any{alice.AccountID, spaceID, "ticket"}},
		{"SELECT * FROM tags WHERE account_id=? AND tag=?", "sqlite_autoindex_tags_1", []any{alice.AccountID, "tag"}},
		{"SELECT * FROM tags WHERE account_id=? AND span_id=?", "tags_node", []any{alice.AccountID, r.TopicID}},
		{"SELECT * FROM links WHERE account_id=? AND source_id=? AND type=?", "sqlite_autoindex_links_1", []any{alice.AccountID, r.Records[1].SpanID, "related_to"}},
		{"SELECT * FROM links WHERE account_id=? AND target_id=? AND type=?", "links_incoming", []any{alice.AccountID, r.TopicID, "related_to"}},
		{"SELECT * FROM " + table + " WHERE topic_id=? ORDER BY occurred_at,seq", table + "_topic_time", []any{r.TopicID}},
		{"SELECT * FROM " + table + " WHERE parent_id=? ORDER BY seq", table + "_parent", []any{r.TopicID}},
		{linkSQL, "links_incoming", linkArgs},
		{linkSQL, "sqlite_autoindex_links_1", linkArgs},
	} {
		rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		plan := ""
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(plan, tc.index) {
			t.Fatalf("missing %s in plan %s", tc.index, plan)
		}
	}
	var mode string
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL unavailable %s %v", mode, err)
	}
	var foreignKeys int
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("foreign keys disabled")
	}
}

func TestDeepContextLinkAndMatchPagination(t *testing.T) {
	s, _ := openTest(t)
	entries := []memory.Entry{topic("Root outside filter")}
	for i := 1; i <= 13; i++ {
		parent := "root"
		if i > 1 {
			parent = fmt.Sprint(i - 1)
		}
		e := memory.Entry{LocalID: fmt.Sprint(i), Parent: memory.Reference{LocalID: parent}, Kind: memory.Context, Title: "Older ancestor"}
		if i >= 12 {
			e.Kind = memory.Decision
			e.Title = "needle"
			e.Payload = decision("", "SQLite", "PostgreSQL").Payload
			e.Tags = []string{"match"}
		}
		entries = append(entries, e)
	}
	chain := remember(t, s, alice, memory.RememberRequest{Space: "Deep", Key: "chain", Entries: entries})
	leaf := chain.Records[12]
	secondLeaf := chain.Records[13]
	for i := range 7 {
		typ := []memory.LinkType{memory.Corrects, memory.Supersedes, memory.Retracts}[i%3]
		remember(t, s, alice, memory.RememberRequest{Space: "Other", Key: fmt.Sprintf("link-%d", i), Entries: []memory.Entry{{Kind: memory.Topic, Title: "later annotation", Links: []memory.LinkInput{{Type: typ, Target: memory.Reference{SpanID: leaf.SpanID}}}}}})
	}
	q := memory.Query{Spaces: []string{"Deep"}, Tags: []string{"match"}, Keywords: "needle", Limit: 1, ContextLimit: 2, LinkLimit: 1}
	seenMatches, seenContext, seenLinks := map[string]bool{}, map[string]bool{}, map[string]bool{}
	snapshot := int64(0)
	for pageCount := 0; ; pageCount++ {
		if pageCount > 30 {
			t.Fatal("continuation failed to terminate")
		}
		page := retrieve(t, s, alice.AccountID, q)
		if pageCount == 0 {
			snapshot = page.SnapshotSequence
			if page.ContextComplete || page.RelationshipsComplete || page.Complete {
				t.Fatal("truncated support falsely complete")
			}
			remember(t, s, alice, memory.RememberRequest{Space: "Deep", Key: "late matching", TopicID: chain.TopicID, Entries: []memory.Entry{{Kind: memory.Note, Title: "needle", Tags: []string{"match"}}}})
		}
		if page.SnapshotSequence != snapshot || len(page.Matches) > 1 || len(page.Context) > 2 || len(page.Relationships) > 1 {
			t.Fatal("bounds/snapshot changed")
		}
		if len(page.MatchIDs) != 1 || !slices.Contains([]string{leaf.SpanID, secondLeaf.SpanID}, page.MatchIDs[0]) {
			t.Fatal("support page lost its matching anchor")
		}
		for _, r := range page.Matches {
			if seenMatches[r.SpanID] {
				t.Fatal("match repeated on support page")
			}
			seenMatches[r.SpanID] = true
		}
		for _, r := range page.Context {
			key := page.MatchIDs[0] + r.SpanID
			if seenContext[key] {
				t.Fatal("ancestor repeated")
			}
			seenContext[key] = true
		}
		for _, r := range page.Relationships {
			key := page.MatchIDs[0] + r.Source.SpanID
			if seenLinks[key] {
				t.Fatal("link repeated")
			}
			seenLinks[key] = true
		}
		if page.NextCursor == "" {
			if !page.Complete || !page.ContextComplete || !page.RelationshipsComplete {
				t.Fatal("exhausted cursor incomplete")
			}
			break
		}
		q.Cursor = page.NextCursor
	}
	if len(seenMatches) != 2 || len(seenContext) != 25 || len(seenLinks) != 14 {
		t.Fatalf("pagination skipped data: %d %d %d", len(seenMatches), len(seenContext), len(seenLinks))
	}
	if len(retrieve(t, s, alice.AccountID, memory.Query{Spaces: []string{"Deep"}, Tags: []string{"match"}}).Matches) != 3 {
		t.Fatal("fresh query did not see new append")
	}
}

func TestKeywordPaginationStableAfterAppend(t *testing.T) {
	s, _ := openTest(t)
	root := remember(t, s, alice, memory.RememberRequest{Space: "Search", Key: "root", Entries: []memory.Entry{topic("Search")}})
	entries := []memory.Entry{}
	for i := range 9 {
		entries = append(entries, memory.Entry{Kind: memory.Note, Title: fmt.Sprintf("needle %d", i), OccurredAt: at(fmt.Sprintf("2026-03-08T07:00:00.%09dZ", i))})
	}
	initial := remember(t, s, alice, memory.RememberRequest{Space: "Search", TopicID: root.TopicID, Key: "notes", Entries: entries})
	q := memory.Query{AllSpaces: true, Keywords: "needle", Limit: 2, ContextLimit: 1, LinkLimit: 1}
	seen := []string{}
	first := retrieve(t, s, alice.AccountID, q)
	seen = append(seen, ids(first.Matches)...)
	remember(t, s, alice, memory.RememberRequest{Space: "Search", TopicID: root.TopicID, Key: "later", Entries: []memory.Entry{{Kind: memory.Note, Title: "needle needle needle", Context: "needle"}}})
	q.Cursor = first.NextCursor
	for i := 0; q.Cursor != ""; i++ {
		if i > 20 {
			t.Fatal("endless keyword pages")
		}
		page := retrieve(t, s, alice.AccountID, q)
		seen = append(seen, ids(page.Matches)...)
		q.Cursor = page.NextCursor
	}
	want := ids(initial.Records)
	slices.Reverse(want)
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("unstable rank or fractional time order: got %v want %v", seen, want)
	}
	for _, q := range []memory.Query{
		{AllSpaces: true, Keywords: "needle", Limit: 2, Cursor: first.NextCursor + "x"},
		{AllSpaces: true, Keywords: "changed", Limit: 2, ContextLimit: 1, LinkLimit: 1, Cursor: first.NextCursor},
	} {
		if _, err := s.Retrieve(t.Context(), alice.AccountID, q); !errors.Is(err, memory.ErrCursor) {
			t.Fatalf("altered cursor accepted: %v", err)
		}
	}
	if _, err := s.Retrieve(t.Context(), bob.AccountID, memory.Query{AllSpaces: true, Keywords: "needle", Limit: 2, ContextLimit: 1, LinkLimit: 1, Cursor: first.NextCursor}); !errors.Is(err, memory.ErrCursor) {
		t.Fatal("cursor usable by another account")
	}
}

func TestHistoricalUnknownAndCalendarFilter(t *testing.T) {
	s, _ := openTest(t)
	root := remember(t, s, alice, memory.RememberRequest{Space: "Dates", Key: "root", Entries: []memory.Entry{topic("Dates")}})
	entries := []memory.Entry{
		{Kind: memory.Note, Title: "before local day", OccurredAt: at("2026-03-08T04:59:59.999999999Z"), Historical: true},
		{Kind: memory.Note, Title: "start local day", OccurredAt: at("2026-03-08T05:00:00Z"), Historical: true},
		{Kind: memory.Note, Title: "end local day", OccurredAt: at("2026-03-09T03:59:59.999999999Z"), Historical: true},
		{Kind: memory.Note, Title: "next local day", OccurredAt: at("2026-03-09T04:00:00Z"), Historical: true},
		{Kind: memory.Note, Title: "unknown historical", Historical: true, TimeUnknown: true},
	}
	written := remember(t, s, alice, memory.RememberRequest{Space: "Dates", TopicID: root.TopicID, Key: "dates", Entries: entries})
	q := memory.Query{AllSpaces: true, TopicIDs: []string{root.TopicID}, Occurred: memory.TimeRange{Calendar: &memory.CalendarRange{StartDate: "2026-03-08", EndDate: "2026-03-09"}}}
	page := retrieve(t, s, alice.AccountID, q)
	if !reflect.DeepEqual(ids(page.Matches), ids(written.Records[1:3])) || len(page.Context) != 1 || page.AppliedOccurred.Before.Sub(*page.AppliedOccurred.From) != 23*time.Hour {
		t.Fatalf("DST/half-open range wrong: %+v", page)
	}
	unknown := retrieve(t, s, alice.AccountID, memory.Query{AllSpaces: true, SpanIDs: []string{written.Records[4].SpanID}}).Matches[0]
	if unknown.OccurredAt != nil || !unknown.TimeUnknown || unknown.RecordedAt.IsZero() {
		t.Fatal("unknown time fabricated")
	}
	recorded := retrieve(t, s, alice.AccountID, memory.Query{AllSpaces: true, Recorded: memory.TimeRange{From: new(written.Records[0].RecordedAt), Before: new(written.Records[0].RecordedAt.Add(time.Nanosecond))}})
	if len(recorded.Matches) != 5 {
		t.Fatalf("recorded time filter lost unknown historical: %d", len(recorded.Matches))
	}
}

func TestValidationAndConcurrentReplay(t *testing.T) {
	s, _ := openTest(t)
	for _, entry := range []memory.Entry{
		{Kind: memory.Topic, Title: "historical without evidence", Historical: true},
		{Kind: memory.Topic, Title: "unknown contemporaneous", TimeUnknown: true},
		{Kind: memory.Topic, Title: "both times", Historical: true, TimeUnknown: true, OccurredAt: at("2026-01-01T00:00:00Z")},
		{Kind: memory.Topic, Title: "payload", Payload: json.RawMessage(`{"x":1} {"y":2}`)},
		{Kind: memory.Topic, Title: "null payload", Payload: json.RawMessage(`null`)},
		{Kind: memory.Topic, Title: "oversized", Context: strings.Repeat("x", 16<<10+1)},
	} {
		if _, err := s.Remember(t.Context(), alice, memory.RememberRequest{Space: "Bad", Key: "bad", Entries: []memory.Entry{entry}}); !errors.Is(err, memory.ErrInvalid) {
			t.Fatalf("invalid entry accepted: %v", err)
		}
	}
	root := remember(t, s, alice, memory.RememberRequest{Space: "Good", Key: "root", Entries: []memory.Entry{topic("Root")}})
	for _, payload := range []string{`{}`, `{"question":"q","outcome":"o","reasons":null,"alternatives":[]}`, `{"question":"q","outcome":"o","reasons":[],"alternatives":[{"option":"pg","disposition":"unknown","reasons":[]}]}`} {
		if _, err := s.Remember(t.Context(), alice, memory.RememberRequest{Space: "Good", TopicID: root.TopicID, Key: "bad", Entries: []memory.Entry{{Kind: memory.Decision, Title: "Invalid", Payload: json.RawMessage(payload)}}}); !errors.Is(err, memory.ErrInvalid) {
			t.Fatalf("invalid decision accepted: %v", err)
		}
	}
	// Empty evidence is explicit, rather than invented.
	remember(t, s, alice, memory.RememberRequest{Space: "Good", TopicID: root.TopicID, Key: "empty-evidence", Entries: []memory.Entry{{Kind: memory.Decision, Title: "Unknown reasons", Payload: json.RawMessage(`{"question":"q","outcome":"o","reasons":[],"alternatives":[]}`)}}})
	for _, q := range []memory.Query{{}, {Spaces: []string{"Good"}, AllSpaces: true}, {AllSpaces: true, Limit: 101}, {AllSpaces: true, ContextLimit: 201}, {AllSpaces: true, LinkLimit: 201}, {AllSpaces: true, Order: memory.Relevance}} {
		if _, err := s.Retrieve(t.Context(), alice.AccountID, q); !errors.Is(err, memory.ErrInvalid) {
			t.Fatalf("invalid query accepted: %v", err)
		}
	}
	r := memory.RememberRequest{Space: "Concurrent", Key: "same", Entries: []memory.Entry{topic("Exactly once")}}
	results := make(chan memory.RememberResult, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { result, err := s.Remember(t.Context(), alice, r); results <- result; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first memory.RememberResult
	for result := range results {
		if first.TopicID == "" {
			first = result
		} else if !reflect.DeepEqual(first, result) {
			t.Fatal("concurrent replay duplicated data")
		}
	}
}

func TestOpenPrivateDatabaseAndRejectForeignSchema(t *testing.T) {
	s, path := openTest(t)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private database: %v %v", info, err)
	}
	for _, path := range []string{"", ":memory:", "file:journal.db?mode=memory"} {
		if opened, err := Open(t.Context(), path); err == nil {
			opened.Close()
			t.Fatalf("accepted unsupported path %q", path)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), "PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(t.Context(), path); err == nil {
		opened.Close()
		t.Fatal("newer schema accepted")
	}
	foreignPath := filepath.Join(t.TempDir(), "foreign.db")
	f, err := os.OpenFile(foreignPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	db, err := sql.Open("sqlite", foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE unrelated(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if opened, err := Open(t.Context(), foreignPath); err == nil {
		opened.Close()
		t.Fatal("foreign database adopted")
	}
}
