package forager

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestQueenReportPreservesStreamedReply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		updates []cell.Update
		want    string
	}{
		{"english", []cell.Update{{Kind: cell.UpdateText, Text: "Choose "}, {Kind: cell.UpdateText, Text: "pstack"}, {Kind: cell.UpdateText, Text: "."}}, "Choose pstack."},
		{"chinese", []cell.Update{{Kind: cell.UpdateText, Text: "请选择"}, {Kind: cell.UpdateThought, Text: "private reasoning"}, {Kind: cell.UpdateText, Text: "方法论"}, {Kind: cell.UpdateText, Text: "。"}}, "请选择方法论。"},
		{"after tools", []cell.Update{{Kind: cell.UpdateText, Text: "Inspecting the repository.\n\n"}, {Kind: cell.UpdateToolCall, Title: "read"}, {Kind: cell.UpdateToolDone, Text: "README content"}, {Kind: cell.UpdateText, Text: "Choose "}, {Kind: cell.UpdateText, Text: "minimal or pstack"}, {Kind: cell.UpdateText, Text: "."}}, "Inspecting the repository.\n\nChoose minimal or pstack."},
		{"tool update between text chunks", []cell.Update{{Kind: cell.UpdateText, Text: "Choose "}, {Kind: cell.UpdateToolCall, Title: "read"}, {Kind: cell.UpdateToolDone}, {Kind: cell.UpdateText, Text: "pstack."}}, "Choose pstack."},
		{"tools without final text", []cell.Update{{Kind: cell.UpdateText, Text: "Inspection completed."}, {Kind: cell.UpdateToolCall, Title: "read"}, {Kind: cell.UpdateToolDone, Text: "README content"}}, "Inspection completed."},
		{"bounded unicode", []cell.Update{{Kind: cell.UpdateText, Text: strings.Repeat("蜂", 4000)}, {Kind: cell.UpdateText, Text: strings.Repeat("巢", 4000)}, {Kind: cell.UpdateText, Text: "overflow"}}, strings.Repeat("蜂", 3992) + strings.Repeat("巢", 4000) + "overflow"},
		{"new answer after full progress", []cell.Update{{Kind: cell.UpdateText, Text: strings.Repeat("x", 4000)}, {Kind: cell.UpdateText, Text: strings.Repeat("x", 4000)}, {Kind: cell.UpdateToolCall, Title: "read"}, {Kind: cell.UpdateToolDone}, {Kind: cell.UpdateText, Text: "Final answer"}, {Kind: cell.UpdateText, Text: "。"}}, strings.Repeat("x", domain.MaxMessageBody-utf8.RuneCountInString("Final answer。")) + "Final answer。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQueenFixture(t, nil)
			f.runtime.script(testQueenID).updates = tc.updates
			msg := queenMessage(f, "reply")
			f.notify(msg)
			waitQueenTurn(t, f, msg.ID)
			f.store.mu.Lock()
			defer f.store.mu.Unlock()
			var reports []domain.Message
			for _, message := range f.store.sent {
				if message.From == domain.AddressQueen && message.Kind == domain.MessageReport {
					reports = append(reports, message)
				}
			}
			if len(reports) != 1 {
				t.Fatalf("reports=%d, want 1", len(reports))
			}
			if reports[0].Body != tc.want {
				t.Fatalf("body length=%d prefix=%q, want length=%d prefix=%q", utf8.RuneCountInString(reports[0].Body), truncateRunes(reports[0].Body, 80), utf8.RuneCountInString(tc.want), truncateRunes(tc.want, 80))
			}
			if reports[0].To != domain.LocalAddress("user") || !utf8.ValidString(reports[0].Body) || utf8.RuneCountInString(reports[0].Body) > domain.MaxMessageBody {
				t.Fatalf("invalid report: %+v", reports[0])
			}
		})
	}
}

func TestQueenReportDoesNotReusePreviousTurnText(t *testing.T) {
	f := newQueenFixture(t, nil)
	for i, text := range []string{"First reply", "Second reply"} {
		script := f.runtime.script(testQueenID)
		script.mu.Lock()
		script.updates = []cell.Update{{Kind: cell.UpdateText, Text: text}, {Kind: cell.UpdateText, Text: "。"}}
		script.mu.Unlock()
		msg := queenMessage(f, []string{"first", "second"}[i])
		f.notify(msg)
		waitQueenTurn(t, f, msg.ID)
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	var reports []domain.Message
	for _, message := range f.store.sent {
		if message.From == domain.AddressQueen && message.Kind == domain.MessageReport {
			reports = append(reports, message)
		}
	}
	if len(reports) != 2 || reports[0].Body != "First reply。" || reports[1].Body != "Second reply。" {
		t.Fatalf("reports=%+v", reports)
	}
}

func TestForagerWithoutHandoffReportsCompleteStream(t *testing.T) {
	f := newFixture(t, nil)
	f.store.mu.Lock()
	agent := f.store.agents[testAgentID]
	agent.Handoff = nil
	f.store.agents[testAgentID] = agent
	f.store.mu.Unlock()
	f.runtime.script(testAgentID).updates = []cell.Update{{Kind: cell.UpdateText, Text: "Implemented the change"}, {Kind: cell.UpdateText, Text: "."}}
	f.notify(f.taskMessage("task", "Implement the change"))
	f.wantState(testAgentID, domain.AgentParked)
	reports := f.store.reportsFrom(testAgentID)
	if len(reports) != 1 || reports[0].Body != "Implemented the change." {
		t.Fatalf("reports=%+v", reports)
	}
	calls := f.publisher.callsCopy()
	if len(calls) != 1 || calls[0].body != "Implemented the change." {
		t.Fatalf("publish calls=%+v", calls)
	}
}
