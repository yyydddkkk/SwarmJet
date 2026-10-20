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
