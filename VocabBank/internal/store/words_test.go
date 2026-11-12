package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func setupStore(t *testing.T) (*Store, Actor, Actor, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vocab.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	owner, err := s.Enroll(t.Context(), "https://issuer.example.test", "synthetic-owner", "Synthetic owner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Enroll(t.Context(), "https://issuer.example.test", "synthetic-other", "Other owner")
	if err != nil {
		t.Fatal(err)
	}
	return s, Actor{Owner: owner.ID, Scopes: []string{Read, Write}}, Actor{Owner: other.ID, Scopes: []string{Read, Write}}, path
}
func senses() []Sense {
	return []Sense{
		{PartOfSpeech: "adjective", Definition: "Able to express ideas clearly.", WhenToUse: "Describe a person or explanation that is easy to understand.", Examples: []Example{{Context: "In a meeting", Sentence: "She gave an articulate explanation of the plan."}, {Context: "Discussing a speaker", Sentence: "The articulate presenter answered every question clearly."}}, CommonPhrases: []string{"an articulate speaker", "an articulate explanation"}, UsageTip: "This describes clear expression."},
		{PartOfSpeech: "verb", Definition: "To express an idea clearly in words.", WhenToUse: "Say that someone expresses a thought or feeling.", Examples: []Example{{Context: "Explaining a problem", Sentence: "Can you articulate the problem in one sentence?"}, {Context: "Sharing an opinion", Sentence: "He needed time to articulate his concerns."}}, CommonPhrases: []string{"articulate an idea"}},
		{PartOfSpeech: "verb", Definition: "To connect parts with a movable joint.", WhenToUse: "Describe how connected parts can move.", Examples: []Example{{Context: "Engineering", Sentence: "These sections articulate at the central joint."}, {Context: "Describing a model", Sentence: "The parts articulate to let the model bend."}}, CommonPhrases: []string{"articulate at a joint"}},
	}
}
func add(t *testing.T, s *Store, a Actor, term, key string) Word {
	t.Helper()
	word, err := s.AddWord(t.Context(), a, AddInput{IdempotencyKey: key, Capture: Capture{Term: term, OriginalSentence: "  Original capture.\n", SourceURL: "https://example.test/source", PersonalNote: "Keep my note exactly."}})
	if err != nil {
		t.Fatal(err)
	}
	return word
}

func TestDurableCaptureCompletionAndExactRetries(t *testing.T) {
	s, a, _, path := setupStore(t)
	in := AddInput{IdempotencyKey: "save-word-001", Capture: Capture{Term: "  articulate \t", OriginalSentence: "  Original capture.\n", SourceURL: "https://example.test/source", PersonalNote: "Keep my note exactly."}}
	draft, err := s.AddWord(t.Context(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Term != "articulate" || draft.Status != NeedsDetails || draft.Revision != 1 || len(draft.Senses) != 0 {
		t.Fatalf("draft=%+v", draft)
	}
	completion := CompleteInput{ID: draft.ID, ExpectedRevision: 1, IdempotencyKey: "complete-word-001", Senses: senses()}
	completed, err := s.CompleteWord(t.Context(), a, completion)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != Complete || completed.Revision != 2 || len(completed.Senses) != 3 || completed.OriginalSentence != in.OriginalSentence || completed.PersonalNote != in.PersonalNote || completed.SourceURL != in.SourceURL {
		t.Fatalf("completion lost captures=%+v", completed)
	}
	update := UpdateInput{ID: draft.ID, ExpectedRevision: 2, IdempotencyKey: "edit-note-001", Capture: in.Capture}
	update.PersonalNote = "Changed by the user."
	edited, err := s.UpdateWord(t.Context(), a, update)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != 3 || !reflect.DeepEqual(edited.Senses, completed.Senses) {
		t.Fatal("note edit lost learning")
	}
	replay, err := s.CompleteWord(t.Context(), a, completion)
	if err != nil || !reflect.DeepEqual(replay, completed) {
		t.Fatalf("old successful completion not replayed: %+v %v", replay, err)
	}
	replay, err = s.AddWord(t.Context(), a, in)
	if err != nil || !reflect.DeepEqual(replay, draft) {
		t.Fatalf("old add not replayed: %+v %v", replay, err)
	}
	changed := completion
	changed.Senses = senses()
	changed.Senses[0].Definition = "Different definition."
	if _, err = s.CompleteWord(t.Context(), a, changed); !errors.Is(err, ErrRetryKey) {
		t.Fatalf("key reuse=%v", err)
	}
	changedAdd := in
	changedAdd.Term = "articulate"
	if _, err = s.AddWord(t.Context(), a, changedAdd); !errors.Is(err, ErrRetryKey) {
		t.Fatalf("pre-normalization retry mismatch=%v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, err := reopened.Word(t.Context(), a, draft.ID)
	if err != nil || !reflect.DeepEqual(current, edited) {
		t.Fatalf("durability=%+v %v", current, err)
	}
	replay, err = reopened.CompleteWord(t.Context(), a, completion)
	if err != nil || !reflect.DeepEqual(replay, completed) {
		t.Fatal("retry snapshot not durable")
	}
}

func TestOwnershipScopesDuplicatesAndTermChanges(t *testing.T) {
	s, a, other, _ := setupStore(t)
	word := add(t, s, a, "In\t hindsight", "first-word-001")
	if _, err := s.AddWord(t.Context(), a, AddInput{IdempotencyKey: "duplicate-key-001", Capture: Capture{Term: " in HINDSIGHT "}}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate=%v", err)
	}
	add(t, s, other, "IN HINDSIGHT", "other-word-001")
	if _, err := s.Word(t.Context(), other, word.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross owner read=%v", err)
	}
	if _, err := s.UpdateWord(t.Context(), other, UpdateInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "other-edit-001", Capture: Capture{Term: "hidden"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross owner edit=%v", err)
	}
	if _, err := s.CompleteWord(t.Context(), other, CompleteInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "other-fill-001", Senses: senses()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross owner complete=%v", err)
	}
	for _, actor := range []Actor{{}, {Owner: a.Owner}, {Owner: a.Owner, Scopes: []string{Read}}, {Owner: "unknown", Scopes: []string{Read, Write}}} {
		if _, err := s.AddWord(t.Context(), actor, AddInput{IdempotencyKey: "forbidden-001", Capture: Capture{Term: "denied"}}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("write authorization=%v", err)
		}
	}
	if _, err := s.Words(t.Context(), Actor{Owner: a.Owner, Scopes: []string{Write}}, Filter{}); !errors.Is(err, ErrForbidden) {
		t.Fatal("write-only actor read")
	}
	filled, err := s.CompleteWord(t.Context(), a, CompleteInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "fill-word-001", Senses: senses()})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := s.UpdateWord(t.Context(), a, UpdateInput{ID: word.ID, ExpectedRevision: filled.Revision, IdempotencyKey: "case-word-001", Capture: Capture{Term: "IN HINDSIGHT"}})
	if err != nil || len(edited.Senses) != 3 {
		t.Fatal("case correction cleared senses")
	}
	renamed, err := s.UpdateWord(t.Context(), a, UpdateInput{ID: word.ID, ExpectedRevision: edited.Revision, IdempotencyKey: "rename-word-001", Capture: Capture{Term: "different expression"}})
	if err != nil || len(renamed.Senses) != 0 || renamed.Status != NeedsDetails {
		t.Fatal("term rename retained unrelated learning")
	}
}
func TestMalformedCompletionLeavesPreviousDataIntact(t *testing.T) {
	s, a, _, _ := setupStore(t)
	word := add(t, s, a, "articulate", "draft-word-001")
	bad := map[string]func(*CompleteInput){
		"no senses":          func(in *CompleteInput) { in.Senses = nil },
		"too many senses":    func(in *CompleteInput) { in.Senses = append(senses(), append(senses(), senses()...)...) },
		"empty definition":   func(in *CompleteInput) { in.Senses[0].Definition = " \n" },
		"missing usage":      func(in *CompleteInput) { in.Senses[0].WhenToUse = "" },
		"one example":        func(in *CompleteInput) { in.Senses[0].Examples = in.Senses[0].Examples[:1] },
		"no context":         func(in *CompleteInput) { in.Senses[0].Examples[0].Context = "" },
		"no sentence":        func(in *CompleteInput) { in.Senses[0].Examples[0].Sentence = "" },
		"no phrase":          func(in *CompleteInput) { in.Senses[0].CommonPhrases = nil },
		"blank phrase":       func(in *CompleteInput) { in.Senses[0].CommonPhrases = []string{" "} },
		"unknown pos":        func(in *CompleteInput) { in.Senses[0].PartOfSpeech = "unknown" },
		"long definition":    func(in *CompleteInput) { in.Senses[0].Definition = strings.Repeat("x", 1001) },
		"control characters": func(in *CompleteInput) { in.Senses[0].Definition = "bad\x00text" },
	}
	for name, change := range bad {
		t.Run(name, func(t *testing.T) {
			in := CompleteInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "bad-fill-001", Senses: senses()}
			change(&in)
			if _, err := s.CompleteWord(t.Context(), a, in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid accepted: %v", err)
			}
			current, err := s.Word(t.Context(), a, word.ID)
			if err != nil || !reflect.DeepEqual(current, word) {
				t.Fatal("malformed completion changed draft")
			}
		})
	}
	filled, err := s.CompleteWord(t.Context(), a, CompleteInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "valid-fill-001", Senses: senses()})
	if err != nil {
		t.Fatal(err)
	}
	invalid := CompleteInput{ID: word.ID, ExpectedRevision: 2, IdempotencyKey: "bad-replace-001"}
	if _, err = s.CompleteWord(t.Context(), a, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	current, _ := s.Word(t.Context(), a, word.ID)
	if !reflect.DeepEqual(current, filled) {
		t.Fatal("invalid replacement destroyed previous learning")
	}
}
func TestStaleConcurrentChangesAndDuplicateEditAreAtomic(t *testing.T) {
	s, a, _, _ := setupStore(t)
	word := add(t, s, a, "articulate", "concurrent-word-001")
	other := add(t, s, a, "other", "concurrent-word-002")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			_, err := s.UpdateWord(t.Context(), a, UpdateInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: fmt.Sprintf("concurrent-edit-%d", i), Capture: Capture{Term: word.Term, PersonalNote: fmt.Sprintf("note %d", i)}})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStale) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("concurrent update did not conflict")
	}
	current, _ := s.Word(t.Context(), a, word.ID)
	if _, err := s.CompleteWord(t.Context(), a, CompleteInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "stale-fill-001", Senses: senses()}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if _, err := s.UpdateWord(t.Context(), a, UpdateInput{ID: word.ID, ExpectedRevision: current.Revision, IdempotencyKey: "duplicate-edit-001", Capture: Capture{Term: other.Term}}); !errors.Is(err, ErrDuplicate) {
		t.Fatal(err)
	}
	unchanged, _ := s.Word(t.Context(), a, word.ID)
	if !reflect.DeepEqual(current, unchanged) {
		t.Fatal("failed operation changed data")
	}
}
func TestLiteralSearchBoundedPaginationAndFutureSchema(t *testing.T) {
	s, a, other, path := setupStore(t)
	for i := range 105 {
		add(t, s, a, fmt.Sprintf("Synthetic %03d", i), fmt.Sprintf("pagination-word-%03d", i))
	}
	add(t, s, a, "literal %_ marker", "literal-word-001")
	add(t, s, other, "Synthetic other", "pagination-other-001")
	page, err := s.Words(t.Context(), a, Filter{Query: "SYNTHETIC", Page: 2, PageSize: 100})
	if err != nil || page.Total != 105 || len(page.Items) != 5 || page.Items[0].Term != "Synthetic 100" {
		t.Fatalf("page=%+v %v", page, err)
	}
	literal, err := s.Words(t.Context(), a, Filter{Query: "%_"})
	if err != nil || literal.Total != 1 {
		t.Fatal("wildcards interpreted as SQL")
	}
	draft, err := s.Words(t.Context(), a, Filter{Status: NeedsDetails, PageSize: 30})
	if err != nil || draft.Total != 106 || len(draft.Items) != 30 {
		t.Fatal("draft filter count wrong")
	}
	for _, f := range []Filter{{Page: -1}, {Page: 100001}, {PageSize: 101}, {Status: "bad"}, {Query: strings.Repeat("x", 121)}} {
		if _, err = s.Words(t.Context(), a, f); !errors.Is(err, ErrInvalid) {
			t.Fatal("unbounded filter accepted")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA user_version=2`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if newer, err := Open(path); err == nil {
		newer.Close()
		t.Fatal("future schema opened")
	}
	db, _ = sql.Open("sqlite", path)
	defer db.Close()
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatal("schema version lowered")
	}
}
