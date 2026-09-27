package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"swarmjet/internal/domain"
)

func writeFixture(t *testing.T) (*Store, KubernetesWriteIntent) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	task := testTask("write-task", p, 91)
	task.RunnerImage = "example.invalid/runner@sha256:" + strings.Repeat("a", 64)
	if _, err = s.CreateKubernetesTask(ctx, task, "pool", domain.RunnerResources{CPUMilli: 100, MemoryMi: 64, StorageMi: 64, TimeoutSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if count, err := s.AdmitKubernetesTasks(ctx, "pool", 1, 1); err != nil || count != 1 {
		t.Fatalf("admit %d %v", count, err)
	}
	if _, err = s.BeginKubernetesExecution(ctx, task.ID, "dev", "write-execution"); err != nil {
		t.Fatal(err)
	}
	if version, err := s.AcquireKubernetesExecution(ctx, "write-execution", "attempt-1"); err != nil || version != 1 {
		t.Fatalf("acquire %d %v", version, err)
	}
	return s, KubernetesWriteIntent{ID: "push-write-task", TaskID: task.ID, ExecutionID: "write-execution", AuthorityVersion: 1, Kind: "push", RepositoryID: "service", ProjectID: 1, SourceBranch: "swarmjet-task", TargetBranch: "main", HeadSHA: strings.Repeat("b", 40)}
}

func TestKubernetesWriteIntentRebindAndFence(t *testing.T) {
	s, w := writeFixture(t)
	ctx := context.Background()
	first, err := s.RecordKubernetesWriteIntent(ctx, w, "attempt-1")
	if err != nil || first.State != "pending" {
		t.Fatalf("first intent %+v %v", first, err)
	}
	changed := w
	changed.HeadSHA = strings.Repeat("c", 40)
	if _, err = s.RecordKubernetesWriteIntent(ctx, changed, "attempt-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutable intent accepted: %v", err)
	}
	if err = s.RevokeKubernetesExecution(ctx, w.ExecutionID, 1, "attempt-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordKubernetesWriteIntent(ctx, w, "attempt-1"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("revoked writer accepted: %v", err)
	}
	if err = s.ConfirmKubernetesPodStopped(ctx, w.ExecutionID, 1, "attempt-1", true); err != nil {
		t.Fatal(err)
	}
	version, err := s.TakeOverKubernetesExecution(ctx, w.ExecutionID, 1, "attempt-2")
	if err != nil || version != 2 {
		t.Fatalf("takeover %d %v", version, err)
	}
	w.AuthorityVersion = version
	got, err := s.RecordKubernetesWriteIntent(ctx, w, "attempt-2")
	if err != nil || got.AuthorityVersion != 2 || got.State != "pending" {
		t.Fatalf("rebound intent %+v %v", got, err)
	}
	if err = s.ConfirmKubernetesWriteIntent(ctx, w.ID, w.ExecutionID, "attempt-1", 1, 0); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("old holder confirmed: %v", err)
	}
	if err = s.ConfirmKubernetesWriteIntent(ctx, w.ID, w.ExecutionID, "attempt-2", 2, 0); err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.KubernetesWriteIntent(ctx, w.ID)
	if err != nil || confirmed.State != "confirmed" {
		t.Fatalf("confirmation %+v %v", confirmed, err)
	}
}

func TestKubernetesWriteLockSerializesRevocation(t *testing.T) {
	s, w := writeFixture(t)
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- s.WithKubernetesAuthority(ctx, w.TaskID, w.ExecutionID, 1, "attempt-1", func(context.Context) error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not take lock")
	}
	revoked := make(chan error, 1)
	go func() { revoked <- s.RevokeKubernetesExecution(ctx, w.ExecutionID, 1, "attempt-1") }()
	select {
	case err := <-revoked:
		t.Fatalf("revoked during external write: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if err := s.WithKubernetesAuthority(ctx, w.TaskID, w.ExecutionID, 1, "attempt-1", func(context.Context) error { return nil }); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("revoked writer accepted: %v", err)
	}
}

func TestFrozenAdmissionRejectsWriteIntentAndConfirmation(t *testing.T) {
	s, w := writeFixture(t)
	ctx := context.Background()
	if _, err := s.RecordKubernetesWriteIntent(ctx, w, "attempt-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FreezeKubernetesAdmission(ctx, w.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKubernetesWriteIntent(ctx, w, "attempt-1"); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("intent after freeze: %v", err)
	}
	if err := s.ConfirmKubernetesWriteIntent(ctx, w.ID, w.ExecutionID, "attempt-1", 1, 0); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("confirmation after freeze: %v", err)
	}
}
