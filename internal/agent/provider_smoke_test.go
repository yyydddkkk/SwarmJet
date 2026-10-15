package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Opt-in: makes a real, minimal model call using an explicitly supplied auth file.
func TestProviderSmoke(t *testing.T) {
	model, image, auth := os.Getenv("SWARMJET_SMOKE_MODEL"), os.Getenv("SWARMJET_SMOKE_IMAGE"), os.Getenv("SWARMJET_SMOKE_AUTH_FILE")
	if model == "" || image == "" || auth == "" {
		t.Skip("set SWARMJET_SMOKE_MODEL, SWARMJET_SMOKE_IMAGE and SWARMJET_SMOKE_AUTH_FILE for a real provider call")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	task := fmt.Sprintf("provider-smoke-%d", time.Now().UnixNano())
	r := New(root)
	ws := filepath.Join(root, "task-sources", task, "meta", ".worktrees", task)
	if e := os.MkdirAll(ws, 0700); e != nil {
		t.Fatal(e)
	}
	credentialDir := filepath.Join(root, "opencode", task, "dev", "opencode")
	if e := os.MkdirAll(credentialDir, 0700); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(auth)
	if e != nil {
		t.Fatal("cannot read explicitly supplied auth file")
	}
	if e = os.WriteFile(filepath.Join(credentialDir, "auth.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	req := domain.AgentRequest{ExecutionID: task, TaskID: task, Role: "dev", Workspace: ws, Config: domain.AgentConfig{Adapter: "opencode", Model: model, Image: image, TimeoutSeconds: 150}, Prompt: "This is a minimal authentication smoke test in an empty disposable workspace. Do not contact GitLab or modify any repository. Only write the required result JSON artifact. Set outcome to no_change and summary to provider-smoke-ready. Use empty assumptions and findings and empty needsHelp. No other work is needed."}
	t.Cleanup(func() {
		cleanup, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", "--volumes", "sj-agent-"+task).Run()
		_ = exec.CommandContext(cleanup, "docker", "run", "--rm", "--entrypoint", "chown", "--mount", "type=bind,src="+root+",dst=/cleanup", image, "-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "/cleanup").Run()
	})
	ex, e := r.Start(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	for ex.Status == "running" || ex.Status == "starting" {
		select {
		case <-ctx.Done():
			t.Fatal("provider smoke timed out")
		case <-time.After(time.Second):
		}
		ex, e = r.Inspect(ctx, task)
		if e != nil {
			t.Fatal(e)
		}
	}
	if ex.Status != "succeeded" || ex.SessionID == "" || ex.Result == nil || ex.Result.Outcome != "no_change" || ex.Result.Summary != "provider-smoke-ready" {
		if logs, err := os.ReadFile(ex.LogPath); err == nil {
			var authData map[string]struct {
				Key string `json:"key"`
			}
			_ = json.Unmarshal(b, &authData)
			secrets := []string{}
			for _, v := range authData {
				if v.Key != "" {
					secrets = append(secrets, v.Key)
				}
			}
			safe := redact(string(logs), secrets)
			if len(safe) > 5000 {
				safe = safe[len(safe)-5000:]
			}
			t.Logf("provider diagnostics: %s", safe)
		}
		t.Fatalf("real provider smoke failed: status=%s exit=%d sessionRecorded=%v structuredResult=%v", ex.Status, ex.ExitCode, ex.SessionID != "", ex.Result != nil)
	}
}
