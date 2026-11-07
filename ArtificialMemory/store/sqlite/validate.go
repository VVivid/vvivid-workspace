package sqlite

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

func invalid(message string) error { return fmt.Errorf("%w: %s", memory.ErrInvalid, message) }

func textOK(value string, maxBytes int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && len(value) <= maxBytes && (!required || strings.TrimSpace(value) != "")
}

func validIdentity(id string) error {
	if !textOK(id, 256, true) {
		return invalid("account/client identity is required and bounded to 256 bytes")
	}
	return nil
}

func validKind(k memory.Kind) bool {
	return slices.Contains([]memory.Kind{memory.Topic, memory.Context, memory.Proposal, memory.Decision, memory.Note, memory.Retraction}, k)
}

func validLink(k memory.LinkType) bool {
	return slices.Contains([]memory.LinkType{memory.DependsOn, memory.RelatedTo, memory.Supersedes, memory.Corrects, memory.Retracts}, k)
}

func validReference(ref memory.Reference, optional bool) bool {
	if ref.SpanID == "" && ref.LocalID == "" {
		return optional
	}
	return (ref.SpanID == "" || ref.LocalID == "") && textOK(ref.SpanID, 64, false) && textOK(ref.LocalID, 64, false)
}

func stringSet(values []string) []string {
	values = slices.Clone(values)
	slices.Sort(values)
	return slices.Compact(values)
}

// canonicalRemember copies caller-owned input. Defaults and unordered metadata
// are normalized; server-assigned times and IDs are deliberately absent here.
func canonicalRemember(actor memory.Actor, input memory.RememberRequest) (memory.RememberRequest, []byte, error) {
	if err := validIdentity(actor.AccountID); err != nil {
		return input, nil, err
	}
	if err := validIdentity(actor.ClientID); err != nil {
		return input, nil, err
	}
	input.Space = strings.TrimSpace(input.Space)
	if !textOK(input.Space, 128, true) || !textOK(input.Key, 128, true) || !textOK(input.TopicID, 64, false) {
		return input, nil, invalid("space, key or topic ID has invalid size/content")
	}
	if len(input.Entries) == 0 || len(input.Entries) > memory.MaxEntries {
		return input, nil, invalid("entries must contain 1..100 nodes")
	}
	input.Entries = slices.Clone(input.Entries)
	locals := map[string]bool{}
	for i := range input.Entries {
		e := &input.Entries[i]
		if !textOK(e.LocalID, 64, false) || (e.LocalID != "" && locals[e.LocalID]) {
			return input, nil, invalid("duplicate or invalid local ID")
		}
		if e.LocalID != "" {
			locals[e.LocalID] = true
		}
		if !validKind(e.Kind) || !textOK(e.Title, 512, true) || !textOK(e.Context, 16<<10, false) || !textOK(e.ExternalRef, 256, false) {
			return input, nil, invalid("invalid kind, title, context or external reference")
		}
		root := i == 0 && input.TopicID == ""
		if root != (e.Kind == memory.Topic) || (root && (e.Parent != (memory.Reference{}))) || (!root && e.ExternalRef != "") {
			return input, nil, invalid("a new topic starts with one parentless topic root; only roots have external references")
		}
		if !validReference(e.Parent, true) {
			return input, nil, invalid("invalid parent reference")
		}
		if e.SchemaVersion == 0 {
			e.SchemaVersion = 1
		}
		if e.SchemaVersion < 1 || e.SchemaVersion > 1_000_000 {
			return input, nil, invalid("invalid schema version")
		}
		if e.TimeUnknown && (!e.Historical || e.OccurredAt != nil) {
			return input, nil, invalid("unknown time requires historical=true and no occurred_at")
		}
		if e.Historical && e.OccurredAt == nil && !e.TimeUnknown {
			return input, nil, invalid("historical time must be supplied or explicitly unknown")
		}
		if e.OccurredAt != nil {
			e.OccurredAt = new(e.OccurredAt.UTC())
			if e.OccurredAt.Year() < 1 || e.OccurredAt.Year() > 9999 {
				return input, nil, invalid("occurred_at outside years 1..9999")
			}
		}
		if len(e.Payload) == 0 {
			e.Payload = json.RawMessage(`{}`)
		}
		if len(e.Payload) > memory.MaxPayloadBytes || !utf8.Valid(e.Payload) {
			return input, nil, invalid("payload exceeds 32768 bytes or is not UTF-8")
		}
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(e.Payload))
		decoder.UseNumber()
		if !json.Valid(e.Payload) || decoder.Decode(&object) != nil || object == nil {
			return input, nil, invalid("payload must be a JSON object")
		}
		e.Payload, _ = json.Marshal(object)
		if len(e.Payload) > memory.MaxPayloadBytes {
			return input, nil, invalid("normalized payload exceeds 32768 bytes")
		}
		if e.Kind == memory.Decision {
			if _, err := decisionText(e.Payload); err != nil {
				return input, nil, err
			}
		}
		if len(e.Tags) > memory.MaxTags || len(e.Links) > memory.MaxLinks {
			return input, nil, invalid("at most 32 tags and links per entry")
		}
		e.Tags = slices.Clone(e.Tags)
		for j := range e.Tags {
			e.Tags[j] = strings.TrimSpace(e.Tags[j])
			if !textOK(e.Tags[j], 64, true) {
				return input, nil, invalid("tag must contain 1..64 bytes")
			}
		}
		e.Tags = stringSet(e.Tags)
		e.Links = slices.Clone(e.Links)
		for _, l := range e.Links {
			if !validLink(l.Type) || !validReference(l.Target, false) {
				return input, nil, invalid("invalid typed link")
			}
		}
		slices.SortFunc(e.Links, func(a, b memory.LinkInput) int {
			return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.Target.SpanID, b.Target.SpanID), cmp.Compare(a.Target.LocalID, b.Target.LocalID))
		})
		e.Links = slices.Compact(e.Links)
		if !textOK(e.Provenance.Attribution, 128, false) || len(e.Provenance.Sources) > 16 {
			return input, nil, invalid("invalid provenance attribution or more than 16 sources")
		}
		for _, source := range e.Provenance.Sources {
			if !textOK(source.Reference, 2048, true) || !textOK(source.Excerpt, 4096, false) {
				return input, nil, invalid("source reference/excerpt exceeds bounds")
			}
		}
	}
	canonical, err := json.Marshal(struct {
		Actor   memory.Actor           `json:"actor"`
		Request memory.RememberRequest `json:"request"`
	}{actor, input})
	return input, canonical, err
}

func decisionText(payload []byte) (string, error) {
	var d memory.DecisionPayload
	if err := json.Unmarshal(payload, &d); err != nil || !textOK(d.Question, 4096, true) || !textOK(d.Outcome, 4096, true) || d.Reasons == nil || d.Alternatives == nil {
		return "", invalid("decision needs question, outcome, reasons array and alternatives array")
	}
	parts := []string{d.Question, d.Outcome}
	for _, reason := range d.Reasons {
		if !textOK(reason, 4096, true) {
			return "", invalid("invalid decision reason")
		}
		parts = append(parts, reason)
	}
	for _, alt := range d.Alternatives {
		if !textOK(alt.Option, 4096, true) || !slices.Contains([]string{"rejected", "deferred", "considered"}, alt.Disposition) || alt.Reasons == nil {
			return "", invalid("alternative needs option, disposition and reasons array")
		}
		parts = append(parts, alt.Option, alt.Disposition)
		for _, reason := range alt.Reasons {
			if !textOK(reason, 4096, true) {
				return "", invalid("invalid alternative reason")
			}
			parts = append(parts, reason)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func normalizeQuery(q memory.Query) (memory.Query, error) {
	if q.AllSpaces == (len(q.Spaces) > 0) {
		return q, invalid("select spaces or all_spaces=true")
	}
	if len(q.Spaces) > memory.MaxSpaces || len(q.TopicIDs) > 100 || len(q.SpanIDs) > 100 || len(q.Tags) > memory.MaxTags || len(q.Kinds) > 6 {
		return q, invalid("too many query selectors")
	}
	q.Spaces, q.TopicIDs, q.SpanIDs, q.Tags = stringSet(q.Spaces), stringSet(q.TopicIDs), stringSet(q.SpanIDs), stringSet(q.Tags)
	for _, set := range []struct {
		values []string
		max    int
	}{{q.Spaces, 128}, {q.TopicIDs, 64}, {q.SpanIDs, 64}, {q.Tags, 64}} {
		for _, value := range set.values {
			if !textOK(value, set.max, true) {
				return q, invalid("invalid query selector")
			}
		}
	}
	q.Kinds = slices.Clone(q.Kinds)
	slices.Sort(q.Kinds)
	q.Kinds = slices.Compact(q.Kinds)
	for _, k := range q.Kinds {
		if !validKind(k) {
			return q, invalid("unknown kind")
		}
	}
	q.Keywords = strings.Join(strings.Fields(q.Keywords), " ")
	if !textOK(q.Keywords, 512, false) || len(strings.Fields(q.Keywords)) > 32 || !textOK(q.ExternalRef, 256, false) || len(q.Cursor) > 4096 {
		return q, invalid("oversize keyword, external reference or cursor")
	}
	var err error
	q.Occurred, err = memory.ResolveRange(q.Occurred)
	if err != nil {
		return q, err
	}
	q.Recorded, err = memory.ResolveRange(q.Recorded)
	if err != nil {
		return q, err
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.ContextLimit == 0 {
		q.ContextLimit = 100
	}
	if q.LinkLimit == 0 {
		q.LinkLimit = 100
	}
	if q.Limit < 1 || q.Limit > 100 || q.ContextLimit < 1 || q.ContextLimit > 200 || q.LinkLimit < 1 || q.LinkLimit > 200 {
		return q, invalid("match limit must be 1..100; context/link limits 1..200")
	}
	if q.Order == "" {
		switch {
		case len(q.TopicIDs) > 0 || q.ExternalRef != "":
			q.Order = memory.Chronological
		case q.Keywords != "":
			q.Order = memory.Relevance
		default:
			q.Order = memory.Newest
		}
	}
	if !slices.Contains([]memory.Order{memory.Chronological, memory.Newest, memory.Relevance}, q.Order) || (q.Order == memory.Relevance && q.Keywords == "") {
		return q, invalid("invalid order, or relevance without keywords")
	}
	return q, nil
}
