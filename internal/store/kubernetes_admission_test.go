package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"swarmjet/internal/domain"
)

func k8sResources() domain.RunnerResources {
	return domain.RunnerResources{CPUMilli: 500, MemoryMi: 512, StorageMi: 1024, TimeoutSeconds: 300}
}

const testRunnerDigest = "registry.example/swarmjet/runner@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func k8sTask(id string, p domain.Project, iid int64) domain.Task {
	task := testTask(id, p, iid)
	task.RunnerImage = testRunnerDigest
	return task
}

func TestKubernetesAdmissionIsAtomicAndCancelsWaitingTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("k8s-%d", i)
		if _, err = s.CreateKubernetesTask(ctx, k8sTask(id, p, int64(i)), "default", k8sResources()); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	counts := make(chan int, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); n, e := s.AdmitKubernetesTasks(ctx, "default", 1, 10); counts <- n; errs <- e }()
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if total != 1 {
		t.Fatalf("admitted %d tasks with capacity 1", total)
	}
	var admitted, starts int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kubernetes_admissions WHERE state='admitted'`).Scan(&admitted); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM commands WHERE id LIKE 'start-k8s-%'`).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	if admitted != 1 || starts != 1 {
		t.Fatalf("admitted=%d starts=%d", admitted, starts)
	}
	cancelled, err := s.CancelKubernetesTask(ctx, "k8s-2")
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	_, err = s.AdmitKubernetesTasks(ctx, "default", 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Task(ctx, "k8s-2")
	if err != nil || task.Stage != "cancelled" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM commands WHERE id='start-k8s-2'`).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	if starts != 0 {
		t.Fatalf("cancelled task started %d times", starts)
	}
}

func TestKubernetesAuthorityRequiresStoppedPodAndValidCheckpoint(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("authority", p, 1), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 1, 1); err != nil {
		t.Fatal(err)
	}
	ex, err := s.BeginKubernetesExecution(ctx, "authority", "development", "exec-1")
	if err != nil || ex.State != "pending" {
		t.Fatalf("execution=%+v err=%v", ex, err)
	}
	v1, err := s.AcquireKubernetesExecution(ctx, "exec-1", "job-1")
	if err != nil || v1 != 1 {
		t.Fatalf("version=%d err=%v", v1, err)
	}
	projection, err := s.Task(ctx, "authority")
	if err != nil || projection.ExecutionID != "exec-1" || projection.AuthorityVersion != v1 || projection.ExecutionPool != "default" {
		t.Fatalf("task authority projection=%+v err=%v", projection, err)
	}
	if _, err = s.TakeOverKubernetesExecution(ctx, "exec-1", v1, "job-2"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("takeover before stop: %v", err)
	}
	if err = s.RevokeKubernetesExecution(ctx, "exec-1", v1, "job-1"); err != nil {
		t.Fatal(err)
	}
	valid, err := s.CheckKubernetesAuthority(ctx, "exec-1", v1, "job-1")
	if err != nil || valid {
		t.Fatalf("revoked authority valid=%v err=%v", valid, err)
	}
	if err = s.ConfirmKubernetesPodStopped(ctx, "exec-1", v1, "job-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TakeOverKubernetesExecution(ctx, "exec-1", v1, "job-2"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("takeover with invalid checkpoint: %v", err)
	}
	// A later checkpoint validation can update the stopped attempt.
	if err = s.ConfirmKubernetesPodStopped(ctx, "exec-1", v1, "job-1", true); err != nil {
		t.Fatal(err)
	}
	v2, err := s.TakeOverKubernetesExecution(ctx, "exec-1", v1, "job-2")
	if err != nil || v2 != v1+1 {
		t.Fatalf("version=%d err=%v", v2, err)
	}
	result := domain.Execution{ID: "exec-1", Status: "completed"}
	if err = s.SaveKubernetesExecutionResult(ctx, "exec-1", v1, "job-1", result); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("stale completion: %v", err)
	}
	if err = s.SaveKubernetesExecutionResult(ctx, "exec-1", v2, "job-2", result); err != nil {
		t.Fatal(err)
	}
	projection.Stage = "planning"
	if err = s.SaveTask(ctx, projection); err != nil {
		t.Fatal(err)
	}
	projection, err = s.Task(ctx, "authority")
	if err != nil || projection.AuthorityVersion != v2 || projection.Executions["development"].ID != "exec-1" {
		t.Fatalf("stale workflow save lost result or authority: %+v %v", projection, err)
	}
}

func TestKubernetesAuthorityLocksRemoteWriteAgainstRevocation(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("locked", p, 1), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, "locked", "development", "exec-locked"); err != nil {
		t.Fatal(err)
	}
	v, err := s.AcquireKubernetesExecution(ctx, "exec-locked", "job-locked")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- s.WithKubernetesAuthority(ctx, "locked", "exec-locked", v, "job-locked", func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	revokeDone := make(chan error, 1)
	go func() { revokeDone <- s.RevokeKubernetesExecution(ctx, "exec-locked", v, "job-locked") }()
	select {
	case err = <-revokeDone:
		t.Fatalf("revocation passed held writer lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err = <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err = <-revokeDone; err != nil {
		t.Fatal(err)
	}
	if err = s.WithKubernetesAuthority(ctx, "locked", "exec-locked", v, "job-locked", func(context.Context) error { t.Fatal("stale callback ran"); return nil }); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("stale authority: %v", err)
	}
}

func TestLegacyPendingAdmissionAndCancelKeepKubernetesQueueConsistent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	bad := k8sTask("bad-image", p, 1)
	bad.RunnerImage = "runner:latest"
	if _, err = s.CreateKubernetesTask(ctx, bad, "default", k8sResources()); err == nil {
		t.Fatal("mutable Runner image accepted")
	}
	if _, err = s.Task(ctx, bad.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid task persisted: %v", err)
	}
	task, err := s.CreateKubernetesTask(ctx, k8sTask("legacy-cancel", p, 2), "default", k8sResources())
	if err != nil {
		t.Fatal(err)
	}
	before := domain.Command{ID: "k8s-note-before", Kind: "note", Text: "before cancel"}
	if err = s.Enqueue(ctx, task.ID, before); err != nil {
		t.Fatal(err)
	}
	count, err := s.AdmitPendingTasks(ctx, 10)
	if err != nil || count != 0 {
		t.Fatalf("legacy admitted Kubernetes task: %d %v", count, err)
	}
	cancelled, err := s.CancelPendingTask(ctx, task.ID)
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	if err = s.Enqueue(ctx, task.ID, domain.Command{ID: "k8s-note-after", Kind: "note", Text: "after cancel"}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Task(ctx, task.ID)
	if err != nil || stored.Stage != "cancelled" || stored.AdmissionStatus != "cancelled" || len(stored.Notes) != 2 || stored.Notes[0] != "before cancel" || stored.Notes[1] != "after cancel" {
		t.Fatalf("task=%+v err=%v", stored, err)
	}
	var state string
	if err = s.Pool.QueryRow(ctx, `SELECT state FROM kubernetes_admissions WHERE task_id=$1`, task.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("admission=%q err=%v", state, err)
	}
	count, err = s.AdmitKubernetesTasks(ctx, "default", 1, 1)
	if err != nil || count != 0 {
		t.Fatalf("cancelled task admitted: %d %v", count, err)
	}
	var starts int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM commands WHERE id=$1`, "start-"+task.ID).Scan(&starts); err != nil || starts != 0 {
		t.Fatalf("start commands=%d err=%v", starts, err)
	}
	pending, err := s.Pending(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending commands=%+v err=%v", pending, err)
	}
}

func TestKubernetesPendingCancellationRacesFirstAcquire(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("pending-race", p, 1), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, "pending-race", "dev", "pending-race-exec"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type outcome struct {
		operation string
		err       error
	}
	results := make(chan outcome, 2)
	go func() {
		<-start
		_, e := s.AcquireKubernetesExecution(ctx, "pending-race-exec", "job-a")
		results <- outcome{"acquire", e}
	}()
	go func() {
		<-start
		e := s.CancelPendingKubernetesExecution(ctx, "pending-race-exec")
		results <- outcome{"cancel", e}
	}()
	close(start)
	first, second := <-results, <-results
	successes := 0
	for _, got := range []outcome{first, second} {
		if got.err == nil {
			successes++
		} else if !errors.Is(got.err, ErrExecutionAuthority) {
			t.Fatalf("%s error: %v", got.operation, got.err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one winner: %+v %+v", first, second)
	}
	ex, err := s.KubernetesExecution(ctx, "pending-race-exec")
	if err != nil {
		t.Fatal(err)
	}
	if ex.State == "cancelled" {
		if ex.AuthorityVersion != 0 || ex.JobCreation != "none" {
			t.Fatalf("cancelled execution: %+v", ex)
		}
		if err = s.MarkKubernetesJobCreating(ctx, ex.ExecutionID, 1, "job-a"); !errors.Is(err, ErrExecutionAuthority) {
			t.Fatalf("cancelled attempt created Job: %v", err)
		}
	} else if ex.State == "active" {
		if ex.AuthorityVersion != 1 || ex.JobCreation != "none" {
			t.Fatalf("acquired execution: %+v", ex)
		}
	} else {
		t.Fatalf("unexpected execution: %+v", ex)
	}
}

func TestKubernetesCreatingAttemptCannotBeStoppedOnEmptyObservation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("creation-race", p, 1), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, "creation-race", "dev", "creation-race-exec"); err != nil {
		t.Fatal(err)
	}
	v, err := s.AcquireKubernetesExecution(ctx, "creation-race-exec", "job-a")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkKubernetesJobCreating(ctx, "creation-race-exec", v, "job-a"); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeKubernetesExecution(ctx, "creation-race-exec", v, "job-a"); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmKubernetesPodStopped(ctx, "creation-race-exec", v, "job-a", true); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("ambiguous create incorrectly marked stopped: %v", err)
	}
	if _, err = s.TakeOverKubernetesExecution(ctx, "creation-race-exec", v, "job-b"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("ambiguous create allowed takeover: %v", err)
	}
	if err = s.MarkKubernetesJobCreated(ctx, "creation-race-exec", v, "job-a"); err != nil {
		t.Fatal(err)
	}
	ex, err := s.KubernetesExecution(ctx, "creation-race-exec")
	if err != nil || ex.State != "revoked" || ex.JobCreation != "created" {
		t.Fatalf("reconciled create=%+v err=%v", ex, err)
	}
	if err = s.ConfirmKubernetesPodStopped(ctx, "creation-race-exec", v, "job-a", true); err != nil {
		t.Fatal(err)
	}
	if next, err := s.TakeOverKubernetesExecution(ctx, "creation-race-exec", v, "job-b"); err != nil || next != v+1 {
		t.Fatalf("takeover version=%d err=%v", next, err)
	}
	ex, err = s.KubernetesExecution(ctx, "creation-race-exec")
	if err != nil || ex.JobCreation != "none" {
		t.Fatalf("new attempt inherited creation state: %+v %v", ex, err)
	}
}

func TestKubernetesJobCreationColumnMigratesExistingExecutions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `ALTER TABLE kubernetes_executions DROP COLUMN job_creation`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migration is not repeatable: %v", err)
	}
	var value string
	if err := s.Pool.QueryRow(ctx, `SELECT job_creation FROM kubernetes_executions LIMIT 1`).Scan(&value); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestKubernetesFreezeSerializesBeginAndFencesLaterAcquire(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("freeze-race", p, 1), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 1, 1); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type beginResult struct {
		ex  KubernetesExecution
		err error
	}
	begin := make(chan beginResult, 1)
	frozen := make(chan beginResult, 1)
	go func() {
		<-start
		ex, e := s.BeginKubernetesExecution(ctx, "freeze-race", "dev", "freeze-race-exec")
		begin <- beginResult{ex, e}
	}()
	go func() {
		<-start
		ex, e := s.FreezeKubernetesAdmission(ctx, "freeze-race")
		frozen <- beginResult{ex, e}
	}()
	close(start)
	b, f := <-begin, <-frozen
	if f.err != nil {
		t.Fatal(f.err)
	}
	if b.err == nil {
		if f.ex.ExecutionID != "freeze-race-exec" || f.ex.State != "pending" {
			t.Fatalf("freeze lost committed Begin: %+v", f.ex)
		}
	} else if !errors.Is(b.err, ErrExecutionAuthority) || f.ex.ExecutionID != "" {
		t.Fatalf("begin=%+v frozen=%+v", b, f)
	}
	if _, err = s.AcquireKubernetesExecution(ctx, "freeze-race-exec", "job-a"); !errors.Is(err, ErrExecutionAuthority) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("frozen admission allowed acquire: %v", err)
	}
	if err = s.MarkKubernetesJobCreating(ctx, "freeze-race-exec", 1, "job-a"); !errors.Is(err, ErrExecutionAuthority) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("frozen admission allowed Job creation: %v", err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, "freeze-race", "dev", "later-exec"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("frozen admission allowed Begin: %v", err)
	}
}

func TestKubernetesTerminalTaskFencesAcquireAndJobCreation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("terminal-acquire", p, 1), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, "terminal-acquire", "dev", "terminal-exec-a"); err != nil {
		t.Fatal(err)
	}
	task, err := s.Task(ctx, "terminal-acquire")
	if err != nil {
		t.Fatal(err)
	}
	task.Stage = "cancelled"
	if err = s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireKubernetesExecution(ctx, "terminal-exec-a", "job-a"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("terminal task acquired: %v", err)
	}
	if err = s.MarkKubernetesJobCreating(ctx, "terminal-exec-a", 1, "job-a"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("terminal task created Job: %v", err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("terminal-creating", p, 2), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, "terminal-creating", "dev", "terminal-exec-b"); err != nil {
		t.Fatal(err)
	}
	v, err := s.AcquireKubernetesExecution(ctx, "terminal-exec-b", "job-b")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.Task(ctx, "terminal-creating")
	if err != nil {
		t.Fatal(err)
	}
	task.Stage = "cancelled"
	if err = s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkKubernetesJobCreating(ctx, "terminal-exec-b", v, "job-b"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("terminal task created Job: %v", err)
	}
	valid, err := s.CheckKubernetesAuthority(ctx, "terminal-exec-b", v, "job-b")
	if err != nil || valid {
		t.Fatalf("terminal task authority=%v err=%v", valid, err)
	}
}

func TestKubernetesFreezeFencesResultsAndRemoteWritesBeforeRevocation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("freeze-effects", p, 1), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKubernetesTask(ctx, k8sTask("freeze-waiting", p, 2), "default", k8sResources()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitKubernetesTasks(ctx, "default", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, "freeze-effects", "dev", "freeze-effects-exec"); err != nil {
		t.Fatal(err)
	}
	v, err := s.AcquireKubernetesExecution(ctx, "freeze-effects-exec", "job-a")
	if err != nil {
		t.Fatal(err)
	}
	ex, err := s.FreezeKubernetesAdmission(ctx, "freeze-effects")
	if err != nil || ex.State != "active" {
		t.Fatalf("freeze=%+v err=%v", ex, err)
	}
	valid, err := s.CheckKubernetesAuthority(ctx, "freeze-effects-exec", v, "job-a")
	if err != nil || valid {
		t.Fatalf("frozen authority valid=%v err=%v", valid, err)
	}
	ran := false
	err = s.WithKubernetesAuthority(ctx, "freeze-effects", "freeze-effects-exec", v, "job-a", func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, ErrExecutionAuthority) || ran {
		t.Fatalf("remote callback ran=%v err=%v", ran, err)
	}
	err = s.SaveKubernetesExecutionResult(ctx, "freeze-effects-exec", v, "job-a", domain.Execution{ID: "freeze-effects-exec", Status: "succeeded"})
	if !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("frozen result accepted: %v", err)
	}
	if err = s.MarkKubernetesJobCreating(ctx, "freeze-effects-exec", v, "job-a"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("frozen Job create accepted: %v", err)
	}
	if count, admitErr := s.AdmitKubernetesTasks(ctx, "default", 1, 1); admitErr != nil || count != 0 {
		t.Fatalf("frozen active Runner released capacity: count=%d err=%v", count, admitErr)
	}
	if err = s.RevokeKubernetesExecution(ctx, "freeze-effects-exec", v, "job-a"); err != nil {
		t.Fatalf("frozen authority could not revoke: %v", err)
	}
	if count, admitErr := s.AdmitKubernetesTasks(ctx, "default", 1, 1); admitErr != nil || count != 0 {
		t.Fatalf("revoked but unconfirmed Runner released capacity: count=%d err=%v", count, admitErr)
	}
	if err = s.ConfirmKubernetesPodStopped(ctx, "freeze-effects-exec", v, "job-a", false); err != nil {
		t.Fatal(err)
	}
	if count, admitErr := s.AdmitKubernetesTasks(ctx, "default", 1, 1); admitErr != nil || count != 1 {
		t.Fatalf("stopped Runner did not release capacity: count=%d err=%v", count, admitErr)
	}
}
