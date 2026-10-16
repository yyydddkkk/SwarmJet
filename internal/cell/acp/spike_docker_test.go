package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This integration test drives a real OpenCode process inside the agent image.
// It is skipped unless SWARMJET_OPENCODE_SPIKE=1, because it needs Docker, the
// agent image, and network access to a model provider.
//
//	SWARMJET_OPENCODE_SPIKE=1 go test ./internal/cell/acp/ -run TestOpenCodeACPDockerSpike -v
//
// Optional environment:
//
//	SWARMJET_OPENCODE_SPIKE_IMAGE      image tag (default swarmjet-agent-opencode-spike:local)
//	SWARMJET_OPENCODE_SPIKE_DOCKER     docker binary (default docker)
//	SWARMJET_OPENCODE_SPIKE_MODEL      provider/model (default opencode/big-pickle, free)
//	SWARMJET_OPENCODE_SPIKE_AUTH_FILE  path to an OpenCode auth.json; injected as
//	                                   OPENCODE_AUTH_CONTENT, never written to disk
//	SWARMJET_OPENCODE_SPIKE_KEEP=1     leave containers and volumes behind
//
// No credential value is ever logged.

const mcpSpikeServer = `#!/usr/bin/env python3
import sys, json
def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n"); sys.stdout.flush()
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    method, rid = msg.get("method"), msg.get("id")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": rid, "result": {"protocolVersion": msg["params"].get("protocolVersion", "2025-06-18"), "capabilities": {"tools": {}}, "serverInfo": {"name": "swarmjet-spike", "version": "0.1.0"}}})
    elif method == "notifications/initialized":
        pass
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": rid, "result": {"tools": [{"name": "swarmjet_ping", "description": "Returns a fixed pong string from the SwarmJet spike MCP server.", "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False}}]}})
    elif method == "tools/call":
        send({"jsonrpc": "2.0", "id": rid, "result": {"content": [{"type": "text", "text": "MCP_PONG_FROM_SWARMJET"}]}})
    elif rid is not None:
        send({"jsonrpc": "2.0", "id": rid, "error": {"code": -32601, "message": "method not found"}})
`

type dockerSpike struct {
	t         *testing.T
	docker    string
	image     string
	model     string
	keep      bool
	container string
	volume    string
	hostDir   string // mounted read-only at /spike
	authFile  string
}

func requireDockerSpike(t *testing.T) *dockerSpike {
	t.Helper()
	if os.Getenv("SWARMJET_OPENCODE_SPIKE") != "1" {
		t.Skip("set SWARMJET_OPENCODE_SPIKE=1 to run the OpenCode-in-Docker spike")
	}
	docker := envOr("SWARMJET_OPENCODE_SPIKE_DOCKER", "docker")
	if _, err := exec.LookPath(docker); err != nil {
		t.Skipf("%s not found: %v", docker, err)
	}
	return &dockerSpike{
		t:        t,
		docker:   docker,
		image:    envOr("SWARMJET_OPENCODE_SPIKE_IMAGE", "swarmjet-agent-opencode-spike:local"),
		model:    envOr("SWARMJET_OPENCODE_SPIKE_MODEL", "opencode/big-pickle"),
		keep:     os.Getenv("SWARMJET_OPENCODE_SPIKE_KEEP") == "1",
		volume:   fmt.Sprintf("swarmjet-spike-vol-%d", time.Now().UnixNano()),
		hostDir:  t.TempDir(),
		authFile: os.Getenv("SWARMJET_OPENCODE_SPIKE_AUTH_FILE"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (s *dockerSpike) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.docker, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *dockerSpike) mustRun(args ...string) string {
	s.t.Helper()
	out, err := s.run(args...)
	if err != nil {
		s.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// ensureImage builds deploy/Dockerfile.agent with AGENT_CLIS=opencode when the
// tag is missing.
func (s *dockerSpike) ensureImage() {
	s.t.Helper()
	if _, err := s.run("image", "inspect", s.image); err == nil {
		return
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		s.t.Fatal(err)
	}
	dockerfile := filepath.Join(root, "deploy", "Dockerfile.agent")
	if _, err := os.Stat(dockerfile); err != nil {
		s.t.Fatalf("agent Dockerfile not found: %v", err)
	}
	s.t.Logf("building %s from %s (this takes a few minutes)", s.image, dockerfile)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.docker, "build",
		"-f", dockerfile,
		"--build-arg", "AGENT_CLIS=opencode",
		"-t", s.image,
		root,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		s.t.Fatalf("docker build: %v", err)
	}
}

func (s *dockerSpike) cleanup() {
	if s.keep {
		return
	}
	if s.container != "" {
		_, _ = s.run("rm", "-f", "-v", s.container)
	}
	_, _ = s.run("volume", "rm", "-f", s.volume)
}

// startContainer launches a long-lived Cell container with a persistent data
// volume, mirroring the ADR's Cell (container + volumes).
func (s *dockerSpike) startContainer() {
	s.t.Helper()
	s.container = fmt.Sprintf("swarmjet-spike-%d", time.Now().UnixNano())
	script := filepath.Join(s.hostDir, "mcp_server.py")
	if err := os.WriteFile(script, []byte(mcpSpikeServer), 0o755); err != nil {
		s.t.Fatal(err)
	}
	s.mustRun("run", "-d", "--name", s.container,
		"-v", s.volume+":/data",
		"-v", s.hostDir+":/spike:ro",
		"--entrypoint", "/bin/sh", s.image, "-c", "sleep infinity",
	)
}

func (s *dockerSpike) stopContainer() {
	s.t.Helper()
	if s.container == "" {
		return
	}
	s.mustRun("rm", "-f", "-v", s.container)
	s.container = ""
}

// cellEnv is the environment injected into the container at exec time. Cell
// credentials live here so they never enter the image.
func (s *dockerSpike) cellEnv(config map[string]any) []string {
	env := []string{
		"HOME=/root",
		"XDG_DATA_HOME=/data/opencode-data",
		"XDG_CONFIG_HOME=/data/opencode-config",
		"XDG_STATE_HOME=/data/opencode-state",
		"OPENCODE_DISABLE_AUTOUPDATE=true",
	}
	cfg := map[string]any{"model": s.model}
	for k, v := range config {
		cfg[k] = v
	}
	raw, _ := json.Marshal(cfg)
	env = append(env, "OPENCODE_CONFIG_CONTENT="+string(raw))
	if s.authFile != "" {
		b, err := os.ReadFile(s.authFile)
		if err != nil {
			s.t.Fatalf("read SWARMJET_OPENCODE_SPIKE_AUTH_FILE: %v", err)
		}
		env = append(env, "OPENCODE_AUTH_CONTENT="+strings.TrimSpace(string(b)))
	}
	return env
}

func (s *dockerSpike) connect(ctx context.Context, config map[string]any, opts ...Option) *Client {
	s.t.Helper()
	cell := DockerCell{
		DockerBinary: s.docker,
		Container:    s.container,
		Env:          s.cellEnv(config),
		Argv:         []string{"opencode", "acp"},
	}
	client, err := cell.Connect(ctx, opts...)
	if err != nil {
		s.t.Fatalf("connect: %v", err)
	}
	return client
}

// recorder collects streamed updates across a turn.
type spikeRecorder struct {
	mu       sync.Mutex
	text     string
	tools    []SessionUpdate
	first    chan struct{}
	firstOne sync.Once
}

func newSpikeRecorder() *spikeRecorder {
	return &spikeRecorder{first: make(chan struct{})}
}

func (r *spikeRecorder) sink(n SessionNotification) error {
	u, err := n.Decode()
	if err != nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch u.SessionUpdate {
	case UpdateAgentMessageChunk:
		r.text += u.Text()
	case UpdateToolCall, UpdateToolCallUpdate:
		r.tools = append(r.tools, u)
	default:
		return nil
	}
	r.firstOne.Do(func() { close(r.first) })
	return nil
}

func (r *spikeRecorder) snapshot() (string, []SessionUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text, append([]SessionUpdate(nil), r.tools...)
}

func (r *spikeRecorder) waitFirst(d time.Duration) bool {
	select {
	case <-r.first:
		return true
	case <-time.After(d):
		return false
	}
}

func waitText(t *testing.T, r *spikeRecorder, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		text, _ := r.snapshot()
		if strings.Contains(text, want) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	text, _ := r.snapshot()
	t.Fatalf("timed out waiting for %q in streamed text; got %q", want, text)
}

// TestOpenCodeACPDockerSpike is the executable form of the spike report. It
// proves the four required behaviours in a real container, plus permissions and
// a stdio MCP server.
func TestOpenCodeACPDockerSpike(t *testing.T) {
	s := requireDockerSpike(t)
	s.ensureImage()
	t.Cleanup(s.cleanup)
	s.startContainer()

	base := context.Background()

	// 1. Connect, create a session, stream one turn.
	ctx, cancel := context.WithTimeout(base, 10*time.Minute)
	defer cancel()
	client := s.connect(ctx, nil)
	init, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	t.Logf("agent=%+v loadSession=%v protocolVersion=%d", init.AgentInfo, init.AgentCapabilities.LoadSession, init.ProtocolVersion)
	if !init.AgentCapabilities.LoadSession {
		t.Fatal("agent does not advertise loadSession; restart recovery would not work")
	}
	session, err := client.NewSession(ctx, "/workspace", nil)
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	t.Logf("session=%s model=%s", session.SessionID, sessionModel(session))

	rec := newSpikeRecorder()
	turnCtx, turnCancel := context.WithTimeout(base, 5*time.Minute)
	result, err := client.Prompt(turnCtx, session.SessionID, []ContentBlock{TextBlock("Reply with exactly: pong")}, rec.sink)
	turnCancel()
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.StopReason != StopEndTurn {
		t.Fatalf("stopReason = %q, want end_turn", result.StopReason)
	}
	text, _ := rec.snapshot()
	t.Logf("streamed text: %q usage=%+v", text, result.Usage)
	if !strings.Contains(strings.ToLower(text), "pong") {
		t.Fatalf("streamed text %q does not contain pong", text)
	}

	// 2. Multi-turn in the same session, with memory of the codeword.
	rec2 := newSpikeRecorder()
	turnCtx, turnCancel = context.WithTimeout(base, 5*time.Minute)
	if _, err := client.Prompt(turnCtx, session.SessionID, []ContentBlock{TextBlock("Remember the codeword BUMBLEBEE. Reply OK.")}, rec2.sink); err != nil {
		turnCancel()
		t.Fatalf("prompt 2: %v", err)
	}
	turnCancel()
	rec3 := newSpikeRecorder()
	turnCtx, turnCancel = context.WithTimeout(base, 5*time.Minute)
	if _, err := client.Prompt(turnCtx, session.SessionID, []ContentBlock{TextBlock("What was the codeword? Reply with just the word.")}, rec3.sink); err != nil {
		turnCancel()
		t.Fatalf("prompt 3: %v", err)
	}
	turnCancel()
	waitText(t, rec3, "BUMBLEBEE", 4*time.Minute)
	t.Logf("multi-turn memory: %q", mustText(rec3))

	// 3. Cancel a running turn; the process and session stay usable.
	longRec := newSpikeRecorder()
	cancelled := make(chan PromptResult, 1)
	errc := make(chan error, 1)
	go func() {
		pctx, pcancel := context.WithTimeout(base, 5*time.Minute)
		defer pcancel()
		res, err := client.Prompt(pctx, session.SessionID, []ContentBlock{TextBlock("Write a very long 3000 word essay about beekeeping.")}, longRec.sink)
		if err != nil {
			errc <- err
			return
		}
		cancelled <- res
	}()
	if !longRec.waitFirst(3 * time.Minute) {
		t.Fatal("no streamed output before cancel")
	}
	if err := client.Cancel(ctx, session.SessionID); err != nil {
		t.Fatalf("session/cancel: %v", err)
	}
	select {
	case res := <-cancelled:
		t.Logf("cancel stopReason=%s", res.StopReason)
		if res.StopReason != StopCancelled {
			t.Fatalf("stopReason after cancel = %q, want cancelled", res.StopReason)
		}
	case err := <-errc:
		t.Fatalf("prompt during cancel: %v", err)
	case <-time.After(3 * time.Minute):
		t.Fatal("cancelled prompt did not return")
	}
	after := newSpikeRecorder()
	turnCtx, turnCancel = context.WithTimeout(base, 5*time.Minute)
	if _, err := client.Prompt(turnCtx, session.SessionID, []ContentBlock{TextBlock("Reply with exactly: alive")}, after.sink); err != nil {
		turnCancel()
		t.Fatalf("prompt after cancel: %v", err)
	}
	turnCancel()
	waitText(t, after, "alive", 3*time.Minute)
	t.Logf("reused after cancel: %q", mustText(after))

	// 4. Attach a stdio MCP server declared through session/new.
	mcpSession, err := client.NewSession(ctx, "/workspace", []MCPServer{{
		Name:    "swarmjet",
		Command: "python3",
		Args:    []string{"/spike/mcp_server.py"},
	}})
	if err != nil {
		t.Fatalf("session/new with MCP server: %v", err)
	}
	mcpRec := newSpikeRecorder()
	turnCtx, turnCancel = context.WithTimeout(base, 5*time.Minute)
	if _, err := client.Prompt(turnCtx, mcpSession.SessionID, []ContentBlock{TextBlock("Call the swarmjet_swarmjet_ping tool and report its exact output.")}, mcpRec.sink); err != nil {
		turnCancel()
		t.Fatalf("mcp prompt: %v", err)
	}
	turnCancel()
	mcpText, mcpTools := mcpRec.snapshot()
	t.Logf("mcp text=%q tools=%d", mcpText, len(mcpTools))
	if !strings.Contains(mcpText+mcpToolOutput(mcpTools), "MCP_PONG_FROM_SWARMJET") {
		t.Fatalf("MCP tool did not return its marker; text=%q tools=%v", mcpText, mcpToolOutput(mcpTools))
	}

	// 5. Permission requests reach the client and follow the platform policy.
	permClient := s.connect(ctx, map[string]any{"permission": map[string]any{"bash": "ask"}},
		WithPermissionHandler(func(_ context.Context, req PermissionRequest) (PermissionOutcome, error) {
			t.Logf("permission requested: kind=%s title=%q options=%d", req.ToolCall.Kind, req.ToolCall.Title, len(req.Options))
			if opt, ok := req.Select(PermissionAllowOnce, PermissionAllowAlways); ok {
				return Selected(opt), nil
			}
			return Cancelled(), nil
		}))
	if _, err := permClient.Initialize(ctx); err != nil {
		t.Fatalf("initialize permission client: %v", err)
	}
	permSession, err := permClient.NewSession(ctx, "/workspace", nil)
	if err != nil {
		t.Fatalf("session/new permission client: %v", err)
	}
	permRec := newSpikeRecorder()
	turnCtx, turnCancel = context.WithTimeout(base, 5*time.Minute)
	if _, err := permClient.Prompt(turnCtx, permSession.SessionID, []ContentBlock{TextBlock("Use the bash tool to run exactly: echo perm-spike-check. Then report the output.")}, permRec.sink); err != nil {
		turnCancel()
		t.Fatalf("permission prompt: %v", err)
	}
	turnCancel()
	waitText(t, permRec, "perm-spike-check", 4*time.Minute)
	t.Logf("permission-approved turn text: %q", mustText(permRec))
	_ = permClient.Close()

	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 6. Container restart: a new container with the same volume must load the
	// session and remember the conversation.
	s.stopContainer()
	s.startContainer()
	restartCtx, restartCancel := context.WithTimeout(base, 10*time.Minute)
	defer restartCancel()
	restarted := s.connect(restartCtx, nil)
	if _, err := restarted.Initialize(restartCtx); err != nil {
		t.Fatalf("initialize after restart: %v", err)
	}
	replay := newSpikeRecorder()
	if _, err := restarted.LoadSession(restartCtx, session.SessionID, "/workspace", nil, replay.sink); err != nil {
		t.Fatalf("session/load after restart: %v", err)
	}
	t.Logf("replayed after restart: %q", mustText(replay))
	recall := newSpikeRecorder()
	turnCtx, turnCancel = context.WithTimeout(base, 5*time.Minute)
	if _, err := restarted.Prompt(turnCtx, session.SessionID, []ContentBlock{TextBlock("What was the codeword? Reply with just the word.")}, recall.sink); err != nil {
		turnCancel()
		t.Fatalf("prompt after restart: %v", err)
	}
	turnCancel()
	waitText(t, recall, "BUMBLEBEE", 4*time.Minute)
	t.Logf("memory survived restart: %q", mustText(recall))
	_ = restarted.Close()
}

func sessionModel(session NewSessionResult) string {
	for _, o := range session.ConfigOptions {
		if o.ID == "model" {
			return o.CurrentValue
		}
	}
	return ""
}

func mustText(r *spikeRecorder) string {
	text, _ := r.snapshot()
	return text
}

// mcpToolOutput flattens tool_call_update output so the test can assert on it.
func mcpToolOutput(tools []SessionUpdate) string {
	var b strings.Builder
	for _, u := range tools {
		if len(u.RawOutput) > 0 {
			b.Write(u.RawOutput)
		}
		b.WriteString(u.Text())
	}
	return b.String()
}
