package forager

import (
	"errors"
	"fmt"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestPRClosedFinalTurnDoesNotPublish(t *testing.T) {
	for _, merged := range []bool{false, true} {
		for _, active := range []bool{false, true} {
			t.Run(fmt.Sprintf("merged=%t/active=%t", merged, active), func(t *testing.T) {
				f := newFixture(t, func(opts *Options) { opts.UnhandledLimit = 1 })
				script := f.runtime.script(testAgentID)
				if active {
					script.blockPrompts = 1
					f.notify(f.taskMessage("work", "initial work"))
					eventually(t, 5*time.Second, func() bool { return script.promptTotal() == 1 }, "initial turn did not start")
				}
				reason := fmt.Sprintf("PR #42 closed (merged=%t)", merged)
				control := f.messageTo(testAgentID, "close-control", domain.AddressSystem, domain.MessageControl, "complete\nafter_event\n"+reason)
				control.Trust = domain.TrustSystem
				// Update the authoritative fake Inbox provenance as production SendMessage does.
				f.store.mu.Lock()
				for i := range f.store.messages {
					if f.store.messages[i].ID == control.ID {
						f.store.messages[i].Trust = domain.TrustSystem
					}
				}
				f.store.mu.Unlock()
				event := f.messageTo(testAgentID, "closed-event", domain.AddressSystem, domain.MessageEvent, "GitHub event: pull_request.closed\n"+reason)
				f.notify(control)
				f.notify(event)
				if active {
					script.releasePrompt()
				}
				f.wantState(testAgentID, domain.AgentCompleted)
				eventually(t, 5*time.Second, func() bool { return f.runtime.destroyCount(testAgentID) == 1 }, "Cell not destroyed")
				prompts := script.prompts()
				want := 1
				if active {
					want = 2
				}
				if len(prompts) != want || !strings.Contains(prompts[len(prompts)-1], "pull_request.closed") {
					t.Fatalf("prompts = %v", prompts)
				}
				if calls := f.publisher.callsCopy(); len(calls) != 0 {
					t.Fatalf("published after close: %+v", calls)
				}
				reports := f.store.reportsFrom(testAgentID)
				if len(reports) == 0 || !strings.Contains(reports[len(reports)-1].Body, reason) {
					t.Fatalf("reports = %+v", reports)
				}
				if !f.store.isHandled(event.ID) || !f.store.isHandled(control.ID) {
					t.Fatal("close messages not handled")
				}
			})
		}
	}
}

func TestPublicationFailureSentToOwner(t *testing.T) {
	f := newFixture(t, nil)
	f.publisher.err = errors.New("push rejected: non-fast-forward")
	f.notify(f.taskMessage("work", "do work"))
	f.wantState(testAgentID, domain.AgentParked)
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	for _, m := range f.store.sent {
		if m.To == domain.ForagerAddress(testAgentID) && m.Trust == domain.TrustSystem && strings.Contains(m.Body, "non-fast-forward") && strings.Contains(m.Body, "merge the latest base") {
			return
		}
	}
	t.Fatal("owner did not receive actionable system publication failure")
}

func TestFinalClosureEventSurvivesPromptCaps(t *testing.T) {
	agent := domain.Agent{ID: testAgentID, Task: strings.Repeat("task", 4000)}
	messages := make([]domain.Message, MaxPromptMessages)
	for i := range messages {
		messages[i] = domain.Message{Body: strings.Repeat("earlier work", 1000)}
	}
	control := domain.Message{From: domain.AddressSystem, Trust: domain.TrustSystem, Kind: domain.MessageControl, Body: "complete\nafter_event\nPR #42 closed (merged=false)"}
	event := domain.Message{From: domain.AddressSystem, Trust: domain.TrustExternal, Kind: domain.MessageEvent, Body: "GitHub event: pull_request.closed\nNumber: #42\nExternal content (untrusted): final event"}
	messages = append(messages, control, event)
	text := buildTurnPrompt(agent, domain.Hive{}, messages, true)
	if !strings.Contains(text, event.Body) || !strings.Contains(text, "merged=false") || !strings.Contains(text, "trust=external") {
		t.Fatalf("final prompt omitted closure: %s", text)
	}
}
