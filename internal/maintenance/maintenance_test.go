package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"swarmjet/internal/agent"
	"swarmjet/internal/artifacts"
	"swarmjet/internal/domain"
	"swarmjet/internal/store"
	"swarmjet/internal/workspace"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Manager, domain.Task) {
	t.Helper()
	dir := t.TempDir()
	m := &Manager{DataDir: dir, Agents: agent.New(dir), Workspace: workspace.New(dir, ""), Event: func(context.Context, string, string, string) error { return nil }}
	return m, domain.Task{ID: "expired", Stage: "completed"}
}

type retentionKubernetes struct{ deleted bool }

func (*retentionKubernetes) Cancel(context.Context, string, int64) error { return nil }
func (r *retentionKubernetes) DeletePVC(context.Context, string) error   { r.deleted = true; return nil }

func TestKubernetesCleanupRequiresSavedReportBeforePVCDeletion(t *testing.T) {
	ctx := context.Background()
	objects := artifacts.FileStore{Root: t.TempDir()}
	runner := &retentionKubernetes{}
	m := &Manager{Store: &store.Store{}, Kubernetes: runner, KubernetesArtifacts: objects}
	task := domain.Task{ID: "task", Stage: "completed", AdmissionBackend: "kubernetes", Executions: map[string]domain.Execution{}}
	if err := m.cleanup(ctx, task); err == nil || runner.deleted {
		t.Fatalf("missing report permitted cleanup: %v", err)
	}
	ref, err := objects.Put(ctx, artifacts.Request{TaskID: "task", ExecutionID: "task-dev", Kind: artifacts.Report, Name: "report"}, strings.NewReader("saved"))
	if err != nil {
		t.Fatal(err)
	}
	task.Executions["dev"] = domain.Execution{ID: "task-dev", Artifacts: []domain.ArtifactRef{{TaskID: ref.TaskID, ExecutionID: ref.ExecutionID, Kind: string(ref.Kind), Name: ref.Name, SHA256: ref.SHA256, Size: ref.Size}}}
	if err := m.cleanup(ctx, task); err != nil || !runner.deleted {
		t.Fatalf("saved report cleanup: %v", err)
	}
	if _, err := objects.Read(ctx, ref); err == nil {
		t.Fatal("expired report retained")
	}
}
func put(t *testing.T, p string, b []byte) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func record(t *testing.T, m *Manager, task, id string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"taskId": task, "execution": domain.Execution{ID: id, ContainerID: "sj-agent-" + id, Status: "succeeded"}})
	put(t, filepath.Join(m.DataDir, "agent-executions", id+".json"), b)
}
func TestEligibilityProtectsActivePausedAndRecentTasks(t *testing.T) {
	m, _ := fixture(t)
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour)
	for _, stage := range []string{"blocked", "waiting_approval", "developing", "partial_merge", "verifying", "failed", "queued"} {
		if m.eligible(domain.Task{Stage: stage}, old, now) {
			t.Fatalf("active stage selected %s", stage)
		}
	}
	for _, stage := range []string{"completed", "cancelled"} {
		if m.eligible(domain.Task{Stage: stage, Paused: true}, old, now) {
			t.Fatal("paused task selected")
		}
		if m.eligible(domain.Task{Stage: stage}, now.Add(-time.Hour), now) {
			t.Fatal("recent task selected")
		}
		if !m.eligible(domain.Task{Stage: stage}, old, now) {
			t.Fatal("expired terminal task omitted")
		}
	}
	if m.eligible(domain.Task{Stage: "completed"}, now.Add(-8*24*time.Hour), now) {
		t.Fatal("report retention shorter than 30 days")
	}
	if !m.eligible(domain.Task{Stage: "cancelled"}, now.Add(-8*24*time.Hour), now) {
		t.Fatal("cancelled default not 7 days")
	}
}
func TestCleanupOnlyOwnTaskDirectoriesAndStoppedContainers(t *testing.T) {
	m, task := fixture(t)
	for _, d := range []string{"task-sources", "opencode", "agent-profiles", "runs"} {
		put(t, filepath.Join(m.DataDir, d, task.ID, "own"), []byte("own"))
		put(t, filepath.Join(m.DataDir, d, "sibling", "keep"), []byte("keep"))
	}
	for _, d := range []string{"projects", "opencode-config"} {
		put(t, filepath.Join(m.DataDir, d, "keep"), []byte("keep"))
	}
	record(t, m, task.ID, "old-execution")
	record(t, m, "sibling", "sibling-execution")
	var removed []string
	m.Docker = func(ctx context.Context, args []string) ([]byte, error) {
		if args[1] == "inspect" {
			return []byte(`[{"Name":"/sj-agent-old-execution","State":{"Status":"exited","Running":false},"Config":{"Labels":{"swarmjet.execution":"old-execution"}}}]`), nil
		}
		removed = append(removed, strings.Join(args, " "))
		return nil, nil
	}
	if e := m.cleanup(context.Background(), task); e != nil {
		t.Fatal(e)
	}
	if len(removed) != 1 || removed[0] != "container rm sj-agent-old-execution" {
		t.Fatalf("unexpected removal %v", removed)
	}
	for _, d := range []string{"task-sources", "opencode", "agent-profiles", "runs"} {
		if _, e := os.Stat(filepath.Join(m.DataDir, d, task.ID)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("task dir remains %s", d)
		}
		if _, e := os.Stat(filepath.Join(m.DataDir, d, "sibling", "keep")); e != nil {
			t.Fatal(e)
		}
	}
	for _, d := range []string{"projects", "opencode-config"} {
		if _, e := os.Stat(filepath.Join(m.DataDir, d, "keep")); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := os.Stat(filepath.Join(m.DataDir, "agent-executions", "sibling-execution.json")); e != nil {
		t.Fatal(e)
	}
}
func TestActiveOrUnownedContainerPreservesEverything(t *testing.T) {
	for _, c := range []struct{ status, label string }{{"running", "execution"}, {"exited", "someone-else"}, {"created", "execution"}} {
		t.Run(c.status+c.label, func(t *testing.T) {
			m, task := fixture(t)
			record(t, m, task.ID, "execution")
			file := filepath.Join(m.DataDir, "runs", task.ID, "keep")
			put(t, file, []byte("data"))
			m.Docker = func(ctx context.Context, args []string) ([]byte, error) {
				if args[1] != "inspect" {
					t.Fatal("attempted container deletion")
				}
				b, _ := json.Marshal([]map[string]any{{"Name": "/sj-agent-execution", "State": map[string]any{"Status": c.status, "Running": c.status == "running"}, "Config": map[string]any{"Labels": map[string]string{"swarmjet.execution": c.label}}}})
				return b, nil
			}
			if e := m.cleanup(context.Background(), task); e == nil {
				t.Fatal("unsafe container accepted")
			}
			if _, e := os.Stat(file); e != nil {
				t.Fatal("artifacts deleted")
			}
			if _, e := os.Stat(m.marker(task.ID)); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("failed cleanup marked complete")
			}
		})
	}
}
func TestTraversalAndSymlinkPathsRejected(t *testing.T) {
	m, task := fixture(t)
	for _, id := range []string{"../sibling", "a/b", "..", ""} {
		task.ID = id
		if e := m.cleanup(context.Background(), task); e == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	outside := t.TempDir()
	put(t, filepath.Join(outside, "keep"), []byte("keep"))
	os.MkdirAll(filepath.Join(m.DataDir, "task-sources"), 0700)
	os.Symlink(outside, filepath.Join(m.DataDir, "task-sources", "expired"))
	task.ID = "expired"
	if e := m.cleanup(context.Background(), task); e == nil {
		t.Fatal("symlink task path accepted")
	}
	if _, e := os.Stat(filepath.Join(outside, "keep")); e != nil {
		t.Fatal("external directory changed")
	}
}
func TestFailedContainerRemovalPreventsArtifactDeletion(t *testing.T) {
	m, task := fixture(t)
	record(t, m, task.ID, "execution")
	file := filepath.Join(m.DataDir, "runs", task.ID, "keep")
	put(t, file, []byte("keep"))
	m.Docker = func(ctx context.Context, args []string) ([]byte, error) {
		if args[1] == "inspect" {
			return []byte(`[{"Name":"/sj-agent-execution","State":{"Status":"exited"},"Config":{"Labels":{"swarmjet.execution":"execution"}}}]`), nil
		}
		return []byte("daemon failed"), errors.New("failed")
	}
	if e := m.cleanup(context.Background(), task); e == nil {
		t.Fatal("removal error hidden")
	}
	if _, e := os.Stat(file); e != nil {
		t.Fatal("artifacts removed after Docker failure")
	}
	if _, e := os.Stat(m.marker(task.ID)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("failed cleanup marked complete")
	}
}
func TestCancelledDirtyWorkspaceIsPreserved(t *testing.T) {
	m, task := fixture(t)
	ctx := context.Background()
	remote := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", remote}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, b)
		}
	}
	git("init", "-b", "main")
	put(t, filepath.Join(remote, ".meta.yaml"), []byte("projects: {}\n"))
	put(t, filepath.Join(remote, "README"), []byte("base"))
	git("add", ".")
	git("commit", "-m", "initial")
	p := domain.Project{ID: "project", MetaURL: remote, MetaRef: "main"}
	ws, e := m.Workspace.Prepare(ctx, task.ID, p)
	if e != nil {
		t.Fatal(e)
	}
	task.Stage = "cancelled"
	task.Workspace = ws
	file := filepath.Join(ws.Root, "README")
	put(t, file, []byte("uncommitted work"))
	if e = m.cleanup(ctx, task); e == nil {
		t.Fatal("dirty cancelled workspace retired")
	}
	b, e := os.ReadFile(file)
	if e != nil || string(b) != "uncommitted work" {
		t.Fatal("cancelled task changes lost")
	}
}
func TestSweepDatabaseSelectionMarkersAndFailures(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, e := store.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("maintenance_test_%d", time.Now().UnixNano())
	if _, e = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	config := admin.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	s := &store.Store{Pool: pool}
	t.Cleanup(func() {
		pool.Close()
		admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES('project',1,'{}')`); e != nil {
		t.Fatal(e)
	}
	m, _ := fixture(t)
	m.Store = s
	m.Event = nil
	now := time.Now()
	tasks := []struct {
		id, stage string
		paused    bool
		age       time.Duration
	}{{"complete", "completed", false, 40 * 24 * time.Hour}, {"cancelled", "cancelled", false, 8 * 24 * time.Hour}, {"blocked", "blocked", false, 90 * 24 * time.Hour}, {"approval", "waiting_approval", false, 90 * 24 * time.Hour}, {"paused", "completed", true, 90 * 24 * time.Hour}, {"recent", "completed", false, time.Hour}, {"cleanup-fails", "completed", false, 40 * 24 * time.Hour}}
	for _, x := range tasks {
		task := domain.Task{ID: x.id, Stage: x.stage, Paused: x.paused}
		b, _ := json.Marshal(task)
		terminal := x.stage == "completed" || x.stage == "cancelled"
		if _, e = pool.Exec(ctx, "INSERT INTO tasks(id,project_id,source,stage,terminal,body,updated_at) VALUES($1,'project',$1,$2,$3,$4,$5)", x.id, x.stage, terminal, b, now.Add(-x.age)); e != nil {
			t.Fatal(e)
		}
		put(t, filepath.Join(m.DataDir, "runs", x.id, "keep"), []byte("test"))
	}
	record(t, m, "cleanup-fails", "failed-execution")
	m.Docker = func(ctx context.Context, args []string) ([]byte, error) {
		if args[1] == "inspect" {
			return []byte(`[{"Name":"/sj-agent-failed-execution","State":{"Status":"exited"},"Config":{"Labels":{"swarmjet.execution":"failed-execution"}}}]`), nil
		}
		return nil, errors.New("injected Docker cleanup failure")
	}
	if e = m.Sweep(ctx); e == nil {
		t.Fatal("failed cleanup not reported")
	}
	for _, id := range []string{"complete", "cancelled"} {
		if _, e = os.Stat(m.marker(id)); e != nil {
			t.Fatal("missing successful retention marker", id, e)
		}
	}
	for _, id := range []string{"blocked", "approval", "paused", "recent", "cleanup-fails"} {
		if _, e = os.Stat(filepath.Join(m.DataDir, "runs", id, "keep")); e != nil {
			t.Fatal("protected artifacts removed", id)
		}
		if _, e = os.Stat(m.marker(id)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("protected/failed task marked complete", id)
		}
	}
	_ = m.Sweep(ctx)
	var count int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE task_id='complete' AND kind='artifact_retention'").Scan(&count); e != nil || count != 1 {
		t.Fatalf("retention event repeated: %d %v", count, e)
	}
}
func TestCancelledTaskCannotReleaseSiblingWorkspace(t *testing.T) {
	m, task := fixture(t)
	task.Stage = "cancelled"
	task.Workspace = domain.Workspace{TaskID: "sibling", Root: filepath.Join(m.DataDir, "task-sources", "sibling", "meta", ".worktrees", "sibling")}
	put(t, filepath.Join(task.Workspace.Root, "keep"), []byte("keep"))
	if e := m.cleanup(context.Background(), task); e == nil {
		t.Fatal("sibling workspace accepted")
	}
	if _, e := os.Stat(filepath.Join(task.Workspace.Root, "keep")); e != nil {
		t.Fatal("sibling changed")
	}
}
