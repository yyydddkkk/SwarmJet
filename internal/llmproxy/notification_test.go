package llmproxy

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestBudgetNotificationTarget(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, parent := range []bool{false, true} {
			backend := newFakeBackend()
			backend.queenEnabled = enabled
			agent := backend.agents["agent-1"]
			agent.ParentID = ""
			want := domain.LocalAddress("human")
			backend.inbox[string(domain.ForagerAddress(agent.ID))] = []domain.Message{{Kind: domain.MessageTask, From: want}}
			if parent {
				agent.ParentID = "parent"
				backend.agents["parent"] = domain.Agent{ID: "parent", Role: domain.RoleForager}
				want = domain.ForagerAddress("parent")
			}
			if enabled {
				want = domain.AddressQueen
			}
			proxy := &Proxy{store: backend, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			for range 2 {
				proxy.notifyBudget(context.Background(), agent, &budgetExceeded{name: "maxCostPerAgentUsd", limit: 1, current: 2})
			}
			if backend.messageCount() != 1 {
				t.Fatalf("enabled=%v parent=%v: notifications=%d", enabled, parent, backend.messageCount())
			}
			message := backend.lastMessage()
			if message.To != want || message.From != domain.AddressSystem || message.Trust != domain.TrustSystem || message.ChainID != agent.ChainID {
				t.Fatalf("enabled=%v parent=%v: message=%+v, want %q", enabled, parent, message, want)
			}
		}
	}
}
