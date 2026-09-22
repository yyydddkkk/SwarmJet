package initflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/managedmeta"
	"swarmjet/internal/store"
)

type fakeRepo struct {
	projects map[string]domain.Project
	inits    map[string]domain.Initialization
	leases   map[string]string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{projects: map[string]domain.Project{}, inits: map[string]domain.Initialization{}, leases: map[string]string{}}
}
func (f *fakeRepo) Project(_ context.Context, id string) (domain.Project, error) {
	p, ok := f.projects[id]
	if !ok {
		return domain.Project{}, store.ErrNotFound
	}
	return p, nil
}
func (f *fakeRepo) SaveProject(_ context.Context, p domain.Project) (domain.Project, error) {
	p.Version++
	f.projects[p.ID] = p
	return p, nil
}
func (f *fakeRepo) Initialization(_ context.Context, id string) (domain.Initialization, error) {
	init, ok := f.inits[id]
	if !ok {
		return domain.Initialization{}, store.ErrNotFound
	}
	return init, nil
}
func (f *fakeRepo) Initializations(_ context.Context) ([]domain.Initialization, error) {
	out := []domain.Initialization{}
	for _, init := range f.inits {
		out = append(out, init)
	}
	return out, nil
}
func (f *fakeRepo) SaveInitialization(_ context.Context, init domain.Initialization) error {
	if init.Version < 1 {
		return store.ErrConflict
	}
	if existing, ok := f.inits[init.ID]; ok && existing.Version != init.Version-1 {
		return store.ErrConflict
	}
	f.inits[init.ID] = init
	return nil
}
func (f *fakeRepo) Lease(_ context.Context, name, owner string, _ time.Duration) (bool, error) {
	if held, ok := f.leases[name]; ok && held != owner {
		return false, nil
	}
	f.leases[name] = owner
	return true, nil
}
func (f *fakeRepo) ReleaseLeases(_ context.Context, owner string) error {
	for name, held := range f.leases {
		if held == owner {
			delete(f.leases, name)
		}
	}
	return nil
}

type fakeAgents struct {
	onStart  func(domain.AgentRequest) (domain.Execution, error)
	requests []domain.AgentRequest
}

func (f *fakeAgents) Start(_ context.Context, req domain.AgentRequest) (domain.Execution, error) {
	f.requests = append(f.requests, req)
	if f.onStart != nil {
		return f.onStart(req)
	}
	return domain.Execution{ID: req.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "ready", Summary: "done"}}, nil
}
func (f *fakeAgents) Inspect(_ context.Context, id string) (domain.Execution, error) {
	return domain.Execution{ID: id, Status: "succeeded"}, nil
}
func (f *fakeAgents) Cancel(context.Context, string) error { return nil }
func (f *fakeAgents) CancelTask(context.Context, string) error {
	return nil
}
func (f *fakeAgents) TaskExecutions(context.Context, string) ([]domain.Execution, error) {
	return nil, nil
}

type fixture struct {
	m       *Manager
	repo    *fakeRepo
	agents  *fakeAgents
	clones  []string
	runs    []string
	project domain.Project
	clients func(domain.Project) (*gitlab.Client, error)
}

func managedProject() domain.Project {
	auto := false
	return domain.Project{
		ID: "p1", Name: "Demo", MetaMode: domain.MetaModeManaged, MetaRef: "main",
		GitLabURL: "http://gitlab.test", ConnectionID: "c1",
		Repositories: []domain.Repository{{ID: "svc", ProjectID: 101, URL: "https://gitlab.test/team/svc.git", Path: "services/svc", TargetBranch: "dev"}},
		Development:  domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 60},
		AutoMerge:    &auto,
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/repository/branches/dev") {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "dev", "commit": map[string]string{"id": "sha-dev"}})
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(server.Close)
	f := &fixture{repo: newFakeRepo(), agents: &fakeAgents{}, project: managedProject()}
	f.repo.projects[f.project.ID] = f.project
	f.clients = func(domain.Project) (*gitlab.Client, error) { return gitlab.New(server.URL, "token"), nil }
	f.m = &Manager{
		Store:   f.repo,
		DataDir: dir,
		Agents:  f.agents,
		Managed: managedmeta.New(dir),
		Clients: f.clients,
		Cloner: func(_ context.Context, _ domain.Project, repo domain.Repository, dest, sha string) error {
			f.clones = append(f.clones, repo.ID+":"+sha)
			if err := os.MkdirAll(dest, 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dest, "README.md"), []byte("source"), 0600)
		},
		Runner: func(_ context.Context, _ string, argv []string) (int, string, error) {
			f.runs = append(f.runs, strings.Join(argv, " "))
			return 0, "ok", nil
		},
	}
	return f
}

func readyAgent(withVerification bool) func(domain.AgentRequest) (domain.Execution, error) {
	return func(req domain.AgentRequest) (domain.Execution, error) {
		if err := os.MkdirAll(req.GeneratedDir, 0700); err != nil {
			return domain.Execution{}, err
		}
		if err := os.WriteFile(filepath.Join(req.GeneratedDir, "PROJECT.md"), []byte("# Demo"+string(byte(10))+"A demo project."), 0600); err != nil {
			return domain.Execution{}, err
		}
		if withVerification {
			spec := `{"commands":[{"name":"unit","path":"services/svc","argv":["go","test","./..."]}]}`
			if err := os.WriteFile(filepath.Join(req.GeneratedDir, "verification.json"), []byte(spec), 0600); err != nil {
				return domain.Execution{}, err
			}
			integration := `{"composeFile":"integration/compose.yaml","checks":[{"name":"integration","service":"runner","command":["go","test","./..."],"required":true}]}`
			if err := os.WriteFile(filepath.Join(req.GeneratedDir, "integration.json"), []byte(integration), 0600); err != nil {
				return domain.Execution{}, err
			}
			if err := os.MkdirAll(filepath.Join(req.GeneratedDir, "integration"), 0700); err != nil {
				return domain.Execution{}, err
			}
			compose := "services:" + string(byte(10)) + "  runner:" + string(byte(10)) + "    image: alpine"
			if err := os.WriteFile(filepath.Join(req.GeneratedDir, "integration", "compose.yaml"), []byte(compose), 0600); err != nil {
				return domain.Execution{}, err
			}
		}
		return domain.Execution{ID: req.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "ready", Summary: "done"}}, nil
	}
}

func (f *fixture) drive(t *testing.T, max int) domain.Initialization {
	t.Helper()
	ctx := context.Background()
	init, err := f.m.Ensure(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < max; i++ {
		if domain.InitTerminal(init.Status) || init.Status == domain.InitNeedsInput || init.Status == domain.InitBlocked {
			return init
		}
		init, err = f.m.Advance(ctx, init)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("initialization did not settle: %+v", init)
	return init
}

func TestEnsureIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.m.Ensure(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.m.Ensure(ctx, f.project.ID)
	if err != nil || first.ID != second.ID || first.ID != "init-p1" {
		t.Fatalf("ensure not idempotent: %+v %+v %v", first, second, err)
	}
	if len(f.repo.inits) != 1 {
		t.Fatalf("duplicate initialization records: %d", len(f.repo.inits))
	}
}

func TestFullManagedFlow(t *testing.T) {
	f := newFixture(t)
	f.agents.onStart = readyAgent(true)
	init := f.drive(t, 12)
	if init.Status != domain.InitReady || !init.ReadyForTasks || !init.RepositoriesConnected || !init.AnalysisComplete {
		t.Fatalf("funnel incomplete: %+v", init)
	}
	if init.RepositorySHAs["svc"] != "sha-dev" {
		t.Fatalf("analysis revision not pinned: %+v", init.RepositorySHAs)
	}
	if len(f.clones) == 0 || f.clones[0] != "svc:sha-dev" {
		t.Fatalf("clone did not use the pinned revision: %v", f.clones)
	}
	if len(f.runs) != 0 {
		t.Fatalf("static initialization executed commands: %v", f.runs)
	}
	body, err := f.m.Managed.ReadFile(context.Background(), "p1", "main", "PROJECT.md")
	if err != nil || !strings.Contains(string(body), "Demo") {
		t.Fatalf("generated docs not published: %v %s", err, body)
	}
	for _, req := range f.agents.requests {
		if req.Role != "init" {
			t.Fatalf("unexpected role %s", req.Role)
		}
		if !strings.Contains(req.Workspace, filepath.Join("initializations", "p1", "analysis")) {
			t.Fatalf("analysis workspace not isolated: %s", req.Workspace)
		}
		if !strings.Contains(req.GeneratedDir, filepath.Join("initializations", "p1", "generated")) {
			t.Fatalf("generated dir not isolated: %s", req.GeneratedDir)
		}
		if req.Workspace == req.GeneratedDir {
			t.Fatal("analysis and generated directories must differ")
		}
	}
	copied := filepath.Join(f.m.DataDir, "initializations", "p1", "verify", "services", "svc", "README.md")
	if _, err := os.Stat(copied); !os.IsNotExist(err) {
		t.Fatalf("static initialization created a verification workspace: %v", err)
	}
	if saved := f.repo.projects["p1"]; saved.Integration.ComposeFile != "" {
		t.Fatalf("static initialization changed task acceptance settings: %+v", saved.Integration)
	}
}

func TestNeedsInputThenAnswer(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.agents.onStart = func(req domain.AgentRequest) (domain.Execution, error) {
		calls++
		if calls == 1 {
			return domain.Execution{ID: req.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "needs_input", Summary: "need info", Questions: []string{"请提供本地联调方式。"}}}, nil
		}
		return readyAgent(true)(req)
	}
	init := f.drive(t, 12)
	if init.Status != domain.InitNeedsInput || len(init.Questions) != 1 {
		t.Fatalf("missing question: %+v", init)
	}
	init, err := f.m.Answer(context.Background(), f.project.ID, map[string]string{"q1": "使用 docker compose"})
	if err != nil || init.Status != domain.InitPending || !init.Questions[0].Answered {
		t.Fatalf("answer not recorded: %+v %v", init, err)
	}
	if init.Attempts < 2 || init.ExecutionID != "" {
		t.Fatalf("answer must force a new attempt: %+v", init)
	}
	init = f.drive(t, 12)
	if init.Status != domain.InitReady || !init.ReadyForTasks {
		t.Fatalf("flow did not continue after answers: %+v", init)
	}
}

func TestBlockedCancelAndRetry(t *testing.T) {
	f := newFixture(t)
	f.agents.onStart = func(req domain.AgentRequest) (domain.Execution, error) {
		return domain.Execution{ID: req.ExecutionID, Status: "failed", FailureReason: "agent crashed"}, nil
	}
	blocked := f.drive(t, 12)
	if blocked.Status != domain.InitBlocked || blocked.Error == "" {
		t.Fatalf("failure not surfaced: %+v", blocked)
	}
	if err := f.m.Retry(context.Background(), f.project.ID); err != nil {
		t.Fatal(err)
	}
	retried, _ := f.m.Get(context.Background(), f.project.ID)
	if retried.Status != domain.InitPending || retried.Attempts < 2 || retried.ExecutionID != "" {
		t.Fatalf("retry did not prepare a fresh execution: %+v", retried)
	}
	if err := f.m.Cancel(context.Background(), f.project.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := f.m.Get(context.Background(), f.project.ID)
	if cancelled.Status != domain.InitCancelled {
		t.Fatalf("cancel not persisted: %+v", cancelled)
	}
	restarted, err := f.m.Ensure(context.Background(), f.project.ID)
	if err != nil || restarted.Status != domain.InitPending {
		t.Fatalf("cancelled init did not restart cleanly: %+v %v", restarted, err)
	}
}

func TestStaticInitializationDoesNotRequireVerification(t *testing.T) {
	t.Run("failing command", func(t *testing.T) {
		f := newFixture(t)
		f.agents.onStart = readyAgent(true)
		f.m.Runner = func(context.Context, string, []string) (int, string, error) {
			t.Fatal("business command executed during static initialization")
			return 1, "boom", nil
		}
		init := f.drive(t, 12)
		if init.Status != domain.InitReady || !init.AnalysisComplete || len(init.Verification) != 0 {
			t.Fatalf("static analysis did not complete without running commands: %+v", init)
		}
	})
	t.Run("no command identified", func(t *testing.T) {
		f := newFixture(t)
		f.agents.onStart = readyAgent(false)
		init := f.drive(t, 12)
		if init.Status != domain.InitReady || !init.AnalysisComplete || len(init.Verification) != 0 {
			t.Fatalf("missing verification incorrectly blocked static initialization: %+v", init)
		}
	})
}

func TestEnsureRejectsImportedProject(t *testing.T) {
	f := newFixture(t)
	imported := f.project
	imported.MetaMode = domain.MetaModeImported
	imported.MetaURL = "https://gitlab.test/team/meta.git"
	f.repo.projects["p2"] = imported
	if _, err := f.m.Ensure(context.Background(), "p2"); err == nil {
		t.Fatal("imported project unexpectedly initialized as managed")
	}
}
