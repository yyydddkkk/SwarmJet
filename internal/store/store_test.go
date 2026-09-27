package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "store_test_" + hex.EncodeToString(random)
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg := admin.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Pool: pool}
	t.Cleanup(func() {
		s.Close()
		_, err := admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
func testProject() domain.Project {
	return domain.Project{ID: "project", Name: "Project", Enabled: true, MetaURL: "https://git.example/group/meta.git", MetaRef: "main", GitLabURL: "https://git.example", CredentialRef: "GITLAB_TOKEN", MergeCredentialRef: "GITLAB_MERGE_TOKEN", Repositories: []domain.Repository{{ID: "service", ProjectID: 1, URL: "https://git.example/group/service.git", Path: "service", TargetBranch: "main"}}, Development: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Review: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Integration: domain.IntegrationConfig{ComposeFile: "compose.yml", StartupTimeoutSeconds: 30, Checks: []domain.Check{{Name: "test", Service: "test", Command: []string{"true"}, Required: true}}}, MaxReworks: 3}
}
func testTask(id string, p domain.Project, iid int64) domain.Task {
	return domain.Task{ID: id, Project: p, Issue: domain.Issue{ProjectID: 1, IID: iid, State: "opened", Labels: []string{"autonomous"}}, Stage: "queued", Executions: map[string]domain.Execution{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}
func TestProjectOptimisticVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil || p.Version != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	stale := p
	p.Name = "Changed"
	saved, err := s.SaveProject(ctx, p)
	if err != nil || saved.Version != 2 {
		t.Fatalf("%+v %v", saved, err)
	}
	stale.Name = "Overwrite"
	if _, err = s.SaveProject(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update:", err)
	}
	got, err := s.Project(ctx, p.ID)
	if err != nil || got.Name != "Changed" || got.Version != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestTaskOutboxAtomicityAndActiveIssue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	first := testTask("first", p, 1)
	if err = s.CreateTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].TaskID != "first" || pending[0].Command.Kind != "start" {
		t.Fatalf("%+v %v", pending, err)
	}
	duplicate := testTask("duplicate", p, 1)
	if err = s.CreateTask(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Task(ctx, duplicate.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("duplicate inserted", err)
	}
	// Force outbox insertion failure after the task row insert; the row must roll back.
	if err = s.Enqueue(ctx, first.ID, domain.Command{ID: "start-rollback", Kind: "note", Text: "collision"}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateTask(ctx, testTask("rollback", p, 2)); err == nil {
		t.Fatal("expected outbox conflict")
	}
	if _, err = s.Task(ctx, "rollback"); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial task committed", err)
	}
	first.Stage = "completed"
	if err = s.SaveTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateTask(ctx, testTask("next", p, 1)); err != nil {
		t.Fatal("terminal issue did not release uniqueness", err)
	}
}
func TestCommandDeduplicationAndDelivery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateTask(ctx, testTask("task", p, 1)); err != nil {
		t.Fatal(err)
	}
	c := domain.Command{ID: "command", Kind: "note", Text: "keep exact text"}
	for i := 0; i < 2; i++ {
		if err = s.Enqueue(ctx, "task", c); err != nil {
			t.Fatal(err)
		}
	}
	changed := c
	changed.Text = "different"
	if err = s.Enqueue(ctx, "task", changed); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting id accepted", err)
	}
	pending, err := s.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].Command.Kind != "start" {
		t.Fatalf("%+v %v", pending, err)
	}
	if err = s.Delivered(ctx, pending[0].Command.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].Command.ID != c.ID {
		t.Fatalf("%+v %v", pending, err)
	}
}

func TestTaskAutoMergeCommandIsSerializedWithMergeStage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	task := testTask("toggle-task", p, 44)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	off := false
	command := domain.Command{ID: "turn-off", Kind: "set_auto_merge", AutoMerge: &off}
	if err := s.EnqueueAutoMerge(ctx, task.ID, command); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestAutoMergeRequest(ctx, task.ID)
	if err != nil || latest == nil || *latest {
		t.Fatalf("queued off switch not visible to merge gate: %v %v", latest, err)
	}
	if err := s.EnqueueAutoMerge(ctx, task.ID, command); err != nil {
		t.Fatal("same command must be idempotent", err)
	}
	task.Stage = "merging"
	if err := s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := s.EnqueueAutoMerge(ctx, task.ID, domain.Command{ID: "turn-on", Kind: "set_auto_merge", AutoMerge: &on}); !errors.Is(err, ErrConflict) {
		t.Fatalf("merge stage accepted a late switch: %v", err)
	}
}
func TestLeaseContentionRenewalAndRelease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ok, err := s.Lease(ctx, "target", "worker-a", time.Hour)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	ok, err = s.Lease(ctx, "target", "worker-b", time.Hour)
	if err != nil || ok {
		t.Fatal("contended lease", ok, err)
	}
	ok, err = s.Lease(ctx, "target", "worker-a", 2*time.Hour)
	if err != nil || !ok {
		t.Fatal("renew", ok, err)
	}
	if err = s.ReleaseLeases(ctx, "worker-a"); err != nil {
		t.Fatal(err)
	}
	ok, err = s.Lease(ctx, "target", "worker-b", time.Hour)
	if err != nil || !ok {
		t.Fatal("release", ok, err)
	}
	_, err = s.Pool.Exec(ctx, "UPDATE leases SET expires_at=now()-interval '1 second' WHERE name='target'")
	if err != nil {
		t.Fatal(err)
	}
	ok, err = s.Lease(ctx, "target", "worker-c", time.Hour)
	if err != nil || !ok {
		t.Fatal("expired lease", ok, err)
	}
}
func TestSessionsAndEventCursor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if s.Session(ctx, "missing") {
		t.Fatal("unknown session valid")
	}
	if err := s.AddSession(ctx, "hash-only"); err != nil {
		t.Fatal(err)
	}
	if !s.Session(ctx, "hash-only") {
		t.Fatal("session missing")
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE sessions SET expires_at=now()-interval '1 second' WHERE token_hash='hash-only'"); err != nil {
		t.Fatal(err)
	}
	if s.Session(ctx, "hash-only") {
		t.Fatal("expired session accepted")
	}
	s.DeleteSession(ctx, "hash-only")
	if s.Session(ctx, "hash-only") {
		t.Fatal("deleted session accepted")
	}
	for _, message := range []string{"one", "two"} {
		if err := s.Event(ctx, "task", "progress", message); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.Events(ctx, "task", 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("%+v %v", events, err)
	}
	later, err := s.Events(ctx, "task", events[0].ID)
	if err != nil || len(later) != 1 || later[0].Message != "two" {
		t.Fatalf("%+v %v", later, err)
	}
}

func TestTaskKeepsFrozenProjectSnapshot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	task := testTask("frozen", p, 1)
	if err = s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	p.Development.Model = "provider/new-model"
	p.Repositories[0].TargetBranch = "release"
	if _, err = s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Version != 1 || got.Project.Development.Model != "provider/model" || got.Project.Repositories[0].TargetBranch != "main" {
		t.Fatalf("active task config drifted: %+v", got.Project)
	}
}

func TestActiveIssueUniquenessCanonicalGitLabURL(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateTask(ctx, testTask("first", p, 1)); err != nil {
		t.Fatal(err)
	}
	alias := testProject()
	alias.ID = "alias"
	alias.GitLabURL += "/api/v4/"
	// Disabled old projects may coexist, but cannot create a second active run.
	alias.Enabled = false
	alias, err = s.SaveProject(ctx, alias)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateTask(ctx, testTask("duplicate-alias", alias, 1)); !errors.Is(err, ErrConflict) {
		t.Fatalf("same GitLab issue accepted through equivalent API URL: %v", err)
	}
}

func TestEnabledRepositoryRoutingHasOneOwner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := testProject()
	if _, e := s.SaveProject(ctx, p); e != nil {
		t.Fatal(e)
	}
	p.ID = "other"
	p.GitLabURL += "/api/v4/"
	if _, e := s.SaveProject(ctx, p); !errors.Is(e, ErrConflict) {
		t.Fatalf("ambiguous repository routing accepted: %v", e)
	}
}

func TestCancelledSourceRemainsSeenButAllowsExplicitNewRun(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, e := s.SaveProject(ctx, testProject())
	if e != nil {
		t.Fatal(e)
	}
	task := testTask("cancel-first", p, 77)
	if e = s.CreateTask(ctx, task); e != nil {
		t.Fatal(e)
	}
	task.Stage = "cancelled"
	if e = s.SaveTask(ctx, task); e != nil {
		t.Fatal(e)
	}
	seen, e := s.HasSource(ctx, p.GitLabURL+"/api/v4/", task.Issue)
	if e != nil || !seen {
		t.Fatalf("cancelled source lost: %v %v", seen, e)
	}
	if e = s.CreateTask(ctx, testTask("manual-second", p, 77)); e != nil {
		t.Fatalf("explicit user rerun rejected: %v", e)
	}
}

func TestDraftSaveAndActivationValidationAreAtomic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	draft := domain.Project{ID: "draft", Name: "Draft", MetaURL: "https://git.example/group/meta.git"}
	saved, err := s.SaveProject(ctx, draft)
	if err != nil || saved.Version != 1 || saved.Enabled {
		t.Fatalf("draft save: %+v %v", saved, err)
	}
	invalid := saved
	invalid.Enabled = true
	if _, err = s.SaveProject(ctx, invalid); err == nil {
		t.Fatal("incomplete activation accepted")
	}
	unchanged, err := s.Project(ctx, draft.ID)
	if err != nil || unchanged.Version != 1 || unchanged.Enabled || len(unchanged.Repositories) != 0 {
		t.Fatalf("failed activation mutated draft: %+v %v", unchanged, err)
	}
	unsafe := saved
	unsafe.Repositories = []domain.Repository{{Path: "../escape"}}
	if _, err = s.SaveProject(ctx, unsafe); err == nil {
		t.Fatal("unsafe disabled draft accepted")
	}
	complete := testProject()
	complete.ID = draft.ID
	complete.Version = saved.Version
	active, err := s.SaveProject(ctx, complete)
	if err != nil || !active.Enabled || active.Version != 2 {
		t.Fatalf("complete activation: %+v %v", active, err)
	}
	if err = domain.ValidateProject(active); err != nil {
		t.Fatal("activated project fails execution contract", err)
	}
}
