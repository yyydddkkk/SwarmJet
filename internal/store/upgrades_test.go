package store

import (
	"context"
	"errors"
	"testing"

	"swarmjet/internal/domain"
)

func TestUpgradeMaintenanceGatesNewTasksAndRetainsHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	project, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateUpgrade(ctx, "upgrade-one", "ready", map[string]string{"tag": "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	const targetRef = "registry.test/swarmjet/platform@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = s.SaveUpgrade(ctx, "upgrade-one", "ready", true, false, map[string]any{"target": map[string]string{"reference": targetRef}}); err != nil {
		t.Fatal(err)
	}
	if usage, err := s.PlatformImageUsage(ctx, targetRef); err != nil || len(usage) == 0 {
		t.Fatalf("prepared target deletion allowed: usage=%v err=%v", usage, err)
	}
	if err = s.CreateUpgrade(ctx, "upgrade-two", "ready", map[string]string{"tag": "v1.2.4"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("parallel upgrade accepted: %v", err)
	}
	if err = s.SaveUpgrade(ctx, "upgrade-one", "running", true, true, map[string]string{"stage": "draining"}); err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.UpgradeMaintenance(ctx); err != nil || !enabled {
		t.Fatalf("maintenance=%v err=%v", enabled, err)
	}
	if err = s.CreateTask(ctx, testTask("new-task", project, 1)); err == nil {
		t.Fatal("new task admitted during maintenance")
	}
	if err = s.SaveUpgrade(ctx, "upgrade-one", "succeeded", false, false, map[string]string{"stage": "completed"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateTask(ctx, testTask("new-task", project, 1)); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateUpgrade(ctx, "upgrade-two", "preparing", map[string]string{"tag": "v1.2.4"}); err != nil {
		t.Fatal(err)
	}
	records, err := s.Upgrades(ctx, 10)
	if err != nil || len(records) != 2 || records[0].ID != "upgrade-two" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestNodeProtocolVersionSurvivesHeartbeat(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "upgradable", Address: "http://node:8092", Capacity: 1, Version: "v1.2.3", ProtocolVersion: 2}); err != nil {
		t.Fatal(err)
	}
	node, err := s.Node(ctx, "upgradable")
	if err != nil || node.ProtocolVersion != 2 || node.Version != "v1.2.3" {
		t.Fatalf("node=%+v err=%v", node, err)
	}
}

func TestOldNodeCannotReceiveTasksAfterPlatformUpgrade(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	project, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.HeartbeatNode(ctx, domain.Node{ID: "old-node", Address: "http://node:8092", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateUpgrade(ctx, "completed-upgrade", "ready", map[string]string{"tag": "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveUpgrade(ctx, "completed-upgrade", "succeeded", false, false, map[string]string{"tag": "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTaskScheduled(ctx, testTask("after-upgrade", project, 1), true)
	if err != nil || task.NodeID != "" || task.AdmissionStatus != "waiting" {
		t.Fatalf("old node admitted: %+v %v", task, err)
	}
	if err = s.HeartbeatNode(ctx, domain.Node{ID: "old-node", Address: "http://node:8092", Capacity: 1, ProtocolVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if admitted, err := s.AdmitPendingTasks(ctx, 1); err != nil || admitted != 1 {
		t.Fatalf("upgraded node admission=%d err=%v", admitted, err)
	}
}
