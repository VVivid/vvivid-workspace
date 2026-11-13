package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func batchItem(term, key string, content []Sense) AddWordsItem {
	return AddWordsItem{AddInput: AddInput{Capture: Capture{Term: term, OriginalSentence: "  Synthetic captured sentence.\n", SourceURL: "https://example.test/synthetic", PersonalNote: "Keep this synthetic note."}, IdempotencyKey: key}, Senses: content}
}

func TestBatchMixedCreationDuplicatesAndPartialSuccess(t *testing.T) {
	s, a, _, _ := setupStore(t)
	existing := add(t, s, a, "existing synthetic", "existing-batch-001")
	existing, err := s.CompleteWord(t.Context(), a, CompleteInput{ID: existing.ID, ExpectedRevision: 1, IdempotencyKey: "existing-fill-001", Senses: senses()})
	if err != nil {
		t.Fatal(err)
	}
	in := AddWordsInput{Words: []AddWordsItem{
		batchItem("  synthetic   complete  ", "batch-complete-001", senses()),
		batchItem("synthetic draft", "batch-draft-001", nil),
		batchItem("   ", "batch-invalid-001", nil),
		batchItem(" EXISTING SYNTHETIC ", "batch-existing-001", senses()),
		batchItem("SYNTHETIC COMPLETE", "batch-duplicate-001", nil),
		batchItem("synthetic broken learning", "batch-broken-001", []Sense{{PartOfSpeech: "noun"}}),
		batchItem("synthetic bad key", "short", nil),
		batchItem("synthetic empty senses", "batch-empty-001", []Sense{}),
	}}
	// Repeating an exact item/key replays it even within one batch; changing
	// that successful key's input conflicts without affecting other items.
	in.Words = append(in.Words, in.Words[0], batchItem("different synthetic intent", in.Words[0].IdempotencyKey, nil))
	results, err := s.AddWords(t.Context(), a, in)
	if err != nil || len(results) != len(in.Words) {
		t.Fatalf("batch=%+v %v", results, err)
	}
	wantErrors := []error{nil, nil, ErrInvalid, ErrDuplicate, ErrDuplicate, ErrInvalid, ErrInvalid, nil, nil, ErrRetryKey}
	for i, result := range results {
		if !errors.Is(result.Err, wantErrors[i]) {
			t.Fatalf("item %d error=%v want %v", i, result.Err, wantErrors[i])
		}
		if result.Err != nil && result.Word.ID != "" {
			t.Fatalf("failed item %d returned a word", i)
		}
	}
	if !reflect.DeepEqual(results[8].Word, results[0].Word) {
		t.Fatal("same-batch exact retry changed its snapshot")
	}
	complete := results[0].Word
	if complete.Term != "synthetic complete" || complete.Status != Complete || complete.Revision != 1 || !reflect.DeepEqual(complete.Senses, in.Words[0].Senses) || complete.CreatedAt != complete.UpdatedAt {
		t.Fatalf("complete was not created in its final state: %+v", complete)
	}
	for _, i := range []int{1, 7} {
		word := results[i].Word
		if word.Status != NeedsDetails || word.Revision != 1 || word.Senses == nil || len(word.Senses) != 0 {
			t.Fatalf("draft %d=%+v", i, word)
		}
	}
	for _, i := range []int{0, 1, 7} {
		word := results[i].Word
		if word.OriginalSentence != in.Words[i].OriginalSentence || word.PersonalNote != in.Words[i].PersonalNote || word.SourceURL != in.Words[i].SourceURL {
			t.Fatalf("item %d lost capture fields", i)
		}
	}
	unchanged, err := s.Word(t.Context(), a, existing.ID)
	if err != nil || !reflect.DeepEqual(unchanged, existing) {
		t.Fatal("duplicate overwrote existing captures or learning")
	}
	first, err := s.Word(t.Context(), a, complete.ID)
	if err != nil || !reflect.DeepEqual(first, complete) {
		t.Fatal("within-batch duplicate overwrote the first item")
	}
	page, err := s.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 4 {
		t.Fatalf("partial success count=%+v %v", page, err)
	}
}

func TestBatchEnvelopeBoundsWriteNothingAndAccept100(t *testing.T) {
	for _, size := range []int{0, 100, 101} {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			s, a, _, _ := setupStore(t)
			in := AddWordsInput{Words: make([]AddWordsItem, size)}
			for i := range size {
				in.Words[i] = batchItem(fmt.Sprintf("bounded synthetic %03d", i), fmt.Sprintf("bounded-key-%03d", i), nil)
				if i%2 == 0 {
					in.Words[i].Senses = senses()
				}
			}
			results, err := s.AddWords(t.Context(), a, in)
			if size != 100 {
				if !errors.Is(err, ErrInvalid) || len(results) != 0 {
					t.Fatalf("invalid envelope=%+v %v", results, err)
				}
			} else {
				if err != nil || len(results) != 100 {
					t.Fatalf("maximum batch=%+v %v", results, err)
				}
				for i, result := range results {
					if result.Err != nil || result.Word.Revision != 1 {
						t.Fatalf("maximum item %d=%+v", i, result)
					}
				}
			}
			page, err := s.Words(t.Context(), a, Filter{})
			want := 0
			if size == 100 {
				want = 100
			}
			if err != nil || page.Total != want {
				t.Fatalf("after envelope: total=%d want %d err=%v", page.Total, want, err)
			}
			var retries int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency`).Scan(&retries); err != nil || retries != want {
				t.Fatalf("retry records=%d want %d err=%v", retries, want, err)
			}
		})
	}
}

func TestBatchRetrySnapshotsSurviveReorderingSplittingEditsAndRestart(t *testing.T) {
	s, a, other, path := setupStore(t)
	in := AddWordsInput{Words: []AddWordsItem{batchItem("retry synthetic complete", "batch-retry-001", senses()), batchItem("retry synthetic draft", "batch-retry-002", nil)}}
	original, err := s.AddWords(t.Context(), a, in)
	if err != nil || original[0].Err != nil || original[1].Err != nil {
		t.Fatalf("create=%+v %v", original, err)
	}
	// Renaming frees the old term; replay must still return the old snapshot,
	// without recreating it or replacing the newer capture and learning fields.
	edited, err := s.UpdateWord(t.Context(), a, UpdateInput{Capture: Capture{Term: "renamed synthetic complete", PersonalNote: "A later capture edit."}, ID: original[0].Word.ID, ExpectedRevision: 1, IdempotencyKey: "batch-later-edit"})
	if err != nil {
		t.Fatal(err)
	}
	newSenses := senses()
	newSenses[0].Definition = "A later learning edit."
	edited, err = s.CompleteWord(t.Context(), a, CompleteInput{ID: edited.ID, ExpectedRevision: edited.Revision, IdempotencyKey: "batch-later-learning", Senses: newSenses})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, order := range [][]int{{1, 0}, {0}, {1}} {
		retry := AddWordsInput{}
		for _, i := range order {
			retry.Words = append(retry.Words, in.Words[i])
		}
		replayed, err := reopened.AddWords(t.Context(), a, retry)
		if err != nil {
			t.Fatal(err)
		}
		for i, index := range order {
			if replayed[i].Err != nil || !reflect.DeepEqual(replayed[i].Word, original[index].Word) {
				t.Fatalf("replay changed snapshot: %+v", replayed[i])
			}
		}
	}
	current, err := reopened.Word(t.Context(), a, edited.ID)
	if err != nil || !reflect.DeepEqual(current, edited) {
		t.Fatal("retry reapplied old intent")
	}
	page, err := reopened.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 2 {
		t.Fatal("retry recreated the old term")
	}
	for name, change := range map[string]func(*AddWordsItem){
		"capture": func(item *AddWordsItem) { item.PersonalNote = "Changed input." },
		"spacing": func(item *AddWordsItem) { item.Term = " retry synthetic complete " },
		"learning": func(item *AddWordsItem) {
			item.Senses = senses()
			item.Senses[0].Definition = "Changed definition."
		},
		"completeness": func(item *AddWordsItem) { item.Senses = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := in.Words[0]
			change(&changed)
			result, err := reopened.AddWords(t.Context(), a, AddWordsInput{Words: []AddWordsItem{changed}})
			if err != nil || !errors.Is(result[0].Err, ErrRetryKey) {
				t.Fatalf("changed-key input=%+v %v", result, err)
			}
		})
	}
	// Owner and operation are both part of the durable key namespace.
	owned, err := reopened.AddWords(t.Context(), other, in)
	if err != nil || owned[0].Err != nil || owned[0].Word.ID == original[0].Word.ID {
		t.Fatal("batch retry key crossed owners")
	}
	single, err := reopened.AddWord(t.Context(), a, AddInput{Capture: Capture{Term: "single namespace synthetic"}, IdempotencyKey: in.Words[0].IdempotencyKey})
	if err != nil || single.ID == original[0].Word.ID || single.Status != NeedsDetails {
		t.Fatal("batch changed the existing add_word namespace")
	}
}

func TestBatchInvalidLearningDoesNotCreateDraftOrRetryRecord(t *testing.T) {
	s, a, _, _ := setupStore(t)
	for name, change := range map[string]func([]Sense) []Sense{
		"too many senses":  func(content []Sense) []Sense { return append(content, append(senses(), senses()...)...) },
		"blank definition": func(content []Sense) []Sense { content[0].Definition = " "; return content },
		"blank usage":      func(content []Sense) []Sense { content[0].WhenToUse = ""; return content },
		"one example":      func(content []Sense) []Sense { content[0].Examples = content[0].Examples[:1]; return content },
		"blank context":    func(content []Sense) []Sense { content[0].Examples[0].Context = ""; return content },
		"no phrases":       func(content []Sense) []Sense { content[0].CommonPhrases = nil; return content },
		"unknown pos":      func(content []Sense) []Sense { content[0].PartOfSpeech = "unknown"; return content },
		"long definition":  func(content []Sense) []Sense { content[0].Definition = strings.Repeat("x", 1001); return content },
	} {
		t.Run(name, func(t *testing.T) {
			item := batchItem("invalid synthetic learning", "invalid-batch-learning", change(senses()))
			result, err := s.AddWords(t.Context(), a, AddWordsInput{Words: []AddWordsItem{item}})
			if err != nil || !errors.Is(result[0].Err, ErrInvalid) {
				t.Fatalf("invalid learning=%+v %v", result, err)
			}
		})
	}
	page, err := s.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 0 {
		t.Fatal("invalid learning left a transient draft")
	}
	var retries int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency`).Scan(&retries); err != nil || retries != 0 {
		t.Fatal("invalid learning left a successful retry record")
	}
}

func TestBatchPerItemTransactionRollsBackOnRetryPersistenceFailure(t *testing.T) {
	s, a, _, _ := setupStore(t)
	_, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER synthetic_retry_failure BEFORE INSERT ON idempotency WHEN NEW.operation='add_words' AND NEW.key='batch-rollback-002' BEGIN SELECT RAISE(ABORT,'synthetic persistence failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	in := AddWordsInput{Words: []AddWordsItem{batchItem("before synthetic failure", "batch-rollback-001", nil), batchItem("failed synthetic complete", "batch-rollback-002", senses()), batchItem("after synthetic failure", "batch-rollback-003", senses())}}
	results, err := s.AddWords(t.Context(), a, in)
	if err != nil || results[0].Err != nil || results[1].Err == nil || results[2].Err != nil {
		t.Fatalf("transaction results=%+v %v", results, err)
	}
	page, err := s.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 2 {
		t.Fatal("failed item escaped rollback or rolled back adjacent successes")
	}
	for _, word := range page.Items {
		if word.Term == in.Words[1].Term {
			t.Fatal("word committed without its retry snapshot")
		}
	}
	if _, err := s.db.ExecContext(t.Context(), `DROP TRIGGER synthetic_retry_failure`); err != nil {
		t.Fatal(err)
	}
	retried, err := s.AddWords(t.Context(), a, in)
	if err != nil || retried[1].Err != nil || !reflect.DeepEqual(retried[0].Word, results[0].Word) || !reflect.DeepEqual(retried[2].Word, results[2].Word) {
		t.Fatal("retry after partial failure did not preserve successful snapshots")
	}
}

func TestBatchRequiresWriteAndEnabledOwner(t *testing.T) {
	s, a, other, _ := setupStore(t)
	in := AddWordsInput{Words: []AddWordsItem{batchItem("forbidden synthetic", "batch-forbidden-001", senses())}}
	for _, actor := range []Actor{{}, {Owner: a.Owner}, {Owner: a.Owner, Scopes: []string{Read}}, {Owner: "unknown", Scopes: []string{Read, Write}}} {
		if result, err := s.AddWords(t.Context(), actor, in); !errors.Is(err, ErrForbidden) || len(result) != 0 {
			t.Fatalf("authorization=%+v %v", result, err)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE owners SET enabled=0 WHERE id=?`, other.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWords(t.Context(), other, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled owner=%v", err)
	}
	page, err := s.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 0 {
		t.Fatal("forbidden batch wrote data")
	}
}
