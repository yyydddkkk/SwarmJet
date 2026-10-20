package forager

import (
	"context"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestScanReconcilesParkedPRAndRunsClosureCleanup(t *testing.T) {
	var f *fixture
	calls := 0
	f = newFixture(t, func(opts *Options) {
		opts.ReconcileParkedPR = func(ctx context.Context, hive domain.Hive, agent domain.Agent) error {
			calls++
			_, err := opts.Store.SendMessage(ctx, domain.Message{
				ID: "reconciled-close-control", HiveID: hive.ID, From: domain.AddressSystem,
				To: domain.ForagerAddress(agent.ID), Kind: domain.MessageControl,
				Trust: domain.TrustSystem, Body: "complete\nafter_event\nPR #42 closed (merged=true)",
			})
			if err != nil {
				return err
			}
			_, err = opts.Store.SendMessage(ctx, domain.Message{
				ID: "reconciled-close-event", HiveID: hive.ID, From: domain.AddressSystem,
				To: domain.ForagerAddress(agent.ID), Kind: domain.MessageEvent,
				Trust: domain.TrustExternal, Body: "GitHub event: pull_request.closed",
			})
			return err
		}
	})
	f.store.mu.Lock()
	a := f.store.agents[testAgentID]
	a.State, a.PRNumber = domain.AgentParked, 42
	f.store.agents[a.ID] = a
	f.store.mu.Unlock()
	if err := f.sup.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wantState(testAgentID, domain.AgentCompleted)
	eventually(t, 5*time.Second, func() bool { return f.runtime.destroyCount(testAgentID) == 1 }, "reconciled owner Cell not removed")
	if calls != 1 || len(f.publisher.callsCopy()) != 0 {
		t.Fatalf("reconcile calls=%d publication calls=%d", calls, len(f.publisher.callsCopy()))
	}
}
