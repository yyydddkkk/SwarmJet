package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/yyydddkkk/SwarmJet/internal/agent"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/acp"
)

func startWithFake(t *testing.T, rt *Runtime, exec *fakeExecutor, dialer *fakeDialer, client *fakeACP, spec cell.Spec) cell.Session {
	t.Helper()
	exec.setHandler(dockerHandler(false, false))
	dialer.newClient = func() ACPClient { return client }
	session, err := rt.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return session
}

func TestStartCreatesContainerAndNewSession(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	client := &fakeACP{
		initResult: acp.InitializeResult{
			ProtocolVersion:   acp.ProtocolVersion,
			AgentCapabilities: acp.AgentCapabilities{LoadSession: true},
		},
		newResult: acp.NewSessionResult{SessionID: "ses-new"},
	}
	spec := testSpec()
	session := startWithFake(t, rt, exec, dialer, client, spec)
	if session.Ref() != "ses-new" {
		t.Fatalf("Ref = %q, want ses-new", session.Ref())
	}

	calls := exec.snapshot()
	create, ok := findCall(calls, "create")
	if !ok {
		t.Fatal("docker create was not called")
	}
	wantArgs := []string{
		"--name", "swarmjet-cell-agent-1",
		"--label", "swarmjet.cell=true",
		"--label", "swarmjet.agent=agent-1",
		"--label", "swarmjet.hive=hive-1",
		"--workdir", "/workspace",
		"--mount", "type=bind,src=" + WorkspaceDir(rt.DataDir, spec.AgentID) + ",dst=/workspace",
		"--mount", "type=volume,src=swarmjet-cell-agent-1-data,dst=/cell",
		"--memory", "1024m",
		"--cpus", "1.5",
		"--pids-limit", "128",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--user", agent.ContainerUser(),
		"--entrypoint", "/bin/sh",
	}
	for _, want := range wantArgs {
		if !hasArg(create, want) {
			t.Errorf("docker create missing %q: %v", want, create)
		}
	}
	if !hasArg(create, "test-agent:latest") {
		t.Errorf("docker create missing image: %v", create)
	}
	if countCalls(calls, "start") != 1 {
		t.Errorf("docker start called %d times, want 1", countCalls(calls, "start"))
	}

	records := dialer.records()
	if len(records) != 1 {
		t.Fatalf("dialer called %d times, want 1", len(records))
	}
	record := records[0]
	if record.container != "swarmjet-cell-agent-1" || record.workdir != "/workspace" {
		t.Fatalf("dial target = %+v", record)
	}
	if strings.Join(record.argv, " ") != "opencode acp" {
		t.Fatalf("argv = %v", record.argv)
	}
	config, ok := envValue(record.env, "OPENCODE_CONFIG_CONTENT")
	if !ok || !strings.Contains(config, `"model":"opencode/big-pickle"`) {
		t.Fatalf("OPENCODE_CONFIG_CONTENT = %q", config)
	}
	if auth, ok := envValue(record.env, "OPENCODE_AUTH_CONTENT"); !ok || auth != "auth-secret" {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %q", auth)
	}
	if home, _ := envValue(record.env, "HOME"); home != "/cell/home" {
		t.Fatalf("HOME = %q", home)
	}

	news := client.newSessionCalls()
	if len(news) != 1 || len(news[0]) != 1 {
		t.Fatalf("MCP servers passed to session/new = %+v", news)
	}
	server := news[0][0]
	if server.Name != "swarmjet" || server.Type != "http" || server.URL != spec.MCPURL {
		t.Fatalf("MCP server = %+v", server)
	}
	if len(server.Headers) != 1 || server.Headers[0].Name != "Authorization" || server.Headers[0].Value != "Bearer mcp-secret" {
		t.Fatalf("MCP headers = %+v", server.Headers)
	}
}

func TestStartLoadsExistingSession(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	client := &fakeACP{
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
		newResult:  acp.NewSessionResult{SessionID: "ses-new"},
		loadReplay: []acp.SessionNotification{stateNotif(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"replayed"}}`)},
	}
	spec := testSpec()
	spec.SessionRef = "ses-old"
	session := startWithFake(t, rt, exec, dialer, client, spec)
	if session.Ref() != "ses-old" {
		t.Fatalf("Ref = %q, want ses-old", session.Ref())
	}
	if calls := client.loadSessionCalls(); len(calls) != 1 || calls[0] != "ses-old" {
		t.Fatalf("session/load calls = %v", calls)
	}
	if news := client.newSessionCalls(); len(news) != 0 {
		t.Fatalf("session/new must not be called when load succeeds: %d", len(news))
	}
}

func TestStartFallsBackWhenLoadFails(t *testing.T) {
	rt, exec, dialer, logs := testRuntime(t)
	client := &fakeACP{
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
		newResult:  acp.NewSessionResult{SessionID: "ses-new"},
		loadErr:    errors.New("session/load rejected auth-secret"),
	}
	spec := testSpec()
	spec.SessionRef = "ses-old"
	session := startWithFake(t, rt, exec, dialer, client, spec)
	if session.Ref() != "ses-new" {
		t.Fatalf("Ref = %q, want fallback ses-new", session.Ref())
	}
	if news := client.newSessionCalls(); len(news) != 1 {
		t.Fatalf("session/new calls = %d, want 1", len(news))
	}
	if !strings.Contains(logs.String(), "session/load failed") {
		t.Fatalf("fallback was not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), "auth-secret") {
		t.Fatalf("credential leaked into logs: %s", logs.String())
	}
}

func TestStartWithoutLoadSupportCreatesNewSession(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	client := &fakeACP{
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: false}},
		newResult:  acp.NewSessionResult{SessionID: "ses-new"},
	}
	spec := testSpec()
	spec.SessionRef = "ses-old"
	session := startWithFake(t, rt, exec, dialer, client, spec)
	if session.Ref() != "ses-new" {
		t.Fatalf("Ref = %q, want ses-new", session.Ref())
	}
	if calls := client.loadSessionCalls(); len(calls) != 0 {
		t.Fatalf("session/load must not be attempted without capability: %v", calls)
	}
}

func TestPromptStreamsTruncatesAndPermissions(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	client := &fakeACP{
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
		newResult:  acp.NewSessionResult{SessionID: "ses-new"},
	}
	spec := testSpec()
	session := startWithFake(t, rt, exec, dialer, client, spec)

	longText := strings.Repeat("a", cell.MaxUpdateText+500)
	var outcome acp.PermissionOutcome
	client.promptFn = func(ctx context.Context, _ string, sink acp.UpdateSink) (acp.PromptResult, error) {
		for _, update := range []string{
			`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello "}}`,
			`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}`,
			`{"sessionUpdate":"tool_call","toolCallId":"1","title":"bash","kind":"execute","status":"pending","rawInput":{"command":"ls"}}`,
			`{"sessionUpdate":"tool_call_update","toolCallId":"1","status":"completed","title":"bash","rawOutput":{"output":"ok"}}`,
			`{"sessionUpdate":"plan","entries":[{"content":"step"}]}`,
			`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + longText + `"}}`,
		} {
			if err := sink(stateNotif(update)); err != nil {
				return acp.PromptResult{}, err
			}
		}
		handler := dialer.permission()
		if handler == nil {
			return acp.PromptResult{}, errors.New("no permission handler")
		}
		var err error
		outcome, err = handler(ctx, acp.PermissionRequest{
			SessionID: "ses-new",
			ToolCall:  acp.SessionUpdate{Title: "bash", Kind: "execute"},
			Options: []acp.PermissionOption{
				{OptionID: "once", Kind: acp.PermissionAllowOnce},
				{OptionID: "always", Kind: acp.PermissionAllowAlways},
			},
		})
		if err != nil {
			return acp.PromptResult{}, err
		}
		return acp.PromptResult{
			StopReason: acp.StopEndTurn,
			Usage:      &acp.Usage{InputTokens: 7, OutputTokens: 3},
		}, nil
	}

	var updates []cell.Update
	result, err := session.Prompt(context.Background(), "do work", func(update cell.Update) {
		updates = append(updates, update)
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.StopReason != string(acp.StopEndTurn) || result.InputTokens != 7 || result.OutputTokens != 3 {
		t.Fatalf("TurnResult = %+v", result)
	}
	kinds := map[cell.UpdateKind]int{}
	for _, update := range updates {
		kinds[update.Kind]++
		if update.At.IsZero() {
			t.Errorf("update has zero timestamp: %+v", update)
		}
	}
	if kinds[cell.UpdateText] < 2 || kinds[cell.UpdateThought] != 1 || kinds[cell.UpdateToolCall] < 2 || kinds[cell.UpdateToolDone] != 1 || kinds[cell.UpdatePlan] != 1 {
		t.Fatalf("update kinds = %v, updates = %+v", kinds, updates)
	}
	var sawPermission, sawTruncated bool
	for _, update := range updates {
		if strings.HasPrefix(update.Title, "permission: ") && strings.Contains(update.Title, "bash") {
			sawPermission = true
		}
		if update.Kind == cell.UpdateText && len([]rune(update.Text)) == cell.MaxUpdateText && strings.HasPrefix(update.Text, "aaa") {
			sawTruncated = true
		}
		if len([]rune(update.Text)) > cell.MaxUpdateText {
			t.Fatalf("update text exceeds cap: %d", len([]rune(update.Text)))
		}
	}
	if !sawPermission {
		t.Fatal("permission approval was not reported as an update")
	}
	if !sawTruncated {
		t.Fatal("long streamed text was not truncated to MaxUpdateText")
	}
	if outcome.Outcome != "selected" || outcome.OptionID != "once" {
		t.Fatalf("permission outcome = %+v, want selected/once", outcome)
	}

	if err := session.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	client.mu.Lock()
	cancels := len(client.cancels)
	client.mu.Unlock()
	if cancels != 1 {
		t.Fatalf("cancel calls = %d, want 1", cancels)
	}

	client.promptFn = func(context.Context, string, acp.UpdateSink) (acp.PromptResult, error) {
		return acp.PromptResult{StopReason: acp.StopEndTurn}, nil
	}
	if _, err := session.Prompt(context.Background(), "again", nil); err != nil {
		t.Fatalf("Prompt after Cancel: %v", err)
	}
	if client.promptCount() != 2 {
		t.Fatalf("prompt count = %d, want 2", client.promptCount())
	}
	if _, err := session.Prompt(context.Background(), "", nil); err == nil {
		t.Fatal("empty prompt must fail")
	}
}

func TestSecretsNeverAppearInErrorsOrLogs(t *testing.T) {
	rt, exec, dialer, logs := testRuntime(t)
	client := &fakeACP{
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
		newResult:  acp.NewSessionResult{SessionID: "ses-new"},
	}
	dialer.newClient = func() ACPClient { return client }
	spec := testSpec()

	exec.setHandler(func(args []string, _ []string) (Result, error) {
		if len(args) > 0 && args[0] == "create" {
			return Result{ExitCode: 1, Stderr: []byte("failed for auth-secret and mcp-secret")}, nil
		}
		return dockerHandler(false, false)(args, nil)
	})
	if _, err := rt.Start(context.Background(), spec); err == nil {
		t.Fatal("expected create failure")
	} else if strings.Contains(err.Error(), "auth-secret") || strings.Contains(err.Error(), "mcp-secret") {
		t.Fatalf("secret leaked into error: %v", err)
	}

	exec.setHandler(dockerHandler(false, false))
	dialer.err = errors.New("docker exec failed with auth-secret")
	if _, err := rt.Start(context.Background(), spec); err == nil {
		t.Fatal("expected dial failure")
	} else if strings.Contains(err.Error(), "auth-secret") {
		t.Fatalf("secret leaked into dial error: %v", err)
	}

	dialer.err = nil
	client.loadErr = errors.New("cannot load auth-secret")
	spec.SessionRef = "ses-old"
	if _, err := rt.Start(context.Background(), spec); err != nil {
		t.Fatalf("fallback start: %v", err)
	}
	for _, secret := range []string{"auth-secret", "mcp-secret"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("secret %q leaked into logs: %s", secret, logs.String())
		}
	}
}

func TestStopRemoveDestroyAreIdempotent(t *testing.T) {
	rt, exec, _, _ := testRuntime(t)
	ctx := context.Background()

	exec.setHandler(func(args []string, _ []string) (Result, error) {
		switch {
		case len(args) > 0 && (args[0] == "stop" || args[0] == "rm"):
			return Result{ExitCode: 1, Stderr: []byte("Error: No such container")}, nil
		case len(args) > 0 && args[0] == "volume":
			return Result{ExitCode: 1, Stderr: []byte("Error: No such volume")}, nil
		default:
			return Result{}, nil
		}
	})
	if err := rt.Stop(ctx, "missing"); err != nil {
		t.Fatalf("Stop missing: %v", err)
	}
	if err := rt.Remove(ctx, "missing"); err != nil {
		t.Fatalf("Remove missing: %v", err)
	}
	if err := rt.Destroy(ctx, "missing"); err != nil {
		t.Fatalf("Destroy missing: %v", err)
	}

	workspace := WorkspaceDir(rt.DataDir, "agent-1")
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	exec.setHandler(func([]string, []string) (Result, error) { return Result{}, nil })
	if err := rt.Destroy(ctx, "agent-1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace was not removed: %v", err)
	}
}

func TestStartReusesRunningContainer(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	client := &fakeACP{
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
		newResult:  acp.NewSessionResult{SessionID: "ses-new"},
	}
	dialer.newClient = func() ACPClient { return client }
	exec.setHandler(dockerHandler(true, true))
	if _, err := rt.Start(context.Background(), testSpec()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	calls := exec.snapshot()
	if countCalls(calls, "create") != 0 || countCalls(calls, "start") != 0 {
		t.Fatalf("running container was recreated: %v", calls)
	}
	if len(calls) != 1 || calls[0][0] != "inspect" {
		t.Fatalf("unexpected docker calls: %v", calls)
	}
}

func TestStartValidatesSpec(t *testing.T) {
	rt, exec, _, _ := testRuntime(t)
	cases := map[string]func(*cell.Spec){
		"agent id":     func(s *cell.Spec) { s.AgentID = "../escape" },
		"hive id":      func(s *cell.Spec) { s.HiveID = "" },
		"repository":   func(s *cell.Spec) { s.Repository = "not-a-repo" },
		"branch":       func(s *cell.Spec) { s.Branch = "-main" },
		"base branch":  func(s *cell.Spec) { s.BaseBranch = "a..b" },
		"model":        func(s *cell.Spec) { s.Model = "" },
		"cli":          func(s *cell.Spec) { s.CLI = "codex" },
		"env name":     func(s *cell.Spec) { s.Env["bad name"] = "x" },
		"reserved env": func(s *cell.Spec) { s.Env["HOME"] = "/tmp" },
		"mcp url":      func(s *cell.Spec) { s.MCPURL = "ftp://example.com" },
	}
	for name, mutate := range cases {
		spec := testSpec()
		mutate(&spec)
		if _, err := rt.Start(context.Background(), spec); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	if calls := exec.snapshot(); len(calls) != 0 {
		t.Fatalf("invalid specs reached docker: %v", calls)
	}
}

func TestTruncateTextKeepsUTF8(t *testing.T) {
	in := strings.Repeat("蜜", cell.MaxUpdateText+10)
	out := truncateText(in, cell.MaxUpdateText)
	if len([]rune(out)) != cell.MaxUpdateText {
		t.Fatalf("runes = %d, want %d", len([]rune(out)), cell.MaxUpdateText)
	}
	if !strings.HasPrefix(out, "蜜") {
		t.Fatal("truncation lost leading characters")
	}
	if truncateText("short", 10) != "short" {
		t.Fatal("short text changed")
	}
}

func TestWorkspaceCloneCreatesAgentBranch(t *testing.T) {
	bare := initBareRepo(t)
	rt, exec, dialer, _ := testRuntime(t)
	rt.Workspace = &GitWorkspace{Git: &Git{Remote: func(string) string { return bare }}}
	client := &fakeACP{
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
		newResult:  acp.NewSessionResult{SessionID: "ses-new"},
	}
	dialer.newClient = func() ACPClient { return client }
	exec.setHandler(dockerHandler(false, false))
	spec := testSpec()
	if _, err := rt.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	workspace := WorkspaceDir(rt.DataDir, spec.AgentID)
	content, err := os.ReadFile(filepath.Join(workspace, "README.md"))
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("workspace content = %q, err = %v", content, err)
	}
	if branch := runGit(t, workspace, "symbolic-ref", "--short", "HEAD"); branch != spec.Branch {
		t.Fatalf("branch = %q, want %q", branch, spec.Branch)
	}
}

func TestWorkspaceKeepsDirtyWorkAndRefusesBranchSwitch(t *testing.T) {
	bare := initBareRepo(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	dataDir := t.TempDir()
	spec := testSpec()
	gitWorkspace := &GitWorkspace{Git: &Git{Remote: func(string) string { return bare }}}
	dir := WorkspaceDir(dataDir, spec.AgentID)
	if err := gitWorkspace.Prepare(context.Background(), spec, dir); err != nil {
		t.Fatalf("first Prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("wip"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gitWorkspace.Prepare(context.Background(), spec, dir); err != nil {
		t.Fatalf("Prepare on same branch must keep dirty work: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dirty.txt")); err != nil {
		t.Fatalf("dirty file disappeared: %v", err)
	}
	runGit(t, dir, "checkout", "main")
	if err := gitWorkspace.Prepare(context.Background(), spec, dir); err == nil {
		t.Fatal("switching branches with uncommitted changes must fail")
	}
}
