package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell/docker"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forager"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"github.com/yyydddkkk/SwarmJet/internal/mcp"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// TestRound2RealCell runs the same loop as TestRound2Loop with a real Docker
// Cell, a real omp/ACP process, and the real bundle/mirror Publisher. The
// Publisher's remote is a local bare repository and PullRequests is faked, so no
// GitHub credential or network is needed.
//
//	SWARMJET_ROUND2_E2E=1 TEST_DATABASE_URL=... \
//	  go test ./cmd/swarmjet/ -run TestRound2RealCell -v -timeout 45m
//
// Optional environment:
//
//	SWARMJET_ROUND2_E2E_IMAGE    agent image (default swarmjet-agent:local)
//	SWARMJET_ROUND2_E2E_MODEL    provider/model (default deepseek/deepseek-chat)
//	SWARMJET_ROUND2_E2E_DOCKER   docker binary (default docker)
//	SWARMJET_ROUND2_E2E_NETWORK  Cell network (default host, for the MCP URL)
func TestRound2RealCell(t *testing.T) {
	if os.Getenv("SWARMJET_ROUND2_E2E") != "1" {
		t.Skip("set SWARMJET_ROUND2_E2E=1 to run the real Docker/omp test")
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	dockerBinary := round2EnvOr("SWARMJET_ROUND2_E2E_DOCKER", "docker")
	image := round2EnvOr("SWARMJET_ROUND2_E2E_IMAGE", "swarmjet-agent:local")
	model := round2EnvOr("SWARMJET_ROUND2_E2E_MODEL", "deepseek/deepseek-chat")
	network := round2EnvOr("SWARMJET_ROUND2_E2E_NETWORK", "host")
	if _, err := exec.LookPath(dockerBinary); err != nil {
		t.Skipf("%s not found: %v", dockerBinary, err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not found: %v", err)
	}
	if out, err := exec.Command(dockerBinary, "image", "inspect", image).CombinedOutput(); err != nil {
		t.Skipf("image %s not available: %v\n%s", image, err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	t.Cleanup(cancel)

	bare := initRound2BareRepo(t)
	s := openRound2Store(t)
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mcpServer := httptest.NewServer(mcp.NewHandler(s, mcp.Options{Logger: logger}))
	t.Cleanup(mcpServer.Close)

	runtime := docker.NewRuntime(dataDir)
	runtime.DockerBinary = dockerBinary
	runtime.Config.Image = image
	runtime.Config.DockerNetwork = network
	runtime.Git = &docker.Git{Remote: func(string) string { return bare }}
	runtime.Logger = logger

	pulls := &round2Pulls{pr: 501}
	publisher := &docker.Publisher{
		DataDir:        dataDir,
		Git:            &docker.Git{Remote: func(string) string { return bare }},
		RemoteURL:      func(string) string { return bare },
		Pulls:          pulls,
		Repository:     func(context.Context, string) (int64, error) { return 1, nil },
		CommandTimeout: 10 * time.Minute,
	}

	hive, err := s.CreateHive(ctx, domain.Hive{
		Name:       "Round 2 real",
		Repository: "acme/round2-real",
		Config: domain.HiveConfig{
			CLI:                "omp",
			TurnTimeoutMinutes: 15,
			IdleParkMinutes:    60,
			HibernateHours:     24,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agentID := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	const firstTask = "Use the bash tool to run exactly this command: cd /workspace && printf 'BUMBLEBEE\\n' > round2-e2e.txt && git add -A && git -c user.name=SwarmJet -c user.email=forager@swarmjet.invalid commit -m round2-first. Then reply with exactly DONE. Do not push and do not open a pull request; the platform publishes."
	task, err := s.CreateForager(ctx, domain.Agent{
		ID:      agentID,
		HiveID:  hive.ID,
		Role:    domain.RoleForager,
		State:   domain.AgentCreating,
		CLI:     "omp",
		Task:    firstTask,
		Branch:  "swarm/e2e/" + agentID,
		ChainID: "chain-" + agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = runtime.Destroy(cleanup, agentID)
	})

	supervisor, err := forager.New(forager.Options{
		Store:     s,
		Runtime:   runtime,
		Publisher: publisher,
		Model:     model,
		MCPURL:    mcpServer.URL,
		Logger:    logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.MessageSink = supervisor.Notify
	supervisorCtx, stopSupervisor := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = supervisor.Run(supervisorCtx)
	}()
	t.Cleanup(func() {
		stopSupervisor()
		<-done
	})

	localPlain, localToken, err := s.CreateToken(ctx, "round2-real-local", domain.TokenDefault)
	if err != nil {
		t.Fatal(err)
	}
	_ = localPlain
	taskMessage, err := s.SendMessage(ctx, domain.Message{
		HiveID:  hive.ID,
		From:    domain.LocalAddress(localToken.ID),
		To:      domain.ForagerAddress(agentID),
		Kind:    domain.MessageTask,
		ChainID: task.ChainID,
		Body:    firstTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverMessage(ctx, taskMessage.ID); err != nil {
		t.Fatal(err)
	}

	// First turn: create and commit a file, then publish it to the bare remote.
	first := waitRealAgentState(t, s, agentID, 25*time.Minute, domain.AgentParked)
	if first.PRNumber != pulls.pr {
		t.Fatalf("PR number = %d, want %d", first.PRNumber, pulls.pr)
	}
	firstPR := pulls.first()
	if firstPR.source != task.Branch {
		t.Fatalf("PR source = %q, want %q", firstPR.source, task.Branch)
	}
	if firstPR.target != "main" {
		t.Fatalf("PR target = %q, want main", firstPR.target)
	}
	if content := bareFile(t, bare, task.Branch, "round2-e2e.txt"); !strings.Contains(content, "BUMBLEBEE") {
		t.Fatalf("published file = %q, want BUMBLEBEE", content)
	}
	t.Logf("first turn: branch=%s pr=%d source=%s target=%s", task.Branch, first.PRNumber, firstPR.source, firstPR.target)

	// Second message: append a line and commit it; the supervisor resumes the
	// saved omp session and publishes again.
	secondTask := "Use the bash tool to run exactly this command: cd /workspace && printf 'SECOND-TURN\\n' >> round2-e2e.txt && git add -A && git -c user.name=SwarmJet -c user.email=forager@swarmjet.invalid commit -m round2-second. Then reply with exactly DONE. Do not push and do not open a pull request."
	secondMessage, err := s.SendMessage(ctx, domain.Message{
		HiveID:  hive.ID,
		From:    domain.LocalAddress(localToken.ID),
		To:      domain.ForagerAddress(agentID),
		Kind:    domain.MessageTask,
		ChainID: task.ChainID,
		Body:    secondTask,
		Hop:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverMessage(ctx, secondMessage.ID); err != nil {
		t.Fatal(err)
	}
	waitRound2Condition(t, "second publish", 25*time.Minute, func() bool { return pulls.count() >= 2 })
	second := waitRealAgentState(t, s, agentID, 25*time.Minute, domain.AgentParked)
	if second.SessionRef == "" {
		t.Fatal("supervisor did not persist the CLI session ref")
	}
	if content := bareFile(t, bare, task.Branch, "round2-e2e.txt"); !strings.Contains(content, "SECOND-TURN") {
		t.Fatalf("second published file = %q, want SECOND-TURN", content)
	}
	t.Logf("second turn: session=%s publishes=%d", second.SessionRef, pulls.count())

	// Cancel: the Forager becomes terminal, the Cell is destroyed and the Agent
	// token is revoked.
	cancelMessage, err := s.SendMessage(ctx, domain.Message{
		HiveID:  hive.ID,
		From:    domain.LocalAddress(localToken.ID),
		To:      domain.ForagerAddress(agentID),
		Kind:    domain.MessageControl,
		ChainID: task.ChainID,
		Body:    "cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverMessage(ctx, cancelMessage.ID); err != nil {
		t.Fatal(err)
	}
	final := waitRealAgentState(t, s, agentID, 2*time.Minute, domain.AgentCancelled)
	if final.State != domain.AgentCancelled {
		t.Fatalf("state = %s, want cancelled", final.State)
	}
	// The terminal state transition happens before Runtime.Destroy and token
	// revocation, so wait for the cleanup itself instead of sampling once.
	var liveTokens = -1
	waitRound2Condition(t, "Cell cleanup", 2*time.Minute, func() bool {
		if _, err := os.Stat(docker.WorkspaceDir(dataDir, agentID)); !os.IsNotExist(err) {
			return false
		}
		if err := exec.Command(dockerBinary, "inspect", "swarmjet-cell-"+agentID).Run(); err == nil {
			return false
		}
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_tokens WHERE agent_id=$1 AND revoked_at IS NULL`, agentID).Scan(&liveTokens); err != nil {
			return false
		}
		return liveTokens == 0
	})
	t.Logf("cancel: state=%s container removed workspace removed live_tokens=%d", final.State, liveTokens)
}

type round2PRCall struct {
	repoID int64
	source string
	target string
	title  string
	body   string
}

type round2Pulls struct {
	mu    sync.Mutex
	calls []round2PRCall
	pr    int64
}

func (p *round2Pulls) CreateOrUpdatePR(_ context.Context, id int64, source, target, title, body string) (forge.MergeRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, round2PRCall{repoID: id, source: source, target: target, title: title, body: body})
	return forge.MergeRequest{IID: p.pr, SourceBranch: source, TargetBranch: target}, nil
}

func (p *round2Pulls) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *round2Pulls) first() round2PRCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return round2PRCall{}
	}
	return p.calls[0]
}

func round2EnvOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func initRound2BareRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	runRound2Git(t, root, "init", "--bare", "--initial-branch=main", bare)
	runRound2Git(t, root, "clone", bare, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runRound2Git(t, seed, "add", "-A")
	runRound2Git(t, seed, "commit", "-m", "initial")
	runRound2Git(t, seed, "push", "origin", "HEAD:refs/heads/main")
	runRound2Git(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return bare
}

func runRound2Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=SwarmJet Test",
		"GIT_AUTHOR_EMAIL=test@swarmjet.invalid",
		"GIT_COMMITTER_NAME=SwarmJet Test",
		"GIT_COMMITTER_EMAIL=test@swarmjet.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func bareFile(t *testing.T, bare, branch, name string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "show", "refs/heads/"+branch+":"+name).CombinedOutput()
	if err != nil {
		t.Fatalf("read %s from %s: %v\n%s", name, branch, err, out)
	}
	return string(out)
}

func waitRealAgentState(t *testing.T, s *store.Store, id string, timeout time.Duration, want domain.AgentState) domain.Agent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		agent, err := s.Agent(context.Background(), id)
		if err == nil {
			switch agent.State {
			case want:
				return agent
			case domain.AgentFailed, domain.AgentCancelled:
				t.Fatalf("agent %s entered %s before %s", id, agent.State, want)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	agent, _ := s.Agent(context.Background(), id)
	t.Fatalf("agent %s did not reach %s in %s; last state=%s", id, want, timeout, agent.State)
	return domain.Agent{}
}

func waitRound2Condition(t *testing.T, what string, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
