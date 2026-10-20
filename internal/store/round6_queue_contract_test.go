package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestQueueSnapshotAndEventShareRepresentation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/queue-contract")
	agentJSON, err := json.Marshal(domain.Agent{ID: "queued-contract", RoleName: "worker", Task: strings.Repeat("星", 201)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO spawn_queue(hive_id,agent_id,agent,task_message,limits) VALUES($1,'queued-contract',$2,'{"body":"different delivery text"}','{}')`, h.ID, agentJSON); err != nil {
		t.Fatal(err)
	}
	queue, err := s.SpawnQueue(ctx, h.ID)
	if err != nil || len(queue) != 1 {
		t.Fatalf("snapshot = %+v %v", queue, err)
	}
	data, err := s.EventData(ctx, EventRef{Type: "queue", HiveID: h.ID, ID: "queued-contract"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		SpawnQueueEntry
		State string `json:"state"`
	}
	if err := json.Unmarshal(encoded, &event); err != nil {
		t.Fatal(err)
	}
	if event.State != "waiting" || event.SpawnQueueEntry != queue[0] || event.Task != strings.Repeat("星", 200) {
		t.Fatalf("event=%+v snapshot=%+v", event, queue[0])
	}
}
