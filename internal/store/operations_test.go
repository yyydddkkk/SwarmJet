package store

import (
	"context"
	"testing"
	"time"
)

func TestOperationsSnapshotSeparatesDemandFromRunnerSlots(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	empty, err := s.OperationsSnapshot(ctx)
	if err != nil || empty.InboxPending != 0 || empty.InboxP95ProcessSeconds != 0 {
		t.Fatalf("empty snapshot=%+v err=%v", empty, err)
	}
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.CreateTaskScheduled(ctx, testTask("waiting", p, 1), true)
	if err != nil || queued.AdmissionStatus != "waiting" {
		t.Fatalf("task=%+v err=%v", queued, err)
	}
	if err := s.EnqueueWebhook(ctx, "gitlab", "delivery", "Issue Hook", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Lease(ctx, "agent-slot:node:0", "execution", time.Minute); err != nil || !ok {
		t.Fatalf("Runner slot lease=%v err=%v", ok, err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO commands(id,task_id,body) VALUES('observability-command',$1,'{}'::jsonb)`, queued.ID); err != nil {
		t.Fatal(err)
	}
	// A waiting task may receive a note; its last update must not reset wait age.
	if _, err := s.Pool.Exec(ctx, `UPDATE tasks SET body=jsonb_set(body,'{createdAt}',to_jsonb((now()-interval '1 hour')::text)),updated_at=now() WHERE id=$1`, queued.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := s.OperationsSnapshot(ctx)
	if err != nil || stats.InboxPending != 1 || stats.AdmissionWaiting != 1 || stats.CommandPending != 1 || stats.RunnerSlotsReserved != 1 || stats.AdmissionOldestWaitSeconds < 3500 || stats.CommandOldestWaitSeconds < 0 {
		t.Fatalf("snapshot=%+v err=%v", stats, err)
	}
	backlog, err := s.BacklogSnapshot(ctx)
	if err != nil || backlog.LegacyInboxPending != 1 || backlog.AdmissionWaiting != 1 || backlog.AdmissionOldestWaitSeconds < 3500 || backlog.CommandPending != 1 || backlog.RunnerSlotsReserved != 1 {
		t.Fatalf("backlog=%+v err=%v", backlog, err)
	}
	if err := s.Delivered(ctx, "observability-command"); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ClaimWebhookInbox(ctx, 10)
	if err != nil || len(ids) != 1 {
		t.Fatalf("claimed=%v err=%v", ids, err)
	}
	if err := s.CompleteWebhookInbox(ctx, []int64{ids[0].ID}); err != nil {
		t.Fatal(err)
	}
	stats, err = s.OperationsSnapshot(ctx)
	if err != nil || stats.InboxPending != 0 || stats.InboxProcessedLastHour != 1 {
		t.Fatalf("completed snapshot=%+v err=%v", stats, err)
	}
}
