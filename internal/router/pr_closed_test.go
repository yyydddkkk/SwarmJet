package router

import (
	"context"
	"fmt"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"strings"
	"testing"
)

func TestClosedPRCompletesOwnerWithOriginalEvent(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprint(merged), func(t *testing.T) {
			b := NewMemoryBackend()
			hive := newTestHive(t, b, domain.HiveConfig{})
			owner := b.AddForager(domain.Agent{HiveID: hive.ID, Role: domain.RoleForager, State: domain.AgentParked, PRNumber: 42})
			event := Event{Type: EventPullRequest, Action: "closed", Repository: hive.Repository, Number: 42, Merged: merged, DeliveryID: "close-42", SenderLogin: "swarmjet[bot]", SenderType: "Bot"}
			r := NewWithOptions(b, Options{AppSlug: "swarmjet"})
			for i := 0; i < 2; i++ {
				if d, err := r.Route(context.Background(), event); err != nil || d.ForagerID != owner.ID {
					t.Fatalf("route = %+v, %v", d, err)
				}
			}
			messages := b.MessagesTo(hive.ID, domain.ForagerAddress(owner.ID))
			if len(messages) != 2 {
				t.Fatalf("messages = %+v", messages)
			}
			if messages[0].Kind != domain.MessageControl || messages[0].Trust != domain.TrustSystem || !strings.HasPrefix(messages[0].Body, "complete\nafter_event\n") || !strings.Contains(messages[0].Body, fmt.Sprintf("merged=%t", merged)) {
				t.Fatalf("control = %+v", messages[0])
			}
			if messages[1].Kind != domain.MessageEvent || messages[1].Trust != domain.TrustExternal || messages[1].Body != Digest(event) {
				t.Fatalf("event = %+v", messages[1])
			}
		})
	}
}

func TestClosedPRRedeliveryDoesNotCompleteRelatedAgent(t *testing.T) {
	for _, revision := range []string{"head-sha", ""} {
		t.Run(fmt.Sprintf("related-revision=%q", revision), func(t *testing.T) {
			b := NewMemoryBackend()
			hive := newTestHive(t, b, domain.HiveConfig{})
			owner := b.AddForager(domain.Agent{ID: "owner", HiveID: hive.ID, Role: domain.RoleForager, State: domain.AgentParked, PRNumber: 42, Branch: "swarm/owner"})
			related := b.AddForager(domain.Agent{ID: "related", HiveID: hive.ID, Role: domain.RoleForager, State: domain.AgentActive, PRNumber: 42, Branch: "swarm/related", Revision: revision, ParentID: owner.ID})
			event := Event{Type: EventPullRequest, Action: "closed", Repository: hive.Repository, Number: 42, Branch: owner.Branch, DeliveryID: "closed-once"}
			r := New(b)
			if first, err := r.Route(context.Background(), event); err != nil || first.ForagerID != owner.ID {
				t.Fatalf("first route = %+v, %v", first, err)
			}
			b.mu.Lock()
			completed := b.agents[owner.ID]
			completed.State = domain.AgentCompleted
			b.agents[owner.ID] = completed
			b.mu.Unlock()
			decision, err := r.Route(context.Background(), event)
			if err != nil || decision.Action != ActionIgnored {
				t.Fatalf("redelivery = %+v, %v", decision, err)
			}
			if messages := b.MessagesTo(hive.ID, domain.ForagerAddress(related.ID)); len(messages) != 0 {
				t.Fatalf("closed event leaked into related Agent: %+v", messages)
			}
		})
	}
}

func TestClosedPRWithoutBranchDoesNotCompletePinnedAgent(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	b.AddForager(domain.Agent{HiveID: hive.ID, Role: domain.RoleForager, State: domain.AgentActive, PRNumber: 42, Revision: "head-sha"})
	decision, err := New(b).Route(context.Background(), Event{Type: EventPullRequest, Action: "closed", Repository: hive.Repository, Number: 42, DeliveryID: "closed-once"})
	if err != nil || decision.Action != ActionIgnored || len(b.Messages()) != 0 {
		t.Fatalf("closed PR resolved to a pinned Agent: decision=%+v err=%v messages=%+v", decision, err, b.Messages())
	}
}
