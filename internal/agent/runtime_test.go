package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"swarmjet/internal/domain"
)

type fakeDocker struct {
	containers             map[string]container
	creates, starts, stops int
	logs                   string
	failStop               bool
	cancelCreate           context.CancelFunc
	failLogs               bool
	calls                  [][]string
}

func (f *fakeDocker) Run(ctx context.Context, args, env []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, args)
	switch args[0] {
	case "create":
		f.creates++
		name := args[2]
		if _, exists := f.containers[name]; exists {
			return nil, errors.New("exists")
		}
		var c container
		c.State.Status = "created"
		c.Config.Labels = map[string]string{"swarmjet.execution": strings.TrimPrefix(name, "sj-agent-")}
		c.Config.Env = env
		f.containers[name] = c
		if f.cancelCreate != nil {
			f.cancelCreate()
			return nil, ctx.Err()
		}
		return []byte(name), nil
	case "inspect":
		c, ok := f.containers[args[1]]
		if !ok {
			return []byte("[]"), errors.New("missing")
		}
		return json.Marshal([]container{c})
	case "rm":
		delete(f.containers, args[len(args)-1])
	case "start":
		f.starts++
		c := f.containers[args[1]]
		c.State.Status = "running"
		c.State.Running = true
		f.containers[args[1]] = c
	case "logs":
		if f.failLogs {
			return nil, errors.New("logs temporarily unavailable")
		}
		return []byte(f.logs), nil
	case "stop", "kill":
		if args[0] == "stop" && f.failStop {
			return nil, errors.New("stop failed")
		}
		f.stops++
		id := args[len(args)-1]
		c := f.containers[id]
		c.State.Running = false
		c.State.Status = "exited"
		f.containers[id] = c
	default:
		return nil, errors.New("unexpected command")
	}
	return nil, nil
}
func setup(t *testing.T) (*Runtime, *fakeDocker, domain.AgentRequest) {
	t.Helper()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "task-sources", "task", "meta", ".worktrees", "task")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	f := &fakeDocker{containers: map[string]container{}}
	r := New(dir)
	r.Executor = f
	return r, f, domain.AgentRequest{ExecutionID: "exec1", TaskID: "task", Role: "dev", Workspace: workspace, Prompt: "secret task prompt", Config: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "test-image", TimeoutSeconds: 300}, Env: map[string]string{"TOKEN": "super-secret"}}
}
func TestJSONL(t *testing.T) {
	b, session := ParseJSONL([]byte("invalid\n{\"type\":\"text\",\"part\":{\"sessionID\":\"ses_exact\"},\"text\":\"super-secret\"}\n"), []string{"super-secret"})
	if session != "ses_exact" || strings.Contains(string(b), "super-secret") {
		t.Fatalf("%s %s", b, session)
	}
}

func TestMessagePrecedesVariadicFileOption(t *testing.T) {
	r, f, req := setup(t)
	if _, e := r.Start(context.Background(), req); e != nil {
		t.Fatal(e)
	}
	for _, args := range f.calls {
		if args[0] != "create" {
			continue
		}
		message, file := -1, -1
		for i, a := range args {
			if strings.HasPrefix(a, "Execute the instructions") {
				message = i
			}
			if a == "--file" {
				file = i
			}
		}
		if message < 0 || file < 0 || message > file {
			t.Fatal("OpenCode --file would consume the prompt message as a filename")
		}
		return
	}
	t.Fatal("no container create")
}

func TestPublicEnvironmentDoesNotCorruptSessionOrLogs(t *testing.T) {
	r, f, req := setup(t)
	req.Env["GIT_TERMINAL_PROMPT"] = "0"
	req.Env["GIT_AUTHOR_NAME"] = "swarmjet"
	f.logs = `{"timestamp":100,"sessionID":"ses_001","text":"swarmjet super-secret"}`
	ex, e := r.Start(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	ex, e = r.Inspect(context.Background(), ex.ID)
	if e != nil {
		t.Fatal(e)
	}
	if ex.SessionID != "ses_001" {
		t.Fatal(ex.SessionID)
	}
	logs, e := os.ReadFile(ex.LogPath)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(logs), `"timestamp":100`) || !strings.Contains(string(logs), "swarmjet") || strings.Contains(string(logs), "super-secret") {
		t.Fatal(string(logs))
	}
}
func TestStartRecoveryAndResult(t *testing.T) {
	r, f, req := setup(t)
	ctx := context.Background()
	first, err := r.Start(ctx, req)
	if err != nil || first.Status != "running" {
		t.Fatalf("%+v %v", first, err)
	}
	r = New(r.DataDir)
	r.Executor = f
	again, err := r.Start(ctx, req)
	if err != nil || again.ID != first.ID || f.creates != 1 || f.starts != 1 {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	b, _ := os.ReadFile(r.path(req.ExecutionID))
	if strings.Contains(string(b), "super-secret") || strings.Contains(string(b), req.Prompt) {
		t.Fatal("secret persisted in metadata")
	}
	c := f.containers[first.ContainerID]
	c.State.Running = false
	c.State.Status = "exited"
	f.containers[first.ContainerID] = c
	f.logs = "{\"sessionID\":\"ses_exact\",\"text\":\"super-secret\"}\n"
	rec, _ := r.read(req.ExecutionID)
	if err = os.WriteFile(rec.ResultPath, []byte(`{"outcome":"changed","summary":"Implemented change","assumptions":[],"findings":[],"needsHelp":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	done, err := r.Inspect(ctx, req.ExecutionID)
	if err != nil || done.Status != "succeeded" || done.SessionID != "ses_exact" {
		t.Fatalf("%+v %v", done, err)
	}
	artifact, err := os.ReadFile(rec.ResultPath)
	if err != nil || !strings.Contains(string(artifact), `"needsHelp":""`) || done.FailureReason != "" {
		t.Fatalf("successful result must be normalized: %s, %v", artifact, err)
	}
	logs, _ := os.ReadFile(done.LogPath)
	if strings.Contains(string(logs), "super-secret") {
		t.Fatal("secret in logs")
	}
}
func TestMissingMalformedArtifactFails(t *testing.T) {
	for index, artifact := range []string{"", `{"outcome":`, `{"outcome":"approved","summary":"wrong role"}`} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			r, f, req := setup(t)
			ex, err := r.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if artifact != "" {
				rec, _ := r.read(req.ExecutionID)
				os.WriteFile(rec.ResultPath, []byte(artifact), 0600)
			}
			c := f.containers[ex.ContainerID]
			c.State.Running = false
			c.State.Status = "exited"
			f.containers[ex.ContainerID] = c
			ex, err = r.Inspect(context.Background(), req.ExecutionID)
			if err != nil || ex.Status != "failed" {
				t.Fatalf("%+v %v", ex, err)
			}
		})
	}
}
func TestCancelAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		r, f, req := setup(t)
		ex, err := r.Start(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		f.failStop = true
		if timeout {
			rec, _ := r.read(ex.ID)
			rec.StartedAt = time.Now().Add(-time.Hour)
			r.save(rec)
			ex, err = r.Inspect(context.Background(), ex.ID)
			if err != nil || ex.Status != "timed_out" {
				t.Fatalf("%+v %v", ex, err)
			}
		} else {
			if err = r.Cancel(context.Background(), ex.ID); err != nil {
				t.Fatal(err)
			}
			ex, _ = r.Inspect(context.Background(), ex.ID)
			if ex.Status != "cancelled" {
				t.Fatal(ex.Status)
			}
		}
		if f.stops != 1 {
			t.Fatal("not stopped")
		}
	}
}
func TestResumeExactSession(t *testing.T) {
	r, f, req := setup(t)
	if _, err := r.Resume(context.Background(), req); err == nil {
		t.Fatal("missing session accepted")
	}
	req.SessionID = "ses_exact"
	if _, err := r.Resume(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls[0], " ")
	if !strings.Contains(joined, "--session ses_exact") || strings.Contains(joined, "--continue") {
		t.Fatal(joined)
	}
}
func TestRecoveryBeforeContainerCreate(t *testing.T) {
	r, f, req := setup(t)
	_, err := r.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := r.read(req.ExecutionID)
	rec.Execution.Status = "starting"
	r.save(rec)
	delete(f.containers, rec.Execution.ContainerID)
	ex, err := r.Start(context.Background(), req)
	if err != nil || ex.Status != "running" {
		t.Fatalf("%+v %v", ex, err)
	}
}
func TestContextAndAdapter(t *testing.T) {
	r, _, req := setup(t)
	req.Config.Adapter = "claude"
	if _, err := r.Start(context.Background(), req); err == nil {
		t.Fatal("unsupported adapter")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Inspect(ctx, "missing"); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestPrivateSamePathMountsAndRoleHistory(t *testing.T) {
	r, f, req := setup(t)
	config := filepath.Join(r.DataDir, "opencode-config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	homes := map[string]string{}
	for _, role := range []string{"dev", "review", "merge"} {
		req.Role = role
		req.ExecutionID = "execution-" + role
		if _, err := r.Start(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		var args []string
		for _, call := range f.calls {
			if call[0] == "create" && call[2] == "sj-agent-"+req.ExecutionID {
				args = call
			}
		}
		scope := role
		if scope == "merge" {
			scope = "review"
		}
		expected := map[string]bool{
			bindMount(filepath.Join(r.DataDir, "task-sources", req.TaskID), false):                        true,
			bindMount(filepath.Join(r.DataDir, "runs", req.TaskID, "executions", req.ExecutionID), false): true,
			bindMount(filepath.Join(r.DataDir, "runs", req.TaskID, "integration"), true):                  true,
			bindMount(filepath.Join(r.DataDir, "opencode", req.TaskID, scope), false):                     true,
			bindMount(filepath.Join(r.DataDir, "opencode", req.TaskID, scope+"-config"), false):           true,
		}
		for i, arg := range args {
			if arg == "--mount" {
				if !expected[args[i+1]] {
					t.Fatalf("unexpected mount exposing controls or siblings: %s", args[i+1])
				}
				delete(expected, args[i+1])
			}
			if strings.HasPrefix(arg, "XDG_DATA_HOME=") {
				homes[role] = strings.TrimPrefix(arg, "XDG_DATA_HOME=")
			}
			if strings.Contains(arg, "super-secret") {
				t.Fatal("credential in Docker arguments")
			}
		}
		if len(expected) != 0 {
			t.Fatalf("missing mounts %v", expected)
		}
		userFound, homeFound := false, false
		for i, arg := range args {
			if arg == "--user" && i+1 < len(args) {
				userFound = args[i+1] == strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid())
			}
			if arg == "HOME="+filepath.Join(r.DataDir, "opencode", req.TaskID, scope) {
				homeFound = true
			}
		}
		if !userFound || !homeFound {
			t.Fatal("agent must use the worker's uid/gid and a private writable home")
		}
	}
	if homes["dev"] == homes["review"] || homes["merge"] != homes["review"] {
		t.Fatalf("history isolation: %v", homes)
	}
}
func TestRejectMountSymlinksAndSiblingWorkspace(t *testing.T) {
	for _, mode := range []string{"source", "workspace", "execution", "config", "sibling", "missing"} {
		t.Run(mode, func(t *testing.T) {
			r, f, req := setup(t)
			switch mode {
			case "source":
				root := filepath.Join(r.DataDir, "task-sources", req.TaskID)
				os.RemoveAll(root)
				os.Symlink(t.TempDir(), root)
			case "workspace":
				os.RemoveAll(req.Workspace)
				os.Symlink(t.TempDir(), req.Workspace)
			case "execution":
				parent := filepath.Join(r.DataDir, "runs", req.TaskID)
				os.MkdirAll(parent, 0700)
				os.Symlink(t.TempDir(), filepath.Join(parent, "executions"))
			case "config":
				os.Symlink(t.TempDir(), filepath.Join(r.DataDir, "opencode-config"))
			case "sibling":
				req.Workspace = filepath.Join(r.DataDir, "task-sources", "other")
				os.MkdirAll(req.Workspace, 0700)
			case "missing":
				os.RemoveAll(filepath.Join(r.DataDir, "task-sources", req.TaskID))
			}
			if _, err := r.Start(context.Background(), req); err == nil {
				t.Fatal("unsafe path accepted")
			}
			if f.creates != 0 {
				t.Fatal("container created with unsafe mount")
			}
		})
	}
}
func TestCancelCapturesSessionBeforeTerminal(t *testing.T) {
	r, f, req := setup(t)
	ex, err := r.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.logs = "{\"sessionID\":\"ses_started_just_now\",\"text\":\"super-secret\"}\n"
	if err = r.Cancel(context.Background(), ex.ID); err != nil {
		t.Fatal(err)
	}
	ex, err = r.Inspect(context.Background(), ex.ID)
	if err != nil || ex.Status != "cancelled" || ex.SessionID != "ses_started_just_now" {
		t.Fatalf("%+v %v", ex, err)
	}
	content, err := os.ReadFile(ex.LogPath)
	if err != nil || strings.Contains(string(content), "super-secret") || !strings.Contains(string(content), "ses_started_just_now") {
		t.Fatalf("%s %v", content, err)
	}
}
func TestCancelCreatedAndAbsentStartingContainers(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(map[bool]string{false: "created", true: "absent"}[absent], func(t *testing.T) {
			r, f, req := setup(t)
			ex, err := r.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			rec, _ := r.read(req.ExecutionID)
			rec.Execution.Status = "starting"
			r.save(rec)
			if absent {
				delete(f.containers, ex.ContainerID)
			} else {
				c := f.containers[ex.ContainerID]
				c.State.Status = "created"
				c.State.Running = false
				f.containers[ex.ContainerID] = c
			}
			if err = r.Cancel(context.Background(), ex.ID); err != nil {
				t.Fatal(err)
			}
			if _, exists := f.containers[ex.ContainerID]; exists {
				t.Fatal("unstarted container orphaned")
			}
			got, err := r.Inspect(context.Background(), ex.ID)
			if err != nil || got.Status != "cancelled" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
func TestResultSymlinkRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "result.json")
	target := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(target, []byte(`{"outcome":"changed","summary":"secret"}`), 0600)
	os.Symlink(target, p)
	if _, err := ReadResult(p, "dev"); err == nil {
		t.Fatal("symlink result accepted")
	}
}

func TestResultFalseNeedsHelpAndBlockedValidation(t *testing.T) {
	for _, scenario := range []struct {
		name, outcome, help string
		wantOK              bool
	}{
		{"no-help-false", "changed", "false", true},
		{"no-help-string", "changed", `""`, true},
		{"blocked-explanation", "blocked", `"Test account is missing"`, true},
		{"blocked-false", "blocked", "false", false},
		{"blocked-empty", "blocked", `""`, false},
		{"boolean-true", "changed", "true", false},
		{"object", "changed", `{}`, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			body := fmt.Sprintf(`{"outcome":%q,"summary":"Updated README","assumptions":[],"findings":[],"needsHelp":%s}`, scenario.outcome, scenario.help)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := ReadResult(path, "dev")
			if (err == nil) != scenario.wantOK {
				t.Fatalf("unexpected validation result: %+v, %v", result, err)
			}
		})
	}
}

func TestInvalidResultPreservesFailureReason(t *testing.T) {
	r, f, req := setup(t)
	ex, err := r.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	c := f.containers[ex.ContainerID]
	c.State.Running = false
	c.State.Status = "exited"
	c.State.ExitCode = 0
	f.containers[ex.ContainerID] = c
	rec, err := r.read(req.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(rec.ResultPath, []byte(`{"outcome":"changed","summary":"Updated README","needsHelp":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	done, err := r.Inspect(context.Background(), ex.ID)
	if err != nil || done.Status != "failed" || done.ExitCode != 0 || !strings.Contains(done.FailureReason, "needsHelp must be a string") {
		t.Fatalf("missing result validation diagnosis: %+v, %v", done, err)
	}
	durable, err := r.read(ex.ID)
	if err != nil || durable.Execution.FailureReason != done.FailureReason {
		t.Fatal("failure reason not persisted", err)
	}
}

func TestInterruptedCreateCleansUpOwnedContainer(t *testing.T) {
	r, f, req := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancelCreate = cancel
	ex, err := r.Start(ctx, req)
	if !errors.Is(err, context.Canceled) || ex.Status != "cancelled" {
		t.Fatalf("%+v %v", ex, err)
	}
	if len(f.containers) != 0 {
		t.Fatal("interrupted create left an orphan")
	}
}
func TestCancelRetriesFinalLogCollection(t *testing.T) {
	r, f, req := setup(t)
	ex, err := r.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.failLogs = true
	if err = r.Cancel(context.Background(), ex.ID); err == nil {
		t.Fatal("log collection error ignored")
	}
	rec, _ := r.read(ex.ID)
	if terminal(rec.Execution.Status) {
		t.Fatal("terminal state persisted before final logs")
	}
	f.failLogs = false
	f.logs = `{"sessionID":"ses_recoverable"}`
	if err = r.Cancel(context.Background(), ex.ID); err != nil {
		t.Fatal(err)
	}
	got, err := r.Inspect(context.Background(), ex.ID)
	if err != nil || got.SessionID != "ses_recoverable" || got.Status != "cancelled" {
		t.Fatalf("%+v %v", got, err)
	}
}
