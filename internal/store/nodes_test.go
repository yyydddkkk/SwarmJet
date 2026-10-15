package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
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
	waiting, err := s.CreateTaskScheduled(ctx, testTask("t3", p, 3), true)
	if err != nil || waiting.NodeID != "" || waiting.AdmissionStatus != "waiting" {
		t.Fatalf("expected waiting task, got %+v %v", waiting, err)
	}
	pending, err := s.Pending(ctx)
	if err != nil || len(pending) != 2 {
		t.Fatalf("waiting task emitted start: %+v %v", pending, err)
	}
	t3, err := s.CreateTaskScheduled(ctx, testTask("t4", p, 4), false)
	if err != nil || t3.NodeID != "" || t3.AdmissionStatus != "waiting" || t3.AdmissionBackend != "local" {
		t.Fatalf("local task bypassed earlier work: %+v err=%v", t3, err)
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
	waiting, err := s.CreateTaskScheduled(ctx, testTask("t1", p, 1), true)
	if err != nil || waiting.NodeID != "" || waiting.AdmissionStatus != "waiting" {
		t.Fatalf("offline node was selected: %+v %v", waiting, err)
	}
}

func TestPendingTaskAdmissionAfterCapacityIsReleased(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.HeartbeatNode(ctx, domain.Node{ID: "a", Address: "http://a", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateTaskScheduled(ctx, testTask("first", p, 1), true)
	if err != nil || first.NodeID != "a" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := s.CreateTaskScheduled(ctx, testTask("second", p, 2), true)
	if err != nil || second.AdmissionStatus != "waiting" || second.NodeID != "" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	page, err := s.TaskPage(ctx, TaskPageOptions{View: "waiting"})
	if err != nil || page.Total != 1 || page.Counts.Waiting != 1 || page.Counts.Active != 1 {
		t.Fatalf("waiting page=%+v %v", page, err)
	}
	if count, err := s.AdmitPendingTasks(ctx, 10); err != nil || count != 0 {
		t.Fatalf("admitted before capacity: %d %v", count, err)
	}
	first.Stage = "waiting_approval"
	if err := s.SaveTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.NodeStatuses(ctx)
	if err != nil || len(statuses) != 1 || statuses[0].Running != 0 {
		t.Fatalf("waiting approval occupied capacity: %+v %v", statuses, err)
	}
	if count, err := s.AdmitPendingTasks(ctx, 10); err != nil || count != 1 {
		t.Fatalf("admission=%d %v", count, err)
	}
	stored, err := s.Task(ctx, second.ID)
	if err != nil || stored.NodeID != "a" || stored.AdmissionStatus != "assigned" {
		t.Fatalf("assigned=%+v %v", stored, err)
	}
	if count, err := s.AdmitPendingTasks(ctx, 10); err != nil || count != 0 {
		t.Fatalf("duplicate admission=%d %v", count, err)
	}
	var starts int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM commands WHERE id='start-second'`).Scan(&starts); err != nil || starts != 1 {
		t.Fatalf("start commands=%d %v", starts, err)
	}
}

func TestProjectAdmissionLimitAndQueueBound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.HeartbeatNode(ctx, domain.Node{ID: "a", Address: "http://a", Capacity: 128}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxActiveTasksPerProject; i++ {
		if _, err := s.CreateTaskScheduled(ctx, testTask(fmt.Sprintf("active-%d", i), p, int64(i+1)), true); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < maxPendingTasksPerProject; i++ {
		task, err := s.CreateTaskScheduled(ctx, testTask(fmt.Sprintf("waiting-%d", i), p, int64(100+i)), true)
		if err != nil || task.AdmissionStatus != "waiting" {
			t.Fatalf("waiting=%+v %v", task, err)
		}
	}
	if _, err := s.CreateTaskScheduled(ctx, testTask("overflow", p, 999), true); !errors.Is(err, ErrAdmissionQueueFull) {
		t.Fatalf("queue overflow error=%v", err)
	}
}

func TestLocalTasksUseBoundedAdmissionWithoutNode(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxActiveTasksPerProject; i++ {
		task, err := s.CreateTaskScheduled(ctx, testTask(fmt.Sprintf("local-%d", i), p, int64(i+1)), false)
		if err != nil || task.NodeID != "" || task.AdmissionStatus != "assigned" {
			t.Fatalf("local task=%+v %v", task, err)
		}
	}
	waiting, err := s.CreateTaskScheduled(ctx, testTask("local-waiting", p, 10), false)
	if err != nil || waiting.AdmissionBackend != "local" || waiting.AdmissionStatus != "waiting" {
		t.Fatalf("waiting local task=%+v %v", waiting, err)
	}
	var starts int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM commands WHERE body->>'kind'='start'`).Scan(&starts); err != nil || starts != maxActiveTasksPerProject {
		t.Fatalf("premature start=%d %v", starts, err)
	}
	first, err := s.Task(ctx, "local-0")
	if err != nil {
		t.Fatal(err)
	}
	first.Stage = "waiting_dependencies"
	if err = s.SaveTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	if count, err := s.AdmitPendingTasks(ctx, 1); err != nil || count != 1 {
		t.Fatalf("local admission=%d %v", count, err)
	}
	got, err := s.Task(ctx, waiting.ID)
	if err != nil || got.NodeID != "" || got.AdmissionStatus != "assigned" {
		t.Fatalf("admitted local=%+v %v", got, err)
	}
}

func TestCancelPendingTaskDoesNotStartOrDeliverSignals(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := s.CreateTaskScheduled(ctx, testTask("cancel-before-start", p, 1), true)
	if err != nil || waiting.AdmissionStatus != "waiting" {
		t.Fatalf("waiting=%+v %v", waiting, err)
	}
	if err := s.Enqueue(ctx, waiting.ID, domain.Command{ID: "note-before-start", Kind: "note", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.Pending(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("premature signal=%+v %v", pending, err)
	}
	if cancelled, err := s.CancelPendingTask(ctx, waiting.ID); err != nil || !cancelled {
		t.Fatalf("cancelled=%v %v", cancelled, err)
	}
	if count, err := s.AdmitPendingTasks(ctx, 10); err != nil || count != 0 {
		t.Fatalf("cancelled task admitted=%d %v", count, err)
	}
	if pending, err := s.Pending(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("cancelled task signal=%+v %v", pending, err)
	}
	stored, err := s.Task(ctx, waiting.ID)
	if err != nil || stored.Stage != "cancelled" {
		t.Fatalf("cancelled task=%+v %v", stored, err)
	}
}

func TestPendingNoteIncludedInStartSnapshot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := s.CreateTaskScheduled(ctx, testTask("wait-for-start", p, 1), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Enqueue(ctx, waiting.ID, domain.Command{ID: "early-note", Kind: "note", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err = s.HeartbeatNode(ctx, domain.Node{ID: "a", Address: "http://a", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	if count, err := s.AdmitPendingTasks(ctx, 1); err != nil || count != 1 {
		t.Fatalf("admission=%d %v", count, err)
	}
	pending, err := s.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].Command.Kind != "start" {
		t.Fatalf("first delivery=%+v %v", pending, err)
	}
	if err = s.Delivered(ctx, pending[0].Command.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Pending(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("note already belongs to the start snapshot: %+v %v", pending, err)
	}
	stored, err := s.Task(ctx, waiting.ID)
	if err != nil || len(stored.Notes) != 1 || stored.Notes[0] != "hello" {
		t.Fatalf("start snapshot lost pending note: %+v %v", stored, err)
	}
}
