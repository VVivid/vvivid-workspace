package store

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ID() string { return rand.Text() }

// Limits are UTF-8 bytes, so transport, SQLite, and MCP share the same bounds.
func bounded(v string, min, max int) bool {
	return utf8.ValidString(v) && len(v) >= min && len(v) <= max &&
		!strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' })
}
func required(v string, max int) bool { return bounded(v, 1, max) && strings.TrimSpace(v) != "" }
func keyValid(v string) bool          { return bounded(v, 8, 128) && strings.TrimSpace(v) == v }
func recordID(v string) bool          { return bounded(v, 1, 64) && strings.TrimSpace(v) == v }
func normalizedTerm(v string) string  { return strings.Join(strings.Fields(v), " ") }
func termKey(v string) string         { return strings.ToLower(normalizedTerm(v)) }

func validateCapture(in Capture) error {
	if !bounded(in.Term, 1, 480) || !required(normalizedTerm(in.Term), 120) ||
		!bounded(in.OriginalSentence, 0, 4000) || !bounded(in.PersonalNote, 0, 4000) || !validURL(in.SourceURL) {
		return fmt.Errorf("%w: word must be 1–120 bytes, sentence/note at most 4000 bytes, source an HTTP(S) URL at most 2048 bytes", ErrInvalid)
	}
	return nil
}
func validURL(v string) bool {
	if v == "" {
		return true
	}
	if !bounded(v, 1, 2048) {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Host != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https")
}
func validateSenses(senses []Sense) error {
	if len(senses) < 1 || len(senses) > 8 {
		return fmt.Errorf("%w: provide 1–8 complete English senses", ErrInvalid)
	}
	for i, s := range senses {
		if !slices.Contains([]string{"noun", "verb", "adjective", "adverb", "pronoun", "preposition", "conjunction", "determiner", "interjection", "phrase"}, s.PartOfSpeech) ||
			!required(s.Definition, 1000) || !required(s.WhenToUse, 2000) || !bounded(s.UsageTip, 0, 2000) ||
			len(s.Examples) < 2 || len(s.Examples) > 6 || len(s.CommonPhrases) < 1 || len(s.CommonPhrases) > 10 {
			return fmt.Errorf("%w: sense %d needs a part_of_speech, definition, when_to_use, 2–6 contextual examples and 1–10 common phrases", ErrInvalid, i+1)
		}
		for _, e := range s.Examples {
			if !required(e.Context, 120) || !required(e.Sentence, 1200) {
				return fmt.Errorf("%w: each example needs context (up to 120 bytes) and sentence (up to 1200 bytes)", ErrInvalid)
			}
		}
		for _, phrase := range s.CommonPhrases {
			if !required(phrase, 240) {
				return fmt.Errorf("%w: common phrases must be 1–240 bytes", ErrInvalid)
			}
		}
	}
	return nil
}
func normalizeFilter(f *Filter) error {
	if !bounded(f.Query, 0, 120) || !slices.Contains([]string{"", NeedsDetails, Complete}, f.Status) {
		return ErrInvalid
	}
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = 30
	}
	if f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 {
		return ErrInvalid
	}
	f.Query = termKey(f.Query)
	return nil
}
