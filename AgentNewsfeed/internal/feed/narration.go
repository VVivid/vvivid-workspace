package feed

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentnewsfeed/internal/store/sqlite"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Narration limits. A part's spoken text fits one ElevenLabs request with room
// to spare, and the whole script stays within what one audio may speak.
const (
	maxNarrationParts     = 5000
	MaxNarrationSayBytes  = 4000
	MaxNarrationTextBytes = 8192
	maxNarrationSayChars  = maxAudioCharacters
)

// What a describe part may describe: the article blocks that have no text to
// read word for word.
const (
	DescribeCode    = "code"
	DescribeDiagram = "diagram"
	DescribeTable   = "table"
)

// errMissingNarration rejects an agent's article without a narration script.
var errMissingNarration = errors.New("narration required")

// narrationError explains, for the agent, where a narration script and its
// article differ.
type narrationError struct{ message string }

func (e narrationError) Error() string { return "invalid narration: " + e.message }

func invalidNarration(format string, args ...any) error {
	return narrationError{fmt.Sprintf(format, args...)}
}

// narrationUnit is one thing the narration must cover, in reading order: a
// passage of text read word for word, or a block it describes.
type narrationUnit struct {
	// label names the unit for error messages, such as "paragraph 3".
	label string
	// describe is the block kind for a described block, "" for text.
	describe string
	// text is the passage as the reader shows it; key holds its letters and
	// digits, lowercased, which is what the narration's text must match.
	text string
	key  []rune
	// at maps each key rune to its byte offset in text.
	at []int
}

// narrationUnits lists what a narration of the article must cover: its title,
// then its body as the native reader shows it. The reader skips a first-level
// heading that repeats the title, and so does the narration. The summary is not
// read aloud.
func narrationUnits(title, markdown string) []narrationUnit {
	units := []narrationUnit{newTextUnit("the title", title)}
	source := []byte(markdown)
	md := goldmark.New(goldmark.WithExtensions(extension.Table))
	document := md.Parser().Parse(text.NewReader(source), parser.WithContext(parser.NewContext()))
	collector := unitCollector{source: source}
	collector.blocks(document)
	body := collector.units
	if collector.titleHeading && len(body) > 0 && strings.EqualFold(strings.TrimSpace(body[0].text), strings.TrimSpace(title)) {
		body = body[1:]
	}
	return append(units, body...)
}

type unitCollector struct {
	source []byte
	units  []narrationUnit
	// started is set by the reader's first block; titleHeading when that block
	// is a first-level heading, which the reader hides if it repeats the title.
	started, titleHeading bool
}

// blocks follows the reader's blocks: headings and paragraphs are read, quotes
// and lists are read through their contents, and code, diagrams and tables are
// described. Rules, HTML blocks and empty blocks are skipped, as the reader
// skips or drops them.
func (c *unitCollector) blocks(parent ast.Node) {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		switch n := node.(type) {
		case *ast.Heading:
			passage := c.inline(n)
			if !c.started && strings.TrimSpace(passage) != "" {
				c.titleHeading = n.Level == 1 && parent.Kind() == ast.KindDocument
			}
			c.addText("the heading", passage)
		case *ast.Paragraph, *ast.TextBlock:
			c.addText("the paragraph", c.inline(n))
		case *ast.Blockquote, *ast.List, *ast.ListItem:
			c.started = true
			c.blocks(n)
		case *ast.FencedCodeBlock:
			kind := DescribeCode
			if info := n.Info; info != nil {
				if name, _, _ := strings.Cut(strings.TrimSpace(string(info.Segment.Value(c.source))), " "); strings.EqualFold(name, "mermaid") {
					kind = DescribeDiagram
				}
			}
			c.addDescribed(kind, c.lines(n))
		case *ast.CodeBlock:
			c.addDescribed(DescribeCode, c.lines(n))
		case *extast.Table:
			c.addDescribed(DescribeTable, "table")
		case *ast.ThematicBreak:
			c.started = true
		}
	}
}

// addText adds a passage the voice reads word for word. One without letters or
// digits, such as an ellipsis, has nothing to match and needs no part.
func (c *unitCollector) addText(kind, passage string) {
	if strings.TrimSpace(passage) != "" {
		c.started = true
	}
	unit := newTextUnit(kind+" "+preview(passage, 0), passage)
	if len(unit.key) > 0 {
		c.units = append(c.units, unit)
	}
}

func (c *unitCollector) addDescribed(kind, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	c.started = true
	c.units = append(c.units, narrationUnit{label: fmt.Sprintf("the %s after %s", kind, c.lastLabel()), describe: kind})
}

func (c *unitCollector) lastLabel() string {
	if len(c.units) == 0 {
		return "the title"
	}
	return c.units[len(c.units)-1].label
}

func (c *unitCollector) lines(n ast.Node) string {
	var b bytes.Buffer
	lines := n.Lines()
	for i := range lines.Len() {
		segment := lines.At(i)
		b.Write(segment.Value(c.source))
	}
	return b.String()
}

// inline is a block's text as the reader shows it: emphasis, links and code
// spans as their text, images as their alt text, raw HTML dropped.
func (c *unitCollector) inline(block ast.Node) string {
	var b strings.Builder
	_ = ast.Walk(block, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || node == block {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Text:
			b.Write(util.ResolveNumericReferences(util.ResolveEntityNames(n.Segment.Value(c.source))))
			if n.SoftLineBreak() || n.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(n.Value)
		case *ast.AutoLink:
			b.Write(n.Label(c.source))
		case *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func newTextUnit(label, passage string) narrationUnit {
	unit := narrationUnit{label: label, text: passage}
	for offset, r := range passage {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			unit.key = append(unit.key, unicode.ToLower(r))
			unit.at = append(unit.at, offset)
		}
	}
	return unit
}

// matchKey is what narration text is compared by: its letters and digits,
// lowercased. Punctuation, spacing, quotes and Markdown marks do not matter.
func matchKey(passage string) []rune {
	return newTextUnit("", passage).key
}

// preview quotes a passage's words from byte offset from, shortened.
func preview(passage string, from int) string {
	rest := strings.Join(strings.Fields(passage[from:]), " ")
	if utf8.RuneCountInString(rest) > 60 {
		rest = string([]rune(rest)[:60]) + "…"
	}
	return "“" + rest + "”"
}

// validNarrationParts checks each part on its own: exactly one of text or
// describe, and a spoken text the voice can read.
func validNarrationParts(parts []sqlite.NarrationPart) error {
	if len(parts) > maxNarrationParts {
		return invalidNarration("use at most %d parts", maxNarrationParts)
	}
	characters := 0
	for i, part := range parts {
		n := i + 1
		switch {
		case (part.Text == "") == (part.Describe == ""):
			return invalidNarration("part %d needs either text (the passage it reads) or describe (code, diagram or table), not both", n)
		case part.Describe != "" && part.Describe != DescribeCode && part.Describe != DescribeDiagram && part.Describe != DescribeTable:
			return invalidNarration("part %d: describe must be code, diagram or table", n)
		case len(part.Text) > MaxNarrationTextBytes || !utf8.ValidString(part.Text):
			return invalidNarration("part %d: text must be valid UTF-8 of at most %d bytes; split a longer passage into several parts", n, MaxNarrationTextBytes)
		case !validSay(part.Say):
			return invalidNarration("part %d: say must be 1–%d bytes of spoken text without control characters other than line breaks", n, MaxNarrationSayBytes)
		}
		characters += utf8.RuneCountInString(part.Say)
	}
	if characters > maxNarrationSayChars {
		return invalidNarration("say adds up to %d characters; the most one audio can speak is %d", characters, maxNarrationSayChars)
	}
	return nil
}

func validSay(say string) bool {
	return len(say) <= MaxNarrationSayBytes && utf8.ValidString(say) && strings.TrimSpace(say) != "" &&
		!strings.ContainsFunc(say, func(r rune) bool { return unicode.IsControl(r) && r != '\n' })
}

// validateNarration checks that parts narrate the whole article in reading
// order: text parts together read each passage of the title and body, word for
// word, without crossing from one passage to the next; and exactly one describe
// part of the right kind stands for each code block, diagram and table.
func validateNarration(title, markdown string, parts []sqlite.NarrationPart) error {
	if len(parts) == 0 {
		return errMissingNarration
	}
	if err := validNarrationParts(parts); err != nil {
		return err
	}
	units := narrationUnits(title, markdown)
	u, read := 0, 0 // the unit being read, and how many of its key runes are covered
	for i, part := range parts {
		n := i + 1
		if u < len(units) && units[u].describe == "" && read == len(units[u].key) {
			u, read = u+1, 0
		}
		if u == len(units) {
			return invalidNarration("part %d comes after the end of the article; remove it", n)
		}
		unit := units[u]
		if part.Describe != "" {
			if unit.describe == "" {
				if read > 0 {
					return invalidNarration("part %d describes a %s, but %s is not finished; it continues %s", n, part.Describe, unit.label, preview(unit.text, unit.at[read]))
				}
				return invalidNarration("part %d describes a %s, but the article has %s here", n, part.Describe, unit.label)
			}
			if unit.describe != part.Describe {
				return invalidNarration("part %d describes a %s, but %s is a %s", n, part.Describe, unit.label, unit.describe)
			}
			u, read = u+1, 0
			continue
		}
		if unit.describe != "" {
			return invalidNarration("part %d reads text, but the article has %s here; add a part with describe %q before it", n, unit.label, unit.describe)
		}
		key := matchKey(part.Text)
		if len(key) == 0 {
			return invalidNarration("part %d: text has no words", n)
		}
		rest := unit.key[read:]
		if len(key) > len(rest) || !slices.Equal(key, rest[:len(key)]) {
			if read == 0 {
				return invalidNarration("part %d: text doesn't match %s; copy the passage as the article shows it", n, unit.label)
			}
			return invalidNarration("part %d: text doesn't match how %s continues: %s. A part's text never crosses into the next paragraph or heading", n, unit.label, preview(unit.text, unit.at[read]))
		}
		read += len(key)
	}
	if u < len(units) && units[u].describe == "" && read == len(units[u].key) {
		u, read = u+1, 0
	}
	if u < len(units) {
		unit := units[u]
		if unit.describe != "" {
			return invalidNarration("the narration ends before %s; add a part with describe %q", unit.label, unit.describe)
		}
		return invalidNarration("the narration ends before reading all of %s, from %s", unit.label, preview(unit.text, unit.at[read]))
	}
	return nil
}

// PlainNarration reads an article as written: each passage of its title and
// body word for word, and a short note for each code block, diagram and table.
// It is valid but not good narration, for tools and tests; agents write their
// own for the ear.
func PlainNarration(title, markdown string) []sqlite.NarrationPart {
	var parts []sqlite.NarrationPart
	for _, unit := range narrationUnits(title, markdown) {
		switch {
		case unit.describe != "":
			parts = append(parts, sqlite.NarrationPart{Describe: unit.describe, Say: "Here the article has a " + unit.describe + "."})
		case len(unit.key) > 0:
			for _, passage := range splitPassage(strings.Join(strings.Fields(unit.text), " "), MaxNarrationSayBytes) {
				parts = append(parts, sqlite.NarrationPart{Text: passage, Say: passage})
			}
		}
	}
	return parts
}

// splitPassage splits text at spaces into pieces of at most limit bytes.
func splitPassage(text string, limit int) []string {
	var pieces []string
	for len(text) > limit {
		cut := strings.LastIndexByte(text[:limit], ' ')
		if cut <= 0 {
			cut = limit
			for !utf8.RuneStart(text[cut]) {
				cut--
			}
		}
		pieces = append(pieces, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	return append(pieces, text)
}
