package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
)

// TestCellRuntimeE2E drives a real Cell with a real OpenCode process. It is
// skipped unless SWARMJET_CELL_E2E=1 because it needs Docker, the agent image
// and network access to a model provider.
//
//	SWARMJET_CELL_E2E=1 go test ./internal/cell/docker/ -run TestCellRuntimeE2E -v -timeout 40m
//
// Optional environment:
//
//	SWARMJET_CELL_E2E_DOCKER  docker binary (default docker)
//	SWARMJET_CELL_E2E_IMAGE   image tag (default swarmjet-agent:local)
//	SWARMJET_CELL_E2E_MODEL   provider/model (default opencode/big-pickle, free)
func TestCellRuntimeE2E(t *testing.T) {
	if os.Getenv("SWARMJET_CELL_E2E") != "1" {
		t.Skip("set SWARMJET_CELL_E2E=1 to run the real Docker/OpenCode Cell test")
	}
	dockerBinary := envOr("SWARMJET_CELL_E2E_DOCKER", "docker")
	image := envOr("SWARMJET_CELL_E2E_IMAGE", "swarmjet-agent:local")
	model := envOr("SWARMJET_CELL_E2E_MODEL", "opencode/big-pickle")
	if _, err := exec.LookPath(dockerBinary); err != nil {
		t.Skipf("%s not found: %v", dockerBinary, err)
	}
	if out, err := exec.Command(dockerBinary, "image", "inspect", image).CombinedOutput(); err != nil {
		t.Skipf("image %s not available: %v\n%s", image, err, out)
	}

	bare := initBareRepo(t)
	agentID := fmt.Sprintf("e2e-agent-%d", time.Now().UnixNano())
	spec := cell.Spec{
		AgentID:    agentID,
		HiveID:     "e2e-hive",
		Repository: "acme/app",
		Branch:     "swarm/e2e/" + agentID,
		BaseBranch: "main",
		CLI:        "opencode",
		Model:      model,
	}
	rt := NewRuntime(t.TempDir())
	rt.DockerBinary = dockerBinary
	rt.Config.Image = image
	rt.Git = &Git{Remote: func(string) string { return bare }}
	rt.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
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

	workspace := WorkspaceDir(rt.DataDir, agentID)
	// The CLI runs as the platform uid; give it a commit identity so the prompt
	// only has to create the file and commit it.
	runGit(t, workspace, "config", "user.name", wipAuthorName)
	runGit(t, workspace, "config", "user.email", wipAuthorEmail)

	// First prove that streamed text reaches the session sink with a tiny turn.
	ping := runTurn(t, session, "Reply with exactly the word: pong")
	t.Logf("ping text: %q", ping)
	if !strings.Contains(strings.ToLower(ping), "pong") {
		t.Fatalf("streamed text was not delivered, got %q", ping)
	}

	firstText, updates := runTurnWithUpdates(t, session,
		"Use the bash tool (not the write tool) to run exactly this command: "+
			"cd /workspace && printf 'BUMBLEBEE' > e2e.txt && git add -A && git commit -m e2e-commit. "+
			"Then reply with DONE.")
	t.Logf("first turn text: %q", firstText)
	for _, update := range updates {
		t.Logf("update kind=%s title=%q text=%q", update.Kind, update.Title, truncateText(update.Text, 200))
	}
	content, err := os.ReadFile(filepath.Join(workspace, "e2e.txt"))
	if err != nil {
		t.Fatalf("agent did not create e2e.txt: %v", err)
	}
	if !strings.Contains(strings.ToUpper(string(content)), "BUMBLEBEE") {
		t.Fatalf("e2e.txt = %q", content)
	}
	if subject, ok := tryGit(t, workspace, "log", "-1", "--format=%s"); !ok || subject != "e2e-commit" {
		t.Fatalf("expected an e2e-commit commit, got %q (%v)", subject, ok)
	}

	// Park the Cell, then resume the same CLI session against the same volume.
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
	secondText := runTurn(t, resumed,
		"What exact word did I ask you to put in e2e.txt? Reply with just the word.")
	if !strings.Contains(strings.ToUpper(secondText), "BUMBLEBEE") {
		t.Fatalf("session memory did not survive restart: %q", secondText)
	}

	if err := rt.Destroy(ctx, agentID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace still exists after Destroy: %v", err)
	}
	if out, err := exec.Command(dockerBinary, "inspect", containerName(agentID)).CombinedOutput(); err == nil {
		t.Fatalf("container still exists after Destroy:\n%s", out)
	}
	if out, err := exec.Command(dockerBinary, "volume", "inspect", volumeName(agentID)).CombinedOutput(); err == nil {
		t.Fatalf("volume still exists after Destroy:\n%s", out)
	}
}

func runTurn(t *testing.T, session cell.Session, prompt string) string {
	t.Helper()
	text, _ := runTurnWithUpdates(t, session, prompt)
	return text
}

func runTurnWithUpdates(t *testing.T, session cell.Session, prompt string) (string, []cell.Update) {
	t.Helper()
	var text strings.Builder
	var updates []cell.Update
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := session.Prompt(ctx, prompt, func(update cell.Update) {
		updates = append(updates, update)
		if update.Kind == cell.UpdateText {
			text.WriteString(update.Text)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	t.Logf("turn stopReason=%s tokens=%d/%d updates=%d", result.StopReason, result.InputTokens, result.OutputTokens, len(updates))
	return text.String(), updates
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
