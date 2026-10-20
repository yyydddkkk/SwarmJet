package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/omp"
)

// TestCellRuntimeE2E drives a real Cell with a real `omp acp` process. It is
// skipped unless SWARMJET_CELL_E2E=1 because it needs Docker and the agent
// image. When SWARMJET_CELL_E2E_LLM_URL is unset, a local fake pi-native
// gateway returns fixed replies, so the test does not need a model credential.
//
//	SWARMJET_CELL_E2E=1 go test ./internal/cell/docker/ -run TestCellRuntimeE2E -v -timeout 30m
//
// Optional environment:
//
//	SWARMJET_CELL_E2E_DOCKER    docker binary (default docker)
//	SWARMJET_CELL_E2E_IMAGE     image tag (default swarmjet-agent:local)
//	SWARMJET_CELL_E2E_MODEL     provider/model (default deepseek/deepseek-chat)
//	SWARMJET_CELL_E2E_NETWORK   Cell network (default host, so 127.0.0.1 reaches the fake)
//	SWARMJET_CELL_E2E_LLM_URL   external fake/real gateway base URL
func TestCellRuntimeE2E(t *testing.T) {
	if os.Getenv("SWARMJET_CELL_E2E") != "1" {
		t.Skip("set SWARMJET_CELL_E2E=1 to run the real Docker/omp Cell test")
	}
	dockerBinary := envOr("SWARMJET_CELL_E2E_DOCKER", "docker")
	image := envOr("SWARMJET_CELL_E2E_IMAGE", "swarmjet-agent:local")
	model := envOr("SWARMJET_CELL_E2E_MODEL", "deepseek/deepseek-chat")
	network := envOr("SWARMJET_CELL_E2E_NETWORK", "host")
	if _, err := exec.LookPath(dockerBinary); err != nil {
		t.Skipf("%s not found: %v", dockerBinary, err)
	}
	if out, err := exec.Command(dockerBinary, "image", "inspect", image).CombinedOutput(); err != nil {
		t.Skipf("image %s not available: %v\n%s", image, err, out)
	}

	llmURL := os.Getenv("SWARMJET_CELL_E2E_LLM_URL")
	fake := &fakeGateway{started: make(chan struct{}, 1)}
	var server *httptest.Server
	if llmURL == "" {
		server = httptest.NewServer(fake)
		defer server.Close()
		llmURL = server.URL
	}

	files, err := omp.ConfigFiles(omp.Options{Model: model, LLMBaseURL: llmURL})
	if err != nil {
		t.Fatal(err)
	}
	bare := initBareRepo(t)
	agentID := fmt.Sprintf("e2e-agent-%d", time.Now().UnixNano())
	spec := cell.Spec{
		AgentID:     agentID,
		HiveID:      "e2e-hive",
		Repository:  "acme/app",
		Branch:      "swarm/e2e/" + agentID,
		BaseBranch:  "main",
		CLI:         "omp",
		Model:       model,
		ConfigFiles: files,
		LLMBaseURL:  llmURL,
		LLMToken:    "e2e-agent-token",
	}
	rt := NewRuntime(t.TempDir())
	rt.DockerBinary = dockerBinary
	rt.Config.Image = image
	rt.Config.DockerNetwork = network
	rt.Git = &Git{Remote: func(string) string { return bare }}
	rt.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = rt.Destroy(cleanupCtx, agentID)
	})

	session, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Logf("started session %s", session.Ref())

	first := runTurn(t, session, "Reply with exactly the word: pong")
	if !strings.Contains(strings.ToLower(first), "pong") {
		t.Fatalf("streamed text was not delivered, got %q", first)
	}

	// Park the Cell and load the same session from the persistent Home volume.
	if err := rt.Stop(ctx, agentID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	spec.SessionRef = session.Ref()
	resumed, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if resumed.Ref() != spec.SessionRef {
		t.Fatalf("session was not preserved: got %q, want %q", resumed.Ref(), spec.SessionRef)
	}
	second := runTurn(t, resumed, "What word did you say before? Reply with just that word.")
	if !strings.Contains(strings.ToLower(second), "pong") {
		t.Fatalf("session history did not survive restart: %q", second)
	}

	// Cancel an in-flight turn and keep the session usable afterwards.
	done := make(chan struct{})
	var cancelResult cell.TurnResult
	var cancelErr error
	go func() {
		defer close(done)
		cancelResult, cancelErr = resumed.Prompt(ctx, "Please cancel-me now.", func(cell.Update) {})
	}()
	select {
	case <-fake.started:
	case <-time.After(60 * time.Second):
		t.Fatalf("fake gateway never saw the cancel turn")
	}
	if err := resumed.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("cancelled turn did not return")
	}
	if cancelErr != nil {
		t.Fatalf("cancelled Prompt error: %v", cancelErr)
	}
	if cancelResult.StopReason != "cancelled" {
		t.Fatalf("stop reason = %q, want cancelled", cancelResult.StopReason)
	}
	after := runTurn(t, resumed, "Reply with exactly the word: pong")
	if !strings.Contains(strings.ToLower(after), "pong") {
		t.Fatalf("session was not usable after cancel: %q", after)
	}
}

// fakeGateway speaks the pi-native SSE protocol omp sends to an auth gateway.
// It deliberately answers from the context so session reloads are observable.
type fakeGateway struct {
	started chan struct{}
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/pi/stream" {
		http.NotFound(w, r)
		return
	}
	var body struct {
		ModelID string `json:"modelId"`
		Context struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		} `json:"context"`
	}
	raw := make([]byte, 0, 1<<20)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			break
		}
	}
	_ = json.Unmarshal(raw, &body)
	if !strings.Contains(body.ModelID, "/") {
		http.Error(w, "bad model id", http.StatusBadRequest)
		return
	}
	// Only the newest user message is an instruction. The rest of the body is
	// conversation history and must not be mistaken for a new command.
	lastPrompt := ""
	historyHasPong := false
	for _, message := range body.Context.Messages {
		text := contentText(message.Content)
		switch message.Role {
		case "user":
			lastPrompt = text
		case "assistant":
			if strings.Contains(text, "pong") {
				historyHasPong = true
			}
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	if strings.Contains(lastPrompt, "cancel-me") {
		g.started <- struct{}{}
		writeEvent(w, map[string]any{
			"type":         "text_delta",
			"contentIndex": 0,
			"delta":        "working",
			"partial":      assistant("working"),
		})
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
		return
	}

	answer := "pong"
	if strings.Contains(lastPrompt, "What word did you say before") && !historyHasPong {
		// The replayed history is part of the request body; if it is absent,
		// answer something that makes the test fail clearly.
		answer = "no-history"
	}
	writeEvent(w, map[string]any{"type": "start", "partial": assistant("")})
	writeEvent(w, map[string]any{
		"type":         "text_start",
		"contentIndex": 0,
		"partial":      assistant(""),
	})
	writeEvent(w, map[string]any{
		"type":         "text_delta",
		"contentIndex": 0,
		"delta":        answer,
		"partial":      assistant(answer),
	})
	writeEvent(w, map[string]any{
		"type":         "text_end",
		"contentIndex": 0,
		"content":      answer,
		"partial":      assistant(answer),
	})
	writeEvent(w, map[string]any{"type": "done", "reason": "stop", "message": assistant(answer)})
	if flusher != nil {
		flusher.Flush()
	}
}

func writeEvent(w http.ResponseWriter, v any) {
	payload, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}

// contentText extracts text blocks from a pi-native message content value.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Type == "text" {
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	var text string
	_ = json.Unmarshal(raw, &text)
	return text
}

func assistant(text string) map[string]any {
	return map[string]any{
		"role":     "assistant",
		"content":  []map[string]any{{"type": "text", "text": text}},
		"api":      "openai-completions",
		"provider": "deepseek",
		"model":    "deepseek-chat",
		"usage": map[string]any{
			"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0},
		},
		"stopReason": "stop",
		"timestamp":  time.Now().UnixMilli(),
	}
}

// TestCellReadOnlyRevisionE2E proves the round-4 workspace contract against a
// real container and a real clone: the commit is checked out detached, and a
// read-only Cell can never publish.
func TestCellReadOnlyRevisionE2E(t *testing.T) {
	if os.Getenv("SWARMJET_CELL_E2E") != "1" {
		t.Skip("set SWARMJET_CELL_E2E=1 to run the real Docker revision checkout test")
	}
	dockerBinary := envOr("SWARMJET_CELL_E2E_DOCKER", "docker")
	image := envOr("SWARMJET_CELL_E2E_IMAGE", "swarmjet-agent:local")
	model := envOr("SWARMJET_CELL_E2E_MODEL", "deepseek/deepseek-chat")
	if _, err := exec.LookPath(dockerBinary); err != nil {
		t.Skipf("%s not found: %v", dockerBinary, err)
	}
	if out, err := exec.Command(dockerBinary, "image", "inspect", image).CombinedOutput(); err != nil {
		t.Skipf("image %s not available: %v\n%s", image, err, out)
	}

	bare := initBareRepo(t)
	sha := runGit(t, bare, "rev-parse", "refs/heads/main")
	agentID := fmt.Sprintf("e2e-verify-%d", time.Now().UnixNano())
	files, err := omp.ConfigFiles(omp.Options{Model: model, LLMBaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	spec := cell.Spec{
		AgentID:     agentID,
		HiveID:      "e2e-hive",
		Repository:  "acme/app",
		Branch:      "verify/" + agentID,
		BaseBranch:  "main",
		CLI:         "omp",
		Model:       model,
		Revision:    sha,
		ReadOnly:    true,
		ConfigFiles: files,
	}
	rt := NewRuntime(t.TempDir())
	rt.DockerBinary = dockerBinary
	rt.Config.Image = image
	rt.Git = &Git{Remote: func(string) string { return bare }}
	rt.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = rt.Destroy(cleanupCtx, agentID)
	})

	if _, err := rt.Start(ctx, spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	head := containerGit(t, rt, agentID, "rev-parse", "HEAD")
	if got := strings.TrimSpace(string(head.Stdout)); got != sha {
		t.Fatalf("HEAD = %q, want %q", got, sha)
	}
	detached, err := rt.cellRunner().Run(ctx, containerName(agentID), cellWorkdir,
		[]string{"git", "symbolic-ref", "--quiet", "--short", "HEAD"})
	if err != nil {
		t.Fatalf("symbolic-ref: %v", err)
	}
	if detached.ExitCode == 0 {
		t.Fatalf("HEAD is attached to %q, want a detached checkout", strings.TrimSpace(string(detached.Stdout)))
	}

	pub := &Publisher{DataDir: rt.DataDir, Cell: rt.cellRunner()}
	if _, err := pub.Publish(ctx, spec, "title", "body"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Publish on a read-only Cell: %v", err)
	}
}

func runTurn(t *testing.T, session cell.Session, prompt string) string {
	t.Helper()
	var text strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := session.Prompt(ctx, prompt, func(update cell.Update) {
		if update.Kind == cell.UpdateText {
			text.WriteString(update.Text)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	t.Logf("turn stopReason=%s tokens=%d/%d", result.StopReason, result.InputTokens, result.OutputTokens)
	return text.String()
}

func containerGit(t *testing.T, rt *Runtime, agentID string, args ...string) Result {
	t.Helper()
	res, err := rt.cellRunner().Run(context.Background(), containerName(agentID), cellWorkdir, append([]string{"git"}, args...))
	if err != nil {
		t.Fatalf("container git %v: %v", args, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("container git %v: exit %d: %s", args, res.ExitCode, res.Stderr)
	}
	return res
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
