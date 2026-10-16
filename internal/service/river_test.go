package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/store"
	"go.temporal.io/sdk/client"
)

type countingTemporal struct {
	client.Client
	starts  atomic.Int32
	signals atomic.Int32
}

func (t *countingTemporal) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	if options.ID != "command-test" {
		return nil, errors.New("unexpected workflow ID")
	}
	t.starts.Add(1)
	return nil, nil
}

func (t *countingTemporal) SignalWorkflow(_ context.Context, workflowID, _, _ string, _ interface{}) error {
	if workflowID != "command-test" {
		return errors.New("unexpected signal workflow ID")
	}
	t.signals.Add(1)
	return nil
}

func TestWebhookDuplicateIsQueuedOnceAndConflictSkipsJob(t *testing.T) {
	s := scanTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var scans atomic.Int32
	queue, err := jobs.Open(s.Pool, jobs.Handlers{
		Webhook: func(context.Context, jobs.Webhook) error {
			scans.Add(1)
			return nil
		},
		Command: func(context.Context, jobs.Command) error { return nil },
	}, jobs.Config{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	s.Jobs = queue
	go func() { _ = queue.Start(ctx) }()
	payload := []byte(`{"project":{"id":7}}`)
	for i := 0; i < 2; i++ {
		if err := s.AcceptWebhook(ctx, "gitlab", "same-id", "Issue Hook", payload); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 8*time.Second, func() bool { return scans.Load() == 1 })
	if scans.Load() != 1 {
		t.Fatalf("scans=%d", scans.Load())
	}
	var jobsQueued int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='webhook'`).Scan(&jobsQueued); err != nil || jobsQueued != 1 {
		t.Fatalf("webhook jobs=%d err=%v", jobsQueued, err)
	}
	if err := s.AcceptWebhook(ctx, "gitlab", "same-id", "Issue Hook", []byte(`{"project":{"id":8}}`)); !errors.Is(err, store.ErrWebhookConflict) {
		t.Fatalf("conflict=%v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if scans.Load() != 1 {
		t.Fatalf("conflict scanned again: %d", scans.Load())
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='webhook'`).Scan(&jobsQueued); err != nil || jobsQueued != 1 {
		t.Fatalf("webhook jobs after conflict=%d err=%v", jobsQueued, err)
	}
}

func TestCommandJobReachesTemporalOnce(t *testing.T) {
	s := scanTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	project := domain.Project{ID: "command-project"}
	projectBody := []byte(`{"id":"command-project"}`)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES($1,1,$2)`, project.ID, projectBody); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "command-test", Project: project, Stage: "queued"}
	taskBody := []byte(`{"id":"command-test","stage":"queued"}`)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO tasks(id,project_id,source,stage,body) VALUES($1,$2,$3,$4,$5)`, task.ID, project.ID, "command-test", task.Stage, taskBody); err != nil {
		t.Fatal(err)
	}
	temporal := &countingTemporal{}
	svc := &Service{Store: s, Temporal: temporal}
	queue, err := jobs.Open(s.Pool, jobs.Handlers{
		Webhook: func(context.Context, jobs.Webhook) error { return nil },
		Command: svc.HandleCommandJob,
	}, jobs.Config{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	s.Jobs = queue
	start := domain.Command{ID: "start-command-test", Kind: "start"}
	if err := s.Enqueue(ctx, task.ID, start); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, task.ID, start); err != nil {
		t.Fatal(err)
	}
	go func() { _ = queue.Start(ctx) }()
	waitFor(t, 8*time.Second, func() bool {
		var delivered bool
		_ = s.Pool.QueryRow(context.Background(), `SELECT delivered FROM commands WHERE id=$1`, start.ID).Scan(&delivered)
		return delivered && temporal.starts.Load() == 1
	})
	if temporal.starts.Load() != 1 {
		t.Fatalf("starts=%d", temporal.starts.Load())
	}
	var startJobs int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='command' AND args->>'id'=$1`, start.ID).Scan(&startJobs); err != nil || startJobs != 1 {
		t.Fatalf("start jobs=%d err=%v", startJobs, err)
	}
	note := domain.Command{ID: "note-command-test", Kind: "note", Text: "once"}
	if err := s.Enqueue(ctx, task.ID, note); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, task.ID, note); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, func() bool {
		var delivered bool
		_ = s.Pool.QueryRow(context.Background(), `SELECT delivered FROM commands WHERE id=$1`, note.ID).Scan(&delivered)
		return delivered && temporal.signals.Load() == 1
	})
	if temporal.signals.Load() != 1 {
		t.Fatalf("signals=%d", temporal.signals.Load())
	}
}

func waitFor(t *testing.T, limit time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for job")
}

func TestCoalescedRepositoryScanRunsOnce(t *testing.T) {
	coalescer := &scanCoalescer{window: 40 * time.Millisecond, groups: map[string]*scanGroup{}}
	var scans atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	fn := func(context.Context) error {
		scans.Add(1)
		if once.CompareAndSwap(false, true) {
			close(started)
		}
		<-release
		return nil
	}
	ctx := context.Background()
	done := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { done <- coalescer.Do(ctx, "gitlab:7", fn) }()
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not start")
	}
	close(release)
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if scans.Load() != 1 {
		t.Fatalf("scans=%d", scans.Load())
	}
}
