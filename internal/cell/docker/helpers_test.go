package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/acp"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// fakeExecutor records docker commands and answers them through handler.
type fakeExecutor struct {
	mu      sync.Mutex
	calls   [][]string
	handler func(args []string, env []string) (Result, error)
}

func (f *fakeExecutor) Run(_ context.Context, args []string, env []string) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	handler := f.handler
	f.mu.Unlock()
	if handler != nil {
		return handler(args, env)
	}
	return Result{}, nil
}

func (f *fakeExecutor) snapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	for i, call := range f.calls {
		out[i] = append([]string(nil), call...)
	}
	return out
}

func (f *fakeExecutor) setHandler(h func(args []string, env []string) (Result, error)) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
}

func dockerHandler(running, exists bool) func(args []string, env []string) (Result, error) {
	return func(args []string, _ []string) (Result, error) {
		switch {
		case len(args) > 0 && args[0] == "inspect":
			if !exists {
				return Result{ExitCode: 1, Stderr: []byte("Error: No such object: cell")}, nil
			}
			if running {
				return Result{Stdout: []byte("true\n")}, nil
			}
			return Result{Stdout: []byte("false\n")}, nil
		case len(args) > 1 && args[0] == "volume" && args[1] == "inspect":
			return Result{ExitCode: 1, Stderr: []byte("Error response from daemon: No such volume")}, nil
		default:
			return Result{}, nil
		}
	}
}

// fakeACP is a scripted ACP client.
type fakeACP struct {
	mu         sync.Mutex
	initResult acp.InitializeResult
	initErr    error
	newResult  acp.NewSessionResult
	newErr     error
	newCalls   [][]acp.MCPServer
	loadErr    error
	loadCalls  []string
	loadReplay []acp.SessionNotification
	promptFn   func(ctx context.Context, text string, sink acp.UpdateSink) (acp.PromptResult, error)
	prompts    []string
	cancels    []string
	cancelErr  error
	closed     bool
	outcome    *acp.PermissionOutcome
}

func (f *fakeACP) Initialize(context.Context) (acp.InitializeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.initResult, f.initErr
}

func (f *fakeACP) NewSession(_ context.Context, _ string, servers []acp.MCPServer) (acp.NewSessionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newCalls = append(f.newCalls, append([]acp.MCPServer(nil), servers...))
	return f.newResult, f.newErr
}

func (f *fakeACP) LoadSession(_ context.Context, id, _ string, _ []acp.MCPServer, sink acp.UpdateSink) (acp.LoadSessionResult, error) {
	f.mu.Lock()
	f.loadCalls = append(f.loadCalls, id)
	loadErr := f.loadErr
	replay := append([]acp.SessionNotification(nil), f.loadReplay...)
	f.mu.Unlock()
	if loadErr != nil {
		return acp.LoadSessionResult{}, loadErr
	}
	for _, notification := range replay {
		if sink != nil {
			_ = sink(notification)
		}
	}
	return acp.LoadSessionResult{}, nil
}

func (f *fakeACP) Prompt(ctx context.Context, _ string, blocks []acp.ContentBlock, sink acp.UpdateSink) (acp.PromptResult, error) {
	text := ""
	if len(blocks) > 0 {
		text = blocks[0].Text
	}
	f.mu.Lock()
	f.prompts = append(f.prompts, text)
	fn := f.promptFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, text, sink)
	}
	return acp.PromptResult{StopReason: acp.StopEndTurn}, nil
}

func (f *fakeACP) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, id)
	return f.cancelErr
}

func (f *fakeACP) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeACP) promptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

func (f *fakeACP) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeACP) newSessionCalls() [][]acp.MCPServer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]acp.MCPServer(nil), f.newCalls...)
}

func (f *fakeACP) loadSessionCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.loadCalls...)
}

// fakeDialer records the connection parameters and returns scripted clients.
type fakeDialer struct {
	mu        sync.Mutex
	newClient func() ACPClient
	err       error
	dials     []dialRecord
	permit    acp.PermissionHandler
}

type dialRecord struct {
	container string
	workdir   string
	env       []string
	argv      []string
}

func (d *fakeDialer) Dial(_ context.Context, container, workdir string, env, argv []string, permission acp.PermissionHandler) (ACPClient, error) {
	d.mu.Lock()
	d.dials = append(d.dials, dialRecord{
		container: container,
		workdir:   workdir,
		env:       append([]string(nil), env...),
		argv:      append([]string(nil), argv...),
	})
	d.permit = permission
	newClient := d.newClient
	err := d.err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if newClient == nil {
		return nil, errors.New("fakeDialer: no client configured")
	}
	return newClient(), nil
}

func (d *fakeDialer) records() []dialRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]dialRecord(nil), d.dials...)
}

func (d *fakeDialer) permission() acp.PermissionHandler {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.permit
}

type fakeWorkspace struct {
	mu    sync.Mutex
	specs []cell.Spec
	dirs  []string
	err   error
}

func (w *fakeWorkspace) Prepare(_ context.Context, spec cell.Spec, dir string) error {
	w.mu.Lock()
	w.specs = append(w.specs, spec)
	w.dirs = append(w.dirs, dir)
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0700)
}

func testRuntime(t *testing.T) (*Runtime, *fakeExecutor, *fakeDialer, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	exec := &fakeExecutor{}
	dialer := &fakeDialer{}
	rt := &Runtime{
		DataDir:   t.TempDir(),
		Config:    domain.AgentConfig{Image: "test-agent:latest", MemoryMB: 1024, CPUs: 1.5, PidsLimit: 128},
		Exec:      exec,
		Dialer:    dialer,
		Workspace: &fakeWorkspace{},
		Logger:    slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return rt, exec, dialer, buf
}

func testSpec() cell.Spec {
	return cell.Spec{
		AgentID:    "agent-1",
		HiveID:     "hive-1",
		Repository: "acme/app",
		Branch:     "swarm/acme/agent-1",
		BaseBranch: "main",
		CLI:        "opencode",
		Model:      "opencode/big-pickle",
		MCPURL:     "http://127.0.0.1:8090/mcp",
		MCPToken:   "mcp-secret",
		Env:        map[string]string{"OPENCODE_AUTH_CONTENT": "auth-secret"},
	}
}

func hasArg(call []string, value string) bool {
	for _, arg := range call {
		if arg == value {
			return true
		}
	}
	return false
}

func findCall(calls [][]string, first string) ([]string, bool) {
	for _, call := range calls {
		if len(call) > 0 && call[0] == first {
			return call, true
		}
	}
	return nil, false
}

func countCalls(calls [][]string, first string) int {
	n := 0
	for _, call := range calls {
		if len(call) > 0 && call[0] == first {
			n++
		}
	}
	return n
}

func envValue(env []string, key string) (string, bool) {
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}

// runGit runs a git command in dir for test fixtures.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initBareRepo creates a bare repository with a main branch containing one file.
func initBareRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "--bare", "--initial-branch=main", bare)
	seed := filepath.Join(root, "seed")
	runGit(t, root, "clone", bare, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "-A")
	runGit(t, seed, "commit", "-m", "initial")
	runGit(t, seed, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return bare
}

func stateNotif(update string) acp.SessionNotification {
	return acp.SessionNotification{SessionID: "ses-test", Update: json.RawMessage(update)}
}
