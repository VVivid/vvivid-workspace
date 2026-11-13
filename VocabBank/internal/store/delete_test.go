package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func deleteItem(word Word, key string) DeleteWordsItem {
	return DeleteWordsItem{ID: word.ID, ExpectedRevision: word.Revision, IdempotencyKey: key}
}

func TestDeleteBatchMixedOutcomesAndOrderedSnapshots(t *testing.T) {
	s, a, other, _ := setupStore(t)
	first := add(t, s, a, "first synthetic deletion", "delete-first-capture")
	first, err := s.CompleteWord(t.Context(), a, CompleteInput{ID: first.ID, ExpectedRevision: 1, IdempotencyKey: "delete-first-completion", Senses: senses()})
	if err != nil {
		t.Fatal(err)
	}
	stale := add(t, s, a, "stale synthetic deletion", "delete-stale-capture")
	stale, err = s.UpdateWord(t.Context(), a, UpdateInput{Capture: Capture{Term: stale.Term, PersonalNote: "A newer synthetic capture."}, ID: stale.ID, ExpectedRevision: 1, IdempotencyKey: "delete-stale-edit"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := add(t, s, other, "other owner's synthetic deletion", "delete-foreign-capture")
	last := add(t, s, a, "last synthetic deletion", "delete-last-capture")
	in := DeleteWordsInput{Words: []DeleteWordsItem{
		deleteItem(first, "delete-first-intent"),
		{ID: stale.ID, ExpectedRevision: 1, IdempotencyKey: "delete-stale-intent"},
		{ID: ID(), ExpectedRevision: 1, IdempotencyKey: "delete-missing-intent"},
		deleteItem(foreign, "delete-foreign-intent"),
		{ID: " ", ExpectedRevision: 1, IdempotencyKey: "delete-invalid-id"},
		{ID: stale.ID, ExpectedRevision: 0, IdempotencyKey: "delete-invalid-revision"},
		deleteItem(last, "short"),
		deleteItem(first, "delete-first-intent"),
		deleteItem(first, "delete-first-new-intent"),
		deleteItem(last, "delete-last-intent"),
	}}
	results, err := s.DeleteWords(t.Context(), a, in)
	if err != nil || len(results) != len(in.Words) {
		t.Fatalf("delete batch=%+v %v", results, err)
	}
	wantErrors := []error{nil, ErrStale, ErrNotFound, ErrNotFound, ErrInvalid, ErrInvalid, ErrInvalid, nil, ErrNotFound, nil}
	for i, result := range results {
		if !errors.Is(result.Err, wantErrors[i]) || (result.DeletedWord.ID != "") != (wantErrors[i] == nil) {
			t.Fatalf("item %d=%+v want error %v", i, result, wantErrors[i])
		}
	}
	if !reflect.DeepEqual(results[0].DeletedWord, first) || !reflect.DeepEqual(results[7].DeletedWord, first) || !reflect.DeepEqual(results[9].DeletedWord, last) {
		t.Fatal("delete results lost or reordered the removed snapshots")
	}
	for _, word := range []Word{first, last} {
		if _, err := s.Word(t.Context(), a, word.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted ID remains readable: %v", err)
		}
	}
	if current, err := s.Word(t.Context(), a, stale.ID); err != nil || !reflect.DeepEqual(current, stale) {
		t.Fatal("stale deletion changed the newer word")
	}
	if current, err := s.Word(t.Context(), other, foreign.ID); err != nil || !reflect.DeepEqual(current, foreign) {
		t.Fatal("deletion crossed owners")
	}
	page, err := s.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("partial deletion count=%+v %v", page, err)
	}
	var retries int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency WHERE operation='delete_words'`).Scan(&retries); err != nil || retries != 2 {
		t.Fatalf("delete retries=%d err=%v", retries, err)
	}
}

func TestDeleteBatchEnvelopeBoundsWriteNothingAndAccept1And100(t *testing.T) {
	for _, size := range []int{0, 1, 100, 101} {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			s, a, _, _ := setupStore(t)
			in := DeleteWordsInput{Words: make([]DeleteWordsItem, size)}
			created := max(1, size)
			for i := range created {
				word := add(t, s, a, fmt.Sprintf("bounded synthetic deletion %03d", i), fmt.Sprintf("delete-bound-capture-%03d", i))
				if i < size {
					in.Words[i] = deleteItem(word, fmt.Sprintf("delete-bound-intent-%03d", i))
				}
			}
			results, err := s.DeleteWords(t.Context(), a, in)
			valid := size == 1 || size == 100
			wantRemaining, wantRetries := created, 0
			if valid {
				if err != nil || len(results) != size {
					t.Fatalf("valid envelope=%+v %v", results, err)
				}
				for i, result := range results {
					if result.Err != nil || result.DeletedWord.ID != in.Words[i].ID {
						t.Fatalf("valid item %d=%+v", i, result)
					}
				}
				wantRemaining, wantRetries = 0, size
			} else if !errors.Is(err, ErrInvalid) || len(results) != 0 {
				t.Fatalf("invalid envelope=%+v %v", results, err)
			}
			page, err := s.Words(t.Context(), a, Filter{})
			if err != nil || page.Total != wantRemaining {
				t.Fatalf("after envelope: total=%d want %d err=%v", page.Total, wantRemaining, err)
			}
			var retries int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency WHERE operation='delete_words'`).Scan(&retries); err != nil || retries != wantRetries {
				t.Fatalf("delete retries=%d want %d err=%v", retries, wantRetries, err)
			}
		})
	}
}

func TestDeleteBatchRetriesSurviveRecreationRestartAndPriorWriteReplays(t *testing.T) {
	s, a, _, path := setupStore(t)
	singleInput := AddInput{Capture: Capture{Term: "single synthetic recreation", PersonalNote: "Original synthetic note."}, IdempotencyKey: "recreate-single-capture"}
	draft, err := s.AddWord(t.Context(), a, singleInput)
	if err != nil {
		t.Fatal(err)
	}
	completion := CompleteInput{ID: draft.ID, ExpectedRevision: 1, IdempotencyKey: "recreate-single-learning", Senses: senses()}
	completed, err := s.CompleteWord(t.Context(), a, completion)
	if err != nil {
		t.Fatal(err)
	}
	update := UpdateInput{Capture: Capture{Term: draft.Term, PersonalNote: "Later synthetic capture."}, ID: draft.ID, ExpectedRevision: completed.Revision, IdempotencyKey: "recreate-single-edit"}
	edited, err := s.UpdateWord(t.Context(), a, update)
	if err != nil {
		t.Fatal(err)
	}
	batchInput := AddWordsInput{Words: []AddWordsItem{batchItem("batch synthetic recreation", "recreate-batch-capture", senses())}}
	batch, err := s.AddWords(t.Context(), a, batchInput)
	if err != nil || len(batch) != 1 || batch[0].Err != nil {
		t.Fatalf("batch creation=%+v %v", batch, err)
	}
	in := DeleteWordsInput{Words: []DeleteWordsItem{deleteItem(edited, "recreate-delete-single"), deleteItem(batch[0].Word, "recreate-delete-batch")}}
	removed, err := s.DeleteWords(t.Context(), a, in)
	if err != nil || len(removed) != 2 || removed[0].Err != nil || removed[1].Err != nil {
		t.Fatalf("initial deletion=%+v %v", removed, err)
	}
	// A separate operation may use the same key. New creation intents produce
	// new IDs even though the normalized terms belonged to removed words.
	recreatedInput := AddWordsInput{Words: []AddWordsItem{
		batchItem(edited.Term, in.Words[0].IdempotencyKey, senses()),
		batchItem(batch[0].Word.Term, in.Words[1].IdempotencyKey, senses()),
	}}
	recreated, err := s.AddWords(t.Context(), a, recreatedInput)
	if err != nil || len(recreated) != 2 {
		t.Fatalf("recreation=%+v %v", recreated, err)
	}
	for i, result := range recreated {
		if result.Err != nil || result.Word.ID == in.Words[i].ID || result.Word.Status != Complete || result.Word.Revision != 1 {
			t.Fatalf("recreated item %d=%+v", i, result)
		}
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
		retry := DeleteWordsInput{}
		for _, i := range order {
			retry.Words = append(retry.Words, in.Words[i])
		}
		replayed, err := reopened.DeleteWords(t.Context(), a, retry)
		if err != nil || len(replayed) != len(order) {
			t.Fatalf("reordered retry=%+v %v", replayed, err)
		}
		for i, index := range order {
			if replayed[i].Err != nil || !reflect.DeepEqual(replayed[i].DeletedWord, removed[index].DeletedWord) {
				t.Fatalf("retry changed removed snapshot: %+v", replayed[i])
			}
		}
	}
	for name, change := range map[string]func(*DeleteWordsItem){
		"id":       func(item *DeleteWordsItem) { item.ID = recreated[0].Word.ID },
		"revision": func(item *DeleteWordsItem) { item.ExpectedRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := in.Words[0]
			change(&changed)
			results, err := reopened.DeleteWords(t.Context(), a, DeleteWordsInput{Words: []DeleteWordsItem{changed}})
			if err != nil || len(results) != 1 || !errors.Is(results[0].Err, ErrRetryKey) || results[0].DeletedWord.ID != "" {
				t.Fatalf("changed intent=%+v %v", results, err)
			}
		})
	}
	if replay, err := reopened.AddWord(t.Context(), a, singleInput); err != nil || !reflect.DeepEqual(replay, draft) {
		t.Fatal("original add_word snapshot was removed or changed")
	}
	if replay, err := reopened.AddWords(t.Context(), a, batchInput); err != nil || len(replay) != 1 || replay[0].Err != nil || !reflect.DeepEqual(replay[0].Word, batch[0].Word) {
		t.Fatal("original add_words snapshot was removed or changed")
	}
	if replay, err := reopened.CompleteWord(t.Context(), a, completion); err != nil || !reflect.DeepEqual(replay, completed) {
		t.Fatal("original completion snapshot was removed or changed")
	}
	if replay, err := reopened.UpdateWord(t.Context(), a, update); err != nil || !reflect.DeepEqual(replay, edited) {
		t.Fatal("original update snapshot was removed or changed")
	}
	for i, result := range recreated {
		if _, err := reopened.Word(t.Context(), a, in.Words[i].ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a prior write replay resurrected old ID %d: %v", i, err)
		}
		current, err := reopened.Word(t.Context(), a, result.Word.ID)
		if err != nil || !reflect.DeepEqual(current, result.Word) {
			t.Fatal("old delete retry removed or changed the recreated word")
		}
	}
	page, err := reopened.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 2 {
		t.Fatal("retry changed the current bank")
	}
}

func TestDeleteBatchRollsBackWhenRetryPersistenceFails(t *testing.T) {
	s, a, _, _ := setupStore(t)
	created, err := s.AddWords(t.Context(), a, AddWordsInput{Words: []AddWordsItem{
		batchItem("before synthetic delete failure", "delete-rollback-capture-001", nil),
		batchItem("failed synthetic deletion", "delete-rollback-capture-002", senses()),
		batchItem("after synthetic delete failure", "delete-rollback-capture-003", senses()),
	}})
	if err != nil || len(created) != 3 {
		t.Fatalf("seed=%+v %v", created, err)
	}
	in := DeleteWordsInput{}
	for i, result := range created {
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		in.Words = append(in.Words, deleteItem(result.Word, fmt.Sprintf("delete-rollback-intent-%03d", i)))
	}
	_, err = s.db.ExecContext(t.Context(), `CREATE TRIGGER synthetic_delete_retry_failure BEFORE INSERT ON idempotency WHEN NEW.operation='delete_words' AND NEW.key='delete-rollback-intent-001' BEGIN SELECT RAISE(ABORT,'synthetic persistence failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	results, err := s.DeleteWords(t.Context(), a, in)
	if err != nil || len(results) != 3 || results[0].Err != nil || results[1].Err == nil || results[1].DeletedWord.ID != "" || results[2].Err != nil {
		t.Fatalf("rollback outcomes=%+v %v", results, err)
	}
	current, err := s.Word(t.Context(), a, created[1].Word.ID)
	if err != nil || !reflect.DeepEqual(current, created[1].Word) {
		t.Fatal("word was deleted without its successful retry snapshot")
	}
	page, err := s.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 1 {
		t.Fatal("failed deletion affected adjacent transactions")
	}
	var retries int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM idempotency WHERE operation='delete_words'`).Scan(&retries); err != nil || retries != 2 {
		t.Fatalf("rollback retry count=%d %v", retries, err)
	}
	if _, err := s.db.ExecContext(t.Context(), `DROP TRIGGER synthetic_delete_retry_failure`); err != nil {
		t.Fatal(err)
	}
	retried, err := s.DeleteWords(t.Context(), a, in)
	if err != nil || len(retried) != 3 {
		t.Fatalf("retry after rollback=%+v %v", retried, err)
	}
	for i, result := range retried {
		if result.Err != nil || !reflect.DeepEqual(result.DeletedWord, created[i].Word) {
			t.Fatalf("retry after rollback item %d=%+v", i, result)
		}
	}
	page, err = s.Words(t.Context(), a, Filter{})
	if err != nil || page.Total != 0 {
		t.Fatal("retry did not finish the failed item")
	}
}

func TestDeleteBatchRequiresWriteEnabledOwnerAndOwnerQualifiedKeys(t *testing.T) {
	s, a, other, _ := setupStore(t)
	word := add(t, s, a, "protected synthetic deletion", "delete-protected-capture")
	otherWord := add(t, s, other, word.Term, "delete-protected-capture")
	in := DeleteWordsInput{Words: []DeleteWordsItem{deleteItem(word, "delete-protected-intent")}}
	for _, actor := range []Actor{{}, {Owner: a.Owner}, {Owner: a.Owner, Scopes: []string{Read}}, {Owner: "unknown", Scopes: []string{Read, Write}}} {
		if results, err := s.DeleteWords(t.Context(), actor, in); !errors.Is(err, ErrForbidden) || len(results) != 0 {
			t.Fatalf("unauthorized deletion=%+v %v", results, err)
		}
	}
	if current, err := s.Word(t.Context(), a, word.ID); err != nil || !reflect.DeepEqual(current, word) {
		t.Fatal("unauthorized batch deleted a word")
	}
	removed, err := s.DeleteWords(t.Context(), a, in)
	if err != nil || len(removed) != 1 || removed[0].Err != nil {
		t.Fatalf("authorized deletion=%+v %v", removed, err)
	}
	if results, err := s.DeleteWords(t.Context(), other, in); err != nil || len(results) != 1 || !errors.Is(results[0].Err, ErrNotFound) || results[0].DeletedWord.ID != "" {
		t.Fatalf("retry snapshot crossed owners=%+v %v", results, err)
	}
	otherInput := DeleteWordsInput{Words: []DeleteWordsItem{deleteItem(otherWord, in.Words[0].IdempotencyKey)}}
	if results, err := s.DeleteWords(t.Context(), other, otherInput); err != nil || len(results) != 1 || results[0].Err != nil || !reflect.DeepEqual(results[0].DeletedWord, otherWord) {
		t.Fatalf("owner-qualified key=%+v %v", results, err)
	}
	untouched := add(t, s, a, "disabled owner's synthetic deletion", "delete-disabled-capture")
	if _, err := s.db.ExecContext(t.Context(), `UPDATE owners SET enabled=0 WHERE id=?`, a.Owner); err != nil {
		t.Fatal(err)
	}
	disabledInput := DeleteWordsInput{Words: []DeleteWordsItem{deleteItem(untouched, "delete-disabled-intent"), in.Words[0]}}
	if results, err := s.DeleteWords(t.Context(), a, disabledInput); !errors.Is(err, ErrForbidden) || len(results) != 0 {
		t.Fatalf("disabled owner deleted a word or replayed a snapshot=%+v %v", results, err)
	}
	var remaining int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM words WHERE owner=? AND id=?`, a.Owner, untouched.ID).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("disabled owner's word count=%d %v", remaining, err)
	}
}
