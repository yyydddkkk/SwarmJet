package mcp

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type queuedMCPBackend struct {
	*MemoryBackend
	mu      sync.Mutex
	waiting []domain.Agent
}

func (b *queuedMCPBackend) EnqueueSpawn(_ context.Context, agent domain.Agent, _ domain.Message, _ domain.Limits) (string, int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, previous := range b.waiting {
		if previous.ID == agent.ID {
			if previous.Task != agent.Task {
				return "", 0, domain.ErrConflict
			}
			return agent.ID, i + 1, nil
		}
	}
	b.waiting = append(b.waiting, agent)
	return agent.ID, len(b.waiting), nil
}

func TestMCPSpawnReportsQueuePosition(t *testing.T) {
	env := newTestEnv(t, Options{})
	backend := &queuedMCPBackend{MemoryBackend: env.backend}
	queuedServer := httptest.NewServer(NewHandler(backend, Options{}))
	t.Cleanup(queuedServer.Close)
	env.server = queuedServer
	ctx := context.Background()
	hive := env.seedHive("queue", "acme/queue")
	hive.Config.MaxConcurrentForagers = 1
	if _, err := backend.UpdateHive(ctx, hive); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, ChainID: "busy"}); err != nil {
		t.Fatal(err)
	}
	session := env.connect(env.userToken)
	first := decode[spawnForagerOutput](t, callTool(t, session, "spawn_forager", map[string]any{
		"hiveId": hive.ID, "task": "queued one", "requestId": "queue-1",
	}))
	if !first.Queued || first.QueuePosition != 1 || first.AgentID == "" || first.Status != "已排队" {
		t.Fatalf("first queued response: %+v", first)
	}
	retry := decode[spawnForagerOutput](t, callTool(t, session, "spawn_forager", map[string]any{
		"hiveId": hive.ID, "task": "queued one", "requestId": "queue-1",
	}))
	if retry.AgentID != first.AgentID || retry.QueuePosition != 1 {
		t.Fatalf("retry queued response: %+v", retry)
	}
	second := decode[spawnOutput](t, callTool(t, session, "spawn", map[string]any{
		"hiveId": hive.ID, "task": "queued two", "requestId": "queue-2",
	}))
	if !second.Queued || second.QueuePosition != 2 || second.AgentID == "" {
		t.Fatalf("second queued response: %+v", second)
	}
}
