package router

import (
	"context"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type queuedRouterBackend struct {
	*MemoryBackend
	mu      sync.Mutex
	waiting []domain.Agent
}

func (b *queuedRouterBackend) EnqueueSpawn(_ context.Context, agent domain.Agent, _ domain.Message, _ domain.Limits) (string, int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, previous := range b.waiting {
		if previous.ID == agent.ID {
			return agent.ID, i + 1, nil
		}
	}
	b.waiting = append(b.waiting, agent)
	return agent.ID, len(b.waiting), nil
}

func TestPRHeadQueuesWhenHiveIsFull(t *testing.T) {
	b := &queuedRouterBackend{MemoryBackend: NewMemoryBackend()}
	hive := newTestHive(t, b.MemoryBackend, domain.HiveConfig{MaxConcurrentForagers: 1})
	meth := methodologyWithEvents(domain.EventRule{Event: MethodologyEventPRHead, Role: "verifier"})
	meth.HiveID = hive.ID
	b.SetActiveMethodology(meth)
	owner := platformForager(t, b.MemoryBackend, hive.ID)
	r := NewWithOptions(b, Options{AppSlug: "swarmjet"})
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_synchronize_by_app.json")
	event.DeliveryID = "queued-pr-head"
	first, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Action != ActionQueued || first.QueuePosition != 1 || first.ForagerID == "" {
		t.Fatalf("first decision: %+v", first)
	}
	event.DeliveryID = "queued-pr-head-redelivery"
	second, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if second.Action != ActionQueued || second.QueuePosition != 1 || second.ForagerID != first.ForagerID {
		t.Fatalf("redelivery decision: %+v", second)
	}
	if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(owner.ID))); got != 0 {
		t.Fatalf("owner received %d pr_head messages", got)
	}
}
