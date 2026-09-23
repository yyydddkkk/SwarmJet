package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarmjet/internal/agentcli"
)

func TestCLIAdaptersLifecycle(t *testing.T) {
	events := map[string]string{
		"opencode": `{"type":"text","sessionID":"session-exact"}`,
		"codex":    `{"type":"thread.started","thread_id":"session-exact"}`,
		"dst":      `{"type":"session","sessionId":"session-exact"}`,
		"agy":      `{"event":"init","conversation_id":"session-exact"}`,
		"claude":   `{"type":"system","subtype":"init","session_id":"session-exact"}`,
	}
	for _, spec := range agentcli.Catalog() {
		t.Run(spec.ID, func(t *testing.T) {
			r, f, req := setup(t)
			req.Config.Adapter, req.Config.Model, req.Config.UseCLIDefaults = spec.ID, "", true
			ex, err := r.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			var create []string
			for _, call := range f.calls {
				if call[0] == "create" {
					create = call
				}
			}
			joined := strings.Join(create, " ")
			if !strings.Contains(joined, "--entrypoint "+spec.Executable+" test-image") || strings.Contains(joined, " -m ") || strings.Contains(joined, " --model ") || strings.Contains(joined, "secret task prompt") {
				t.Fatal(create)
			}
			f.logs = events[spec.ID] + "\n"
			ex, err = r.Inspect(context.Background(), ex.ID)
			if err != nil || ex.SessionID != "session-exact" {
				t.Fatal(ex, err)
			}
			// Retrying Start adopts the existing container, without another launch.
			if _, err = r.Start(context.Background(), req); err != nil || f.creates != 1 {
				t.Fatal(err, f.creates)
			}
			if err = r.Cancel(context.Background(), ex.ID); err != nil {
				t.Fatal(err)
			}
			req.ExecutionID = "resume-run"
			req.SessionID = ex.SessionID
			if _, err = r.Start(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			last := ""
			for _, call := range f.calls {
				if call[0] == "create" {
					last = strings.Join(call, " ")
				}
			}
			if !strings.Contains(last, "session-exact") || strings.Contains(last, "--last") || strings.Contains(last, "--continue") {
				t.Fatal(last)
			}
			rec, err := r.read(req.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(rec.ResultPath, []byte(`{"outcome":"no_change","summary":"done","assumptions":[],"findings":[],"needsHelp":""}`), 0600); err != nil {
				t.Fatal(err)
			}
			c := f.containers[rec.Execution.ContainerID]
			c.State.Running = false
			c.State.Status = "exited"
			c.State.ExitCode = 0
			f.containers[rec.Execution.ContainerID] = c
			finished, err := r.Inspect(context.Background(), req.ExecutionID)
			if err != nil || finished.Status != "succeeded" {
				t.Fatal(finished, err)
			}
		})
	}
}

func TestCLIProfileSnapshotAcrossRolesAndTasks(t *testing.T) {
	r, _, req := setup(t)
	req.Config.Adapter = "codex"
	req.Config.UseCLIDefaults = true
	req.Config.Model = ""
	master := filepath.Join(r.DataDir, "runtime-profiles/default/home/.codex")
	if err := os.MkdirAll(master, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(master, "config.toml"), "model = \"first-model\"\n")
	write(filepath.Join(master, "auth.json"), `{"tokens":{"access_token":"initial-private-token"}}`)
	write(filepath.Join(master, "history.jsonl"), "unrelated history")
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	dev, _ := r.read(req.ExecutionID)
	write(filepath.Join(dev.ProfileHome, ".codex/auth.json"), `{"tokens":{"access_token":"refreshed-private-token"}}`)
	write(filepath.Join(master, "config.toml"), "model = \"new-model\"\n")
	req.ExecutionID = "review-run"
	req.Role = "review"
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	review, _ := r.read(req.ExecutionID)
	b, _ := os.ReadFile(filepath.Join(review.ProfileHome, ".codex/config.toml"))
	if !strings.Contains(string(b), "first-model") || review.ProfileHome == dev.ProfileHome {
		t.Fatal("review adopted later config or shared developer session")
	}
	if _, err := os.Stat(filepath.Join(dev.ProfileHome, ".codex/history.jsonl")); !os.IsNotExist(err) {
		t.Fatal("copied master history")
	}
	secrets, err := r.profileSecrets(dev)
	if err != nil {
		t.Fatal(err)
	}
	clean, _ := ParseJSONL([]byte("initial-private-token refreshed-private-token"), secrets)
	if strings.Contains(string(clean), "private-token") {
		t.Fatal("auth rotation leaked credentials")
	}
	req.TaskID = "new-task"
	req.ExecutionID = "new-run"
	req.Role = "dev"
	req.Workspace = filepath.Join(r.DataDir, "task-sources/new-task/work")
	if err = os.MkdirAll(req.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	newRun, _ := r.read(req.ExecutionID)
	b, _ = os.ReadFile(filepath.Join(newRun.ProfileHome, ".codex/config.toml"))
	if !strings.Contains(string(b), "new-model") {
		t.Fatal("new task did not adopt current configuration")
	}
}

func TestCLIProfileRejectsSymlink(t *testing.T) {
	r, _, req := setup(t)
	req.Config.Adapter = "claude"
	req.Config.UseCLIDefaults = true
	master := filepath.Join(r.DataDir, "runtime-profiles/default/home/.claude")
	if err := os.MkdirAll(master, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(master, "settings.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(context.Background(), req); err == nil {
		t.Fatal("accepted unsafe profile file")
	}
}

func TestCLITerminalFailureCannotSucceedWithArtifact(t *testing.T) {
	for id, event := range map[string]string{
		"codex":  `{"type":"turn.failed","error":{"message":"private provider error"}}`,
		"claude": `{"type":"result","is_error":true}`,
		"agy":    `{"event":"result","result":{"status":"WAITING"}}`,
	} {
		t.Run(id, func(t *testing.T) {
			r, f, req := setup(t)
			req.Config.Adapter = id
			req.Config.UseCLIDefaults = true
			if _, err := r.Start(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			rec, _ := r.read(req.ExecutionID)
			if err := os.WriteFile(rec.ResultPath, []byte(`{"outcome":"no_change","summary":"early artifact","assumptions":[],"findings":[],"needsHelp":""}`), 0600); err != nil {
				t.Fatal(err)
			}
			f.logs = event
			c := f.containers[rec.Execution.ContainerID]
			c.State.Running = false
			c.State.Status = "exited"
			c.State.ExitCode = 0
			f.containers[rec.Execution.ContainerID] = c
			ex, err := r.Inspect(context.Background(), req.ExecutionID)
			if err != nil || ex.Status != "failed" || ex.Result != nil || strings.Contains(ex.FailureReason, "private provider error") {
				t.Fatal(ex, err)
			}
		})
	}
}

func TestCLIConfigSecretsAcrossFormats(t *testing.T) {
	for name, data := range map[string]string{
		"config.toml":    "model = \"visible-model\"\nexperimental_bearer_token = \"private-config-token\"\n",
		"settings.jsonc": "{ // comment\n\"model\":\"visible-model\",\"apiKey\":\"private-config-token\",}",
		"settings.yaml":  "model: visible-model\nprovider:\n  apiKey: private-config-token\n",
		".env":           "DEEPSEEK_API_KEY=private-config-token # deployment credential\n",
	} {
		t.Run(name, func(t *testing.T) {
			clean, _ := ParseJSONL([]byte("visible-model private-config-token"), fileSecrets(name, []byte(data)))
			if string(clean) != "visible-model [REDACTED]" {
				t.Fatalf("incorrect redaction: %q", clean)
			}
		})
	}
}
