package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/omp"
	"github.com/yyydddkkk/SwarmJet/internal/queen"
)

// TestQueenCellE2E uses a real omp ACP session and repository read tool. Only
// model inference is faked: a deterministic tool call must produce repository
// contents in the next model request before the fake will send its reply.
func TestQueenCellE2E(t *testing.T) {
	if os.Getenv("SWARMJET_CELL_E2E") != "1" {
		t.Skip("set SWARMJET_CELL_E2E=1 to run the real Queen Docker/omp test")
	}
	dockerBinary := envOr("SWARMJET_CELL_E2E_DOCKER", "docker")
	image := envOr("SWARMJET_CELL_E2E_IMAGE", "swarmjet-agent:local")
	if out, err := exec.Command(dockerBinary, "image", "inspect", image).CombinedOutput(); err != nil {
		t.Fatalf("Queen E2E requires image %s: %v\n%s", image, err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	header := queen.Header(ctx, nil, "e2e-queen-hive")
	gateway := &queenReadGateway{header: strings.TrimSpace(header)}
	server := httptest.NewServer(gateway)
	defer server.Close()
	model := "deepseek/deepseek-chat"
	files, err := omp.ConfigFiles(omp.Options{Model: model, LLMBaseURL: server.URL, RolePrompt: header})
	if err != nil {
		t.Fatal(err)
	}
	bare := initBareRepo(t)
	agentID := fmt.Sprintf("e2e-queen-%d", time.Now().UnixNano())
	spec := cell.Spec{
		AgentID: agentID, HiveID: "e2e-queen-hive", Repository: "acme/app",
		Branch: "queen/e2e-queen-hive", ReadOnly: true,
		CLI: "omp", Model: model, ConfigFiles: files,
		LLMBaseURL: server.URL, LLMToken: "e2e-queen-token",
	}
	rt := NewRuntime(t.TempDir())
	rt.DockerBinary = dockerBinary
	rt.Config.Image = image
	rt.Config.DockerNetwork = envOr("SWARMJET_CELL_E2E_NETWORK", "host")
	rt.Git = &Git{Remote: func(string) string { return bare }}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := rt.Destroy(cleanupCtx, agentID); err != nil {
			t.Errorf("Destroy: %v", err)
		}
	})
	session, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start Queen: %v", err)
	}
	if head := strings.TrimSpace(string(containerGit(t, rt, agentID, "rev-parse", "HEAD").Stdout)); head != runGit(t, bare, "rev-parse", "refs/heads/main") {
		t.Fatalf("Queen did not check out default branch: %s", head)
	}
	if branch := strings.TrimSpace(string(containerGit(t, rt, agentID, "branch", "--show-current").Stdout)); branch != spec.Branch {
		t.Fatalf("Queen branch = %q, want %q", branch, spec.Branch)
	}
	var reply strings.Builder
	result, err := session.Prompt(ctx, "Read README.md using the read tool, then reply with pong.", func(update cell.Update) {
		if update.Kind == cell.UpdateText {
			reply.WriteString(update.Text)
		}
	})
	if err != nil {
		t.Fatalf("Queen Prompt: %v", err)
	}
	gateway.mu.Lock()
	sawHeader, sawRead := gateway.sawHeader, gateway.sawRead
	gateway.mu.Unlock()
	if !sawHeader || !sawRead || !strings.Contains(reply.String(), "pong") {
		t.Fatalf("Queen turn: injected core=%t, repository read=%t, reply=%q, stop=%s", sawHeader, sawRead, reply.String(), result.StopReason)
	}
	pub := &Publisher{DataDir: rt.DataDir, Cell: rt.cellRunner()}
	if _, err := pub.Publish(ctx, spec, "Queen should never publish", ""); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Queen Publish: %v", err)
	}
	t.Logf("Queen session %s read README.md via omp and replied %q; read-only publication rejected", session.Ref(), reply.String())
}

type queenReadGateway struct {
	mu        sync.Mutex
	header    string
	sawHeader bool
	sawRead   bool
}

func (g *queenReadGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/pi/stream" {
		http.NotFound(w, r)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		Context struct {
			SystemPrompt json.RawMessage `json:"systemPrompt"`
			Messages     []struct {
				Role       string          `json:"role"`
				ToolCallID string          `json:"toolCallId"`
				IsError    bool            `json:"isError"`
				Content    json.RawMessage `json:"content"`
			} `json:"messages"`
		} `json:"context"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	var systemParts []string
	_ = json.Unmarshal(body.Context.SystemPrompt, &systemParts)
	g.sawHeader = g.sawHeader || strings.Contains(strings.Join(systemParts, "\n"), g.header) || strings.Contains(contentText(body.Context.SystemPrompt), g.header)
	sawResult := false
	for _, m := range body.Context.Messages {
		if m.Role == "toolResult" && m.ToolCallID == "queen-read-readme" {
			sawResult = true
		}
		if m.Role == "toolResult" && m.ToolCallID == "queen-read-readme" && !m.IsError && strings.Contains(contentText(m.Content), "hello") {
			g.sawRead = true
		}
	}
	g.mu.Unlock()
	if sawResult {
		// Reuse the existing fake's text streaming implementation for the reply.
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		(&fakeGateway{}).ServeHTTP(w, r)
		return
	}
	// A real omp read tool must run between these two inference requests.
	message := assistant("")
	message["content"] = []map[string]any{{"type": "toolCall", "id": "queen-read-readme", "name": "read", "arguments": map[string]any{"path": "README.md"}}}
	message["stopReason"] = "toolUse"
	w.Header().Set("Content-Type", "text/event-stream")
	writeEvent(w, map[string]any{"type": "start", "partial": assistant("")})
	writeEvent(w, map[string]any{"type": "toolcall_start", "contentIndex": 0, "partial": message})
	writeEvent(w, map[string]any{"type": "toolcall_end", "contentIndex": 0, "toolCall": message["content"].([]map[string]any)[0], "partial": message})
	writeEvent(w, map[string]any{"type": "done", "reason": "toolUse", "message": message})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
