package feed_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

// narratedBody exercises every kind of block: a heading repeating the title
// (skipped, as in the reader), inline Markdown, an entity, a quote, a list, a
// rule, code, a diagram and a table.
const narratedBody = "# How a $100 Payment Settled $200\n\n" +
	"A customer owes **two** invoices of $100 each. Their [billing](https://example.com) system sends `one` payment &amp; waits.\n\n" +
	"## The setup\n\n" +
	"> A timeout can mean three things.\n\n" +
	"- The request never arrived.\n- The server finished.\n\n" +
	"---\n\n" +
	"```go\nmu.Lock()\n```\n\n" +
	"```mermaid\nsequenceDiagram\n  A->>B: pay\n```\n\n" +
	"| Invoice | Amount |\n| --- | --- |\n| I1 | $100 |\n"

func narratedArticle(parts ...sqlite.NarrationPart) feed.Content {
	return feed.Content{
		URL: "https://example.com/payments", Title: "How a $100 Payment Settled $200", Summary: "Not read aloud.",
		ContentMarkdown: narratedBody, Narration: parts,
	}
}

func goodNarration() []sqlite.NarrationPart {
	return []sqlite.NarrationPart{
		{Text: "How a $100 Payment Settled $200", Say: "How a hundred-dollar payment settled two hundred dollars."},
		{Text: "A customer owes two invoices of $100 each.", Say: "A customer owes two invoices of a hundred dollars each."},
		{Text: "Their billing system sends one payment & waits.", Say: "Their billing system sends one payment... and waits."},
		{Text: "The setup", Say: "The setup."},
		{Text: "A timeout can mean three things.", Say: "A timeout can mean three things."},
		{Text: "The request never arrived.", Say: "One: the request never arrived."},
		{Text: "The server finished.", Say: "Two: the server finished."},
		{Describe: feed.DescribeCode, Say: "In the code, the service takes the lock."},
		{Describe: feed.DescribeDiagram, Say: "The diagram shows A paying B."},
		{Describe: feed.DescribeTable, Say: "Invoice one is a hundred dollars."},
	}
}

func TestNarrationIsRequiredValidatedAndReturnedByGet(t *testing.T) {
	_, s, alice, _ := setup(t)
	ctx := t.Context()
	if _, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "missing", Article: narratedArticle()}); !strings.HasPrefix(feed.SafeError(err), "Narration required.") {
		t.Fatalf("article without narration: %v", err)
	}
	added, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "narrated", Article: narratedArticle(goodNarration()...)})
	if err != nil {
		t.Fatal(feed.SafeError(err))
	}
	if !reflect.DeepEqual(added.Article.Narration, goodNarration()) {
		t.Fatalf("stored narration: %#v", added.Article.Narration)
	}
	got, err := s.GetArticle(ctx, alice, added.Article.ID)
	if err != nil || !reflect.DeepEqual(got.Narration, goodNarration()) {
		t.Fatalf("get_article narration: %#v %v", got.Narration, err)
	}
	page, err := s.ListArticles(ctx, alice, feed.ListInput{})
	if err != nil || len(page.Articles) != 1 || page.Articles[0].Narration != nil {
		t.Fatalf("lists must omit narration: %#v %v", page, err)
	}
}

func TestNarrationErrorsSayWhereTheScriptDiffers(t *testing.T) {
	_, s, alice, _ := setup(t)
	edit := func(change func([]sqlite.NarrationPart) []sqlite.NarrationPart) []sqlite.NarrationPart {
		return change(goodNarration())
	}
	for _, tc := range []struct {
		name  string
		parts []sqlite.NarrationPart
		want  string
	}{
		{"changed words", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			p[1].Text = "A customer owes three invoices."
			return p
		}), `part 2: text doesn't match the paragraph “A customer owes two invoices`},
		{"unfinished paragraph", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			return append(p[:2], p[3:]...)
		}), `part 3: text doesn't match how the paragraph “A customer owes two invoices`},
		{"crosses into the next block", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			p[2].Text += " The setup"
			return append(p[:3], p[4:]...)
		}), "A part's text never crosses into the next paragraph or heading"},
		{"missing describe", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			return append(p[:7], p[8:]...)
		}), `part 8 describes a diagram, but the code after`},
		{"text where code is", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			p[7] = sqlite.NarrationPart{Text: "mu.Lock()", Say: "mu dot lock"}
			return p
		}), `part 8 reads text, but the article has the code after`},
		{"ends early", goodNarration()[:9], `the narration ends before the table after`},
		{"extra part", append(goodNarration(), sqlite.NarrationPart{Text: "More", Say: "More."}), "part 11 comes after the end of the article"},
		{"text and describe", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			p[0].Describe = feed.DescribeCode
			return p
		}), "part 1 needs either text"},
		{"unknown describe", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			p[7].Describe = "image"
			return p
		}), "part 8: describe must be code, diagram or table"},
		{"empty say", edit(func(p []sqlite.NarrationPart) []sqlite.NarrationPart {
			p[0].Say = " "
			return p
		}), "part 1: say must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: tc.name, Article: narratedArticle(tc.parts...)})
			message := feed.SafeError(err)
			if !strings.HasPrefix(message, "Invalid narration: ") || !strings.Contains(message, tc.want) {
				t.Fatalf("got %q, want it to contain %q", message, tc.want)
			}
		})
	}
}

func TestPatchNeedsNarrationForNewWordsOnly(t *testing.T) {
	f, s, alice, _ := setup(t)
	ctx := t.Context()
	added, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "narrated", Article: narratedArticle(goodNarration()...)})
	if err != nil {
		t.Fatal(err)
	}
	id := added.Article.ID
	title := "How a $100 Payment Settled $300"
	if _, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "retitle", ArticleID: id, ExpectedRevision: 1, Patch: feed.Patch{Title: &title}}); !strings.Contains(feed.SafeError(err), "part 1: text doesn't match the title") {
		t.Fatalf("new title with the old script: %v", err)
	}
	// The body's first heading no longer repeats the title, so the reader
	// shows it and the script reads it too.
	script := append([]sqlite.NarrationPart{
		{Text: title, Say: "How a hundred-dollar payment settled three hundred dollars."},
		{Text: "How a $100 Payment Settled $200", Say: "How a hundred-dollar payment settled two hundred dollars."},
	}, goodNarration()[1:]...)
	retitled, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "retitle", ArticleID: id, ExpectedRevision: 1, Patch: feed.Patch{Title: &title, Narration: &script}})
	if err != nil || retitled.Article.Title != title || !reflect.DeepEqual(retitled.Article.Narration, script) {
		t.Fatalf("retitle with a new script: %v", err)
	}

	// An article saved before scripts existed keeps working: other fields can
	// change without a script, new words need one, and one can be added.
	legacy := sqlite.Article{URL: "https://example.com/legacy", Title: "Legacy", Summary: "Saved earlier.", ContentMarkdown: "Old text."}
	old, err := f.Store.MutateArticle(ctx, alice.Owner(), "legacy", "add_article", []byte("legacy add"), legacy, 0)
	if err != nil {
		t.Fatal(err)
	}
	summary := "A new summary."
	if _, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "legacy-summary", ArticleID: old.Article.ID, ExpectedRevision: 1, Patch: feed.Patch{Summary: &summary}}); err != nil {
		t.Fatalf("legacy summary patch: %v", err)
	}
	body := "New text."
	if _, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "legacy-body", ArticleID: old.Article.ID, ExpectedRevision: 2, Patch: feed.Patch{ContentMarkdown: &body}}); !strings.HasPrefix(feed.SafeError(err), "Narration required.") {
		t.Fatalf("legacy body patch without a script: %v", err)
	}
	backfill := feed.PlainNarration("Legacy", "Old text.")
	narrated, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "legacy-script", ArticleID: old.Article.ID, ExpectedRevision: 2, Patch: feed.Patch{Narration: &backfill}})
	if err != nil || len(narrated.Article.Narration) != 2 {
		t.Fatalf("backfilled script: %#v %v", narrated.Article.Narration, err)
	}
	empty := []sqlite.NarrationPart{}
	if _, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "clear-script", ArticleID: old.Article.ID, ExpectedRevision: 3, Patch: feed.Patch{Narration: &empty}}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatalf("cleared script: %v", err)
	}
}

func TestPlainNarrationReadsEveryBlockAndSplitsLongPassages(t *testing.T) {
	parts := feed.PlainNarration("Title", narratedBody+"\n"+strings.Repeat("word ", 1000))
	if _, err := feed.NormalizeAddInput(feed.AddInput{IdempotencyKey: "plain", Article: feed.Content{
		URL: "https://example.com/plain", Title: "Title", Summary: "Summary.", ContentMarkdown: narratedBody + "\n" + strings.Repeat("word ", 1000), Narration: parts,
	}}, thumbnail.Policy{}); err != nil {
		t.Fatal(feed.SafeError(err))
	}
	for _, part := range parts {
		if len(part.Say) > feed.MaxNarrationSayBytes {
			t.Fatalf("part of %d bytes", len(part.Say))
		}
	}
}
