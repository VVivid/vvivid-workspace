package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/mcpserver"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ScenarioResult struct {
	OriginalRequest memory.RememberRequest
	Original        mcpserver.RememberReceipt
	Reuse           mcpserver.RememberReceipt
	Replacement     mcpserver.RememberReceipt
	Filtered        memory.Page
}

func event(kind memory.Kind, title, date, source string) memory.Entry {
	when, err := time.Parse(time.RFC3339, date)
	if err != nil {
		panic(err) // Fixed synthetic scenario constants, not user input.
	}
	return memory.Entry{
		Kind: kind, Title: title, Payload: json.RawMessage(`{}`), Historical: true, OccurredAt: new(when),
		Provenance: memory.Provenance{Attribution: "synthetic-example", Sources: []memory.Source{{Reference: source, Excerpt: "Invented fixture discussion; never evidence of a real decision."}}},
	}
}

// RunScenario uses the official SDK over authenticated HTTP. All reasoning and
// source references below are deliberately invented, and never seed user data.
func RunScenario(ctx context.Context, session *mcp.ClientSession, out io.Writer) (ScenarioResult, error) {
	var result ScenarioResult
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 3 {
		return result, errors.New("synthetic discovery did not find the three journal tools")
	}
	root := event(memory.Topic, "PAYM-123 payment service", "2025-04-01T09:00:00Z", "synthetic://GuestUp/PAYM-123/context")
	root.LocalID, root.ExternalRef = "root", "PAYM-123"
	root.Context = "Synthetic GuestUp prototype: one operator, one process, modest payment volume."
	goChoice := event(memory.Decision, "Language: Go", "2025-04-02T09:00:00Z", "synthetic://GuestUp/PAYM-123/language")
	goChoice.LocalID = "language"
	goChoice.Parent = memory.Reference{LocalID: "root"}
	goChoice.Payload = json.RawMessage(`{"question":"Which language fits the synthetic service?","outcome":"Go","reasons":["One deployable executable","Existing Go experience in the invented team"],"alternatives":[{"option":"Java","disposition":"rejected","reasons":["Extra runtime packaging for this small prototype"]}]}`)
	sqliteChoice := event(memory.Decision, "Persistence: SQLite", "2025-04-03T09:00:00Z", "synthetic://GuestUp/PAYM-123/persistence")
	sqliteChoice.Parent = memory.Reference{LocalID: "language"}
	sqliteChoice.Tags = []string{"architecture", "storage"}
	sqliteChoice.Payload = json.RawMessage(`{"question":"Which store fits the original synthetic constraints?","outcome":"SQLite","reasons":["One writer and local durable storage","No separate database service to operate"],"alternatives":[{"option":"PostgreSQL","disposition":"rejected","reasons":["A separate database service adds operational overhead","Multi-writer coordination was unnecessary under the original constraints"]}],"fixture_number":9007199254740993}`)
	result.OriginalRequest = memory.RememberRequest{Space: "GuestUp", Key: "synthetic-paym-123", Entries: []memory.Entry{root, goChoice, sqliteChoice}}
	result.Original, err = Call[mcpserver.RememberReceipt](ctx, session, "remember", result.OriginalRequest)
	if err != nil {
		return result, err
	}
	sqliteID := result.Original.Records[2].SpanID
	reuse := event(memory.Topic, "PAYM-1234 payment reconciliation", "2025-05-01T09:00:00Z", "synthetic://GuestUp/PAYM-1234/reuse")
	reuse.ExternalRef = "PAYM-1234"
	reuse.Context = "Synthetic later ticket explicitly reuses PAYM-123's storage reasoning."
	reuse.Links = []memory.LinkInput{{Type: memory.RelatedTo, Target: memory.Reference{SpanID: result.Original.TopicID}}, {Type: memory.DependsOn, Target: memory.Reference{SpanID: sqliteID}}}
	result.Reuse, err = Call[mcpserver.RememberReceipt](ctx, session, "remember", memory.RememberRequest{Space: "Planning", Key: "synthetic-paym-1234", Entries: []memory.Entry{reuse}})
	if err != nil {
		return result, err
	}
	replacement := event(memory.Decision, "Changed requirements: PostgreSQL", "2025-06-01T09:00:00Z", "synthetic://GuestUp/PAYM-123/revised-requirements")
	replacement.Context = "Synthetic new requirements now demand several concurrent writer instances."
	replacement.Payload = json.RawMessage(`{"question":"Which store fits the new synthetic multi-writer requirements?","outcome":"PostgreSQL","reasons":["Concurrent writers are now required","The invented operator now accepts a separate database service"],"alternatives":[{"option":"SQLite","disposition":"rejected","reasons":["The previous one-writer assumption no longer holds"]}]}`)
	replacement.Links = []memory.LinkInput{{Type: memory.Supersedes, Target: memory.Reference{SpanID: sqliteID}}}
	result.Replacement, err = Call[mcpserver.RememberReceipt](ctx, session, "remember", memory.RememberRequest{Space: "GuestUp", Key: "synthetic-replacement", TopicID: result.Original.TopicID, Entries: []memory.Entry{replacement}})
	if err != nil {
		return result, err
	}
	// All filters intersect, but older ancestors and later incoming replacement
	// references must survive even though they fall outside this date range.
	result.Filtered, err = Call[memory.Page](ctx, session, "retrieve", memory.Query{
		Spaces: []string{"GuestUp"}, TopicIDs: []string{result.Original.TopicID}, Keywords: "PostgreSQL", Tags: []string{"storage"},
		Occurred: memory.TimeRange{Calendar: &memory.CalendarRange{StartDate: "2025-04-03", EndDate: "2025-04-04", Timezone: "UTC"}},
	})
	if err != nil {
		return result, err
	}
	page := result.Filtered
	if !page.Complete || len(page.Matches) != 1 || len(page.Context) != 2 || len(page.Relationships) != 3 || !bytes.Contains(page.Matches[0].Payload, []byte("9007199254740993")) {
		return result, errors.New("synthetic retrieval lost bounded context, relationships or exact JSON")
	}
	history, err := Call[memory.Page](ctx, session, "retrieve", memory.Query{Spaces: []string{"GuestUp"}, TopicIDs: []string{result.Original.TopicID}})
	if err != nil || !history.Complete || len(history.Matches) != 4 {
		return result, errors.New("synthetic topic history is incomplete")
	}
	later, err := Call[memory.Page](ctx, session, "retrieve", memory.Query{Spaces: []string{"GuestUp"}, SpanIDs: []string{result.Replacement.Records[0].SpanID}})
	if err != nil || len(later.Matches) != 1 {
		return result, errors.New("synthetic replacement expansion failed")
	}
	reused, err := Call[memory.Page](ctx, session, "retrieve", memory.Query{Spaces: []string{"Planning"}, TopicIDs: []string{result.Reuse.TopicID}})
	if err != nil || len(reused.Matches) != 1 || len(reused.Relationships) != 2 {
		return result, errors.New("synthetic linked-topic expansion failed")
	}
	fmt.Fprintln(out, "SYNTHETIC EXAMPLE ONLY: invented reasons and sources, temporary private data.")
	for _, record := range []memory.Record{page.Context[0], page.Context[1], page.Matches[0], reused.Matches[0], later.Matches[0]} {
		fmt.Fprintf(out, "%s [%s] topic=%s\n", record.Title, record.SpanID, record.TopicID)
		if record.Kind == memory.Decision {
			var payload memory.DecisionPayload
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return result, err
			}
			fmt.Fprintf(out, "  Chosen %s: %v\n", payload.Outcome, payload.Reasons)
			for _, alternative := range payload.Alternatives {
				fmt.Fprintf(out, "  %s %s: %v\n", alternative.Disposition, alternative.Option, alternative.Reasons)
			}
		}
		for _, source := range record.Provenance.Sources {
			fmt.Fprintf(out, "  Source: %s\n", source.Reference)
		}
	}
	for _, link := range page.Relationships {
		fmt.Fprintf(out, "  Link: %s [%s/%s] -> [%s/%s]\n", link.Type, link.Source.Space, link.Source.SpanID, link.Target.Space, link.Target.SpanID)
	}
	return result, nil
}
