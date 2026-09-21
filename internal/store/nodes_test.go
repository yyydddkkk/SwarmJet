package store

import (
	"context"
	"errors"
	"testing"

	"swarmjet/internal/domain"
)

func TestNodeHeartbeatStatusesAndAddress(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "a", Address: "http://a:8092", Capacity: 2, Version: "v"}); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "b", Address: "http://b:8092", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	node, err := s.Node(ctx, "a")
	if err != nil || node.Address != "http://a:8092" || node.Capacity != 2 {
		t.Fatalf("node=%+v err=%v", node, err)
	}
	address, err := s.NodeAddress(ctx, "a")
	if err != nil || address != "http://a:8092" {
		t.Fatalf("address=%q err=%v", address, err)
	}
	statuses, err := s.NodeStatuses(ctx)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("statuses=%+v err=%v", statuses, err)
	}
	for _, st := range statuses {
		if !st.Online || st.Running != 0 {
			t.Fatalf("unexpected status %+v", st)
		}
	}
	if _, err := s.Node(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node error=%v", err)
	}
}

func TestCreateTaskScheduledHonorsCapacityAndNodeAffinity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := testProject()
	if _, err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "a", Address: "http://a", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "b", Address: "http://b", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	t1, err := s.CreateTaskScheduled(ctx, testTask("t1", p, 1), true)
	if err != nil || t1.NodeID != "a" {
		t.Fatalf("t1 assigned to %q err=%v", t1.NodeID, err)
	}
	t2, err := s.CreateTaskScheduled(ctx, testTask("t2", p, 2), true)
	if err != nil || t2.NodeID != "b" {
		t.Fatalf("t2 assigned to %q err=%v", t2.NodeID, err)
	}
	if _, err := s.CreateTaskScheduled(ctx, testTask("t3", p, 3), true); !errors.Is(err, ErrNoNode) {
		t.Fatalf("expected ErrNoNode, got %v", err)
	}
	t3, err := s.CreateTaskScheduled(ctx, testTask("t3", p, 3), false)
	if err != nil || t3.NodeID != "" {
		t.Fatalf("local fallback assigned to %q err=%v", t3.NodeID, err)
	}
	// The assignment survives a round trip and stays with the task.
	stored, err := s.Task(ctx, "t1")
	if err != nil || stored.NodeID != "a" {
		t.Fatalf("stored node=%q err=%v", stored.NodeID, err)
	}
}

func TestOfflineNodeIsNotSelected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := testProject()
	if _, err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "a", Address: "http://a", Capacity: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE nodes SET last_seen=now()-interval '10 minutes' WHERE id='a'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTaskScheduled(ctx, testTask("t1", p, 1), true); !errors.Is(err, ErrNoNode) {
		t.Fatalf("offline node was selected: %v", err)
	}
}
