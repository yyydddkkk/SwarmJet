package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"swarmjet/internal/artifacts"
	"swarmjet/internal/domain"
	"swarmjet/internal/k8srunner"
	"swarmjet/internal/observability"
	"swarmjet/internal/store"
)

func TestKubernetesActivityFailuresAreObserved(t *testing.T) {
	metrics := observability.New()
	a := &KubernetesActivities{Metrics: metrics}
	if _, err := a.RunKubernetesTask(context.Background(), domain.Task{ID: "private-task-id"}); err == nil {
		t.Fatal("expected unconfigured run to fail")
	}
	if err := a.StopKubernetesTask(context.Background(), "private-task-id"); err == nil {
		t.Fatal("expected unconfigured stop to fail")
	}
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, phase := range []string{"run_kubernetes_task", "stop_kubernetes_task"} {
		if !strings.Contains(body, `swarmjet_activity_steps_total{phase="`+phase+`",result="error"} 1`) {
			t.Fatalf("missing %s failure metric", phase)
		}
	}
	if strings.Contains(body, "private-task-id") {
		t.Fatal("task ID leaked into metric labels")
	}
}

type fixtureRunner struct {
	created     k8srunner.Spec
	stopped     bool
	log         []byte
	createError error
	pvcError    error
}

func (f *fixtureRunner) EnsurePVC(context.Context, string, int64) (string, error) {
	return "sj-task", f.pvcError
}
func (f *fixtureRunner) Create(_ context.Context, s k8srunner.Spec) (k8srunner.Job, error) {
	f.created = s
	return k8srunner.Job{}, f.createError
}
func (f *fixtureRunner) Observe(context.Context, string, int64) (k8srunner.Observation, error) {
	return k8srunner.Observation{Exists: true, Complete: true, Pods: []k8srunner.PodObservation{{UID: "pod-uid", Phase: "Succeeded"}}}, nil
}
func (f *fixtureRunner) ReadPodLog(context.Context, string, int64) ([]byte, error) { return f.log, nil }
func (f *fixtureRunner) Cancel(context.Context, string, int64) error               { f.stopped = true; return nil }

func isolatedPipelineStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "pipeline_k8s_" + hex.EncodeToString(random[:])
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg := admin.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{Pool: pool}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func fixtureKubernetesTask(t *testing.T, s *store.Store) domain.Task {
	t.Helper()
	ctx := context.Background()
	p := domain.Project{ID: "project", Name: "isolated"}
	projectJSON, _ := json.Marshal(p)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES('project',1,$1)`, projectJSON); err != nil {
		t.Fatal(err)
	}
	image := "example.invalid/runner@sha256:" + strings.Repeat("a", 64)
	task := domain.Task{ID: "task", Project: p, Issue: domain.Issue{ProjectID: 1, IID: 1}, Stage: "queued", RunnerImage: image, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	var err error
	task, err = s.CreateKubernetesTask(ctx, task, "isolated", domain.RunnerResources{CPUMilli: 100, MemoryMi: 64, StorageMi: 64, TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.AdmitKubernetesTasks(ctx, "isolated", 1, 1); err != nil || n != 1 {
		t.Fatalf("admission=%d %v", n, err)
	}
	return task
}

func TestKubernetesActivityPersistsIndependentArtifactAndFence(t *testing.T) {
	s := isolatedPipelineStore(t)
	task := fixtureKubernetesTask(t, s)
	log := []byte(fmt.Sprintf(`{"schemaVersion":1,"executionId":"task-dev","generation":1,"taskId":"task","status":"succeeded","inputSha256":"%s"}`+"\n", strings.Repeat("b", 64)))
	runner := &fixtureRunner{log: log}
	objects := artifacts.FileStore{Root: t.TempDir()}
	a := KubernetesActivities{Store: s, Runner: runner, Artifacts: objects}
	got, err := a.RunKubernetesTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if runner.created.Image != task.RunnerImage || runner.created.SecretName != "" || got.Executions["dev"].Status != "succeeded" || len(got.Executions["dev"].Artifacts) != 3 {
		t.Fatalf("job=%+v task=%+v", runner.created, got)
	}
	for _, r := range got.Executions["dev"].Artifacts {
		if _, err := objects.Read(context.Background(), artifacts.Ref{TaskID: r.TaskID, ExecutionID: r.ExecutionID, Kind: artifacts.Kind(r.Kind), Name: r.Name, SHA256: r.SHA256, Size: r.Size}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.RunKubernetesTask(context.Background(), task); err != nil {
		t.Fatal("lost activity response repeated execution:", err)
	}
	if err := s.RevokeKubernetesExecution(context.Background(), "task-dev", 1, k8srunner.JobName("task-dev", 1)); !errors.Is(err, store.ErrExecutionAuthority) {
		t.Fatalf("completed execution accepted revoke: %v", err)
	}
}

func TestKubernetesActivityBlocksInvalidResultAndStops(t *testing.T) {
	s := isolatedPipelineStore(t)
	task := fixtureKubernetesTask(t, s)
	runner := &fixtureRunner{log: []byte(`{"status":"succeeded"}`)}
	a := KubernetesActivities{Store: s, Runner: runner, Artifacts: artifacts.FileStore{Root: t.TempDir()}}
	if _, err := a.RunKubernetesTask(context.Background(), task); err == nil {
		t.Fatal("invalid result accepted")
	}
	if err := a.StopKubernetesTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if !runner.stopped {
		t.Fatal("Runner was not stopped")
	}
	ex, err := s.KubernetesExecution(context.Background(), "task-dev")
	if err != nil || ex.State != "stopped" || ex.CheckpointValid {
		t.Fatalf("unsafe recovery state: %+v %v", ex, err)
	}
	if _, err := a.RunKubernetesTask(context.Background(), task); err == nil {
		t.Fatal("stopped attempt restarted without a valid checkpoint")
	}
}

func TestKubernetesStopCancelsPendingWithoutPod(t *testing.T) {
	s := isolatedPipelineStore(t)
	task := fixtureKubernetesTask(t, s)
	runner := &fixtureRunner{pvcError: errors.New("injected PVC failure")}
	a := KubernetesActivities{Store: s, Runner: runner, Artifacts: artifacts.FileStore{Root: t.TempDir()}}
	if _, err := a.RunKubernetesTask(context.Background(), task); err == nil {
		t.Fatal("PVC failure lost")
	}
	if err := a.StopKubernetesTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	ex, err := s.KubernetesExecution(context.Background(), "task-dev")
	if err != nil || ex.State != "cancelled" || runner.stopped {
		t.Fatalf("pending cancel: %+v stopped=%v err=%v", ex, runner.stopped, err)
	}
}

func TestKubernetesAmbiguousCreateCannotConfirmStop(t *testing.T) {
	s := isolatedPipelineStore(t)
	task := fixtureKubernetesTask(t, s)
	runner := &fixtureRunner{createError: errors.New("injected lost create response")}
	a := KubernetesActivities{Store: s, Runner: runner, Artifacts: artifacts.FileStore{Root: t.TempDir()}}
	if _, err := a.RunKubernetesTask(context.Background(), task); err == nil {
		t.Fatal("create error lost")
	}
	if err := a.StopKubernetesTask(context.Background(), task.ID); err == nil {
		t.Fatal("ambiguous create was confirmed stopped")
	}
	ex, err := s.KubernetesExecution(context.Background(), "task-dev")
	if err != nil || ex.State != "revoked" || ex.JobCreation != "creating" || ex.CheckpointValid {
		t.Fatalf("unsafe ambiguous state: %+v %v", ex, err)
	}
}

func TestKubernetesWorkflowCompletesOfflineFixture(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(10 * time.Second)
	var saved []domain.Task
	env.RegisterActivityWithOptions(func(_ context.Context, task domain.Task) error { saved = append(saved, task); return nil }, activity.RegisterOptions{Name: "SaveTask"})
	env.RegisterActivityWithOptions(func(_ context.Context, task domain.Task) (domain.Task, error) {
		task.Executions = map[string]domain.Execution{"dev": {ID: "task-dev", Status: "succeeded"}}
		return task, nil
	}, activity.RegisterOptions{Name: "RunKubernetesTask"})
	env.RegisterActivityWithOptions(func(context.Context, string) error { return nil }, activity.RegisterOptions{Name: "StopKubernetesTask"})
	env.ExecuteWorkflow(KubernetesTask, domain.Task{ID: "task", AdmissionBackend: "kubernetes", Stage: "queued"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil || len(saved) != 2 || saved[1].Stage != "completed" {
		t.Fatalf("workflow result: saved=%+v err=%v", saved, env.GetWorkflowError())
	}
}

func TestKubernetesWorkflowCancelStopsRunnerBeforeTerminalState(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(10 * time.Second)
	stopped := false
	var final domain.Task
	env.RegisterActivityWithOptions(func(_ context.Context, task domain.Task) error { final = task; return nil }, activity.RegisterOptions{Name: "SaveTask"})
	env.RegisterActivityWithOptions(func(ctx context.Context, task domain.Task) (domain.Task, error) { <-ctx.Done(); return task, ctx.Err() }, activity.RegisterOptions{Name: "RunKubernetesTask"})
	env.RegisterActivityWithOptions(func(context.Context, string) error { stopped = true; return nil }, activity.RegisterOptions{Name: "StopKubernetesTask"})
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(SignalName, domain.Command{ID: "cancel", Kind: "cancel"}) }, time.Millisecond)
	env.ExecuteWorkflow(KubernetesTask, domain.Task{ID: "task", AdmissionBackend: "kubernetes", Stage: "queued"})
	if err := env.GetWorkflowError(); err != nil || !stopped || final.Stage != "cancelled" {
		t.Fatalf("cancelled before stop or failed: stopped=%v stage=%s err=%v", stopped, final.Stage, err)
	}
}

func TestKubernetesWorkflowIgnoresDuplicateCommandID(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(10 * time.Second)
	var final domain.Task
	env.RegisterActivityWithOptions(func(_ context.Context, task domain.Task) error { final = task; return nil }, activity.RegisterOptions{Name: "SaveTask"})
	env.RegisterActivityWithOptions(func(ctx context.Context, task domain.Task) (domain.Task, error) { <-ctx.Done(); return task, ctx.Err() }, activity.RegisterOptions{Name: "RunKubernetesTask"})
	env.RegisterActivityWithOptions(func(context.Context, string) error { return nil }, activity.RegisterOptions{Name: "StopKubernetesTask"})
	env.RegisterDelayedCallback(func() {
		note := domain.Command{ID: "note-1", Kind: "note", Text: "once"}
		env.SignalWorkflow(SignalName, note)
		env.SignalWorkflow(SignalName, note)
		env.SignalWorkflow(SignalName, domain.Command{ID: "cancel-1", Kind: "cancel"})
	}, time.Millisecond)
	env.ExecuteWorkflow(KubernetesTask, domain.Task{ID: "task", AdmissionBackend: "kubernetes", Stage: "queued"})
	if err := env.GetWorkflowError(); err != nil || final.Stage != "cancelled" || len(final.Notes) != 1 || final.Notes[0] != "once" {
		t.Fatalf("duplicate note applied: notes=%v stage=%s err=%v", final.Notes, final.Stage, err)
	}
}
