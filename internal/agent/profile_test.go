package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func writeProfileFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func masterHome(r *Runtime) string {
	return filepath.Join(r.DataDir, "runtime-profiles", "default", "home")
}
func TestProfileCopiesOnlyAuthAndConfigIntoPrivateRoleScopes(t *testing.T) {
	r, f, req := setup(t)
	master := masterHome(r)
	auth := `{"provider":{"type":"api","key":"profile-secret"}}`
	writeProfileFixture(t, filepath.Join(master, ".local/share/opencode/auth.json"), auth)
	writeProfileFixture(t, filepath.Join(master, ".local/share/opencode/opencode.db"), "must not be copied")
	writeProfileFixture(t, filepath.Join(master, ".config/opencode/opencode.json"), `{"theme":"system"}`)
	writeProfileFixture(t, filepath.Join(master, ".config/opencode/plugin.js"), "must not be copied")
	for _, role := range []string{"dev", "review", "merge"} {
		req.Role = role
		req.ExecutionID = "profile-" + role
		if _, err := r.Start(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		scope := roleScope(role)
		authPath := filepath.Join(r.DataDir, "opencode", req.TaskID, scope, "opencode/auth.json")
		b, err := os.ReadFile(authPath)
		if err != nil || string(b) != auth {
			t.Fatal("missing role auth", role, err)
		}
		info, _ := os.Stat(authPath)
		if info.Mode().Perm() != 0600 {
			t.Fatal("auth mode", info.Mode())
		}
		for _, path := range []string{filepath.Join(r.DataDir, "opencode", req.TaskID, scope, "opencode/opencode.db"), filepath.Join(r.DataDir, "opencode", req.TaskID, scope+"-config", "opencode/plugin.js")} {
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("unexpected profile file copied", path)
			}
		}
		recordData, _ := os.ReadFile(r.path(req.ExecutionID))
		if strings.Contains(string(recordData), "profile-secret") {
			t.Fatal("credential in execution record")
		}
	}
	for _, call := range f.calls {
		if call[0] != "create" {
			continue
		}
		for i, arg := range call {
			if strings.Contains(arg, master) || strings.Contains(arg, "profile-secret") {
				t.Fatal("master or secret in Docker argv", arg)
			}
			if arg == "--mount" && i+1 < len(call) && strings.Contains(call[i+1], "-auth-snapshots") {
				t.Fatal("redaction snapshot exposed to agent")
			}
		}
	}
	reviewPath := filepath.Join(r.DataDir, "opencode", req.TaskID, "review", "opencode/auth.json")
	writeProfileFixture(t, reviewPath, `{"provider":{"access":"refreshed-review-token"}}`)
	req.ExecutionID = "merge-again"
	req.Role = "merge"
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(reviewPath)
	if !strings.Contains(string(b), "refreshed-review-token") {
		t.Fatal("merge did not retain review credentials")
	}
}
func TestProfilePreservesTaskRefreshAndSnapshotsMasterOnlyForNewTasks(t *testing.T) {
	r, _, req := setup(t)
	profileAuth := filepath.Join(masterHome(r), ".local/share/opencode/auth.json")
	profileConfig := filepath.Join(masterHome(r), ".config/opencode/opencode.json")
	writeProfileFixture(t, profileAuth, `{"p":{"key":"original"}}`)
	writeProfileFixture(t, profileConfig, `{"model":"provider/original"}`)
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	taskAuth := filepath.Join(r.DataDir, "opencode", req.TaskID, "dev/opencode/auth.json")
	writeProfileFixture(t, taskAuth, `{"p":{"access":"refreshed-task"}}`)
	writeProfileFixture(t, profileAuth, `{"p":{"key":"new-master"}}`)
	writeProfileFixture(t, profileConfig, `{"model":"provider/new"}`)
	req.ExecutionID = "next-execution"
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(taskAuth)
	if !strings.Contains(string(b), "refreshed-task") {
		t.Fatal("task auth overwritten")
	}
	configPath := filepath.Join(r.DataDir, "opencode", req.TaskID, "dev-config/opencode/opencode.json")
	b, _ = os.ReadFile(configPath)
	if !strings.Contains(string(b), "provider/original") {
		t.Fatal("task config mutated")
	}
	req.TaskID = "new-task"
	req.ExecutionID = "new-task-execution"
	req.Workspace = filepath.Join(r.DataDir, "task-sources", req.TaskID, "workspace")
	os.MkdirAll(req.Workspace, 0700)
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(r.DataDir, "opencode", req.TaskID, "dev/opencode/auth.json"))
	if !strings.Contains(string(b), "new-master") {
		t.Fatal("new task did not inherit updated master")
	}
}
func TestEmptyProfileCanBeSeededLaterAndLegacyConfigIsSnapshot(t *testing.T) {
	r, _, req := setup(t)
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal("environment-only setup failed", err)
	}
	writeProfileFixture(t, filepath.Join(masterHome(r), ".local/share/opencode/auth.json"), `{"p":{"key":"later-login"}}`)
	writeProfileFixture(t, filepath.Join(r.DataDir, "opencode-config/opencode/opencode.jsonc"), "{ // legacy\n \"theme\": \"light\"\n}")
	req.ExecutionID = "later"
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(r.DataDir, "opencode", req.TaskID, "dev/opencode/auth.json"))
	if !strings.Contains(string(b), "later-login") {
		t.Fatal("late login not inherited")
	}
	snapshot := filepath.Join(r.DataDir, "opencode", req.TaskID, "dev-config/opencode/opencode.jsonc")
	b, _ = os.ReadFile(snapshot)
	if !strings.Contains(string(b), "legacy") {
		t.Fatal("legacy config not copied")
	}
	writeProfileFixture(t, filepath.Join(masterHome(r), ".config/opencode/opencode.json"), `{"model":"provider/new"}`)
	req.ExecutionID = "frozen-config"
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(snapshot), "opencode.json")); !os.IsNotExist(err) {
		t.Fatal("existing role config gained new master config")
	}
}
func TestProfileConfigurationWinsOverLegacy(t *testing.T) {
	r, _, req := setup(t)
	writeProfileFixture(t, filepath.Join(masterHome(r), ".config/opencode/opencode.json"), `{"theme":"master"}`)
	writeProfileFixture(t, filepath.Join(r.DataDir, "opencode-config/opencode/opencode.jsonc"), `{"theme":"legacy"}`)
	if _, err := r.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.DataDir, "opencode", req.TaskID, "dev-config/opencode/opencode.jsonc")); !os.IsNotExist(err) {
		t.Fatal("profile and legacy config were mixed")
	}
}
func TestAuthSecretsRedactLogsAndResultsAcrossRefresh(t *testing.T) {
	r, f, req := setup(t)
	writeProfileFixture(t, filepath.Join(masterHome(r), ".local/share/opencode/auth.json"), `{"api":{"key":"original-secret"},"oauth":{"access":"access-secret","refresh_token":"refresh-secret"}}`)
	ex, err := r.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := r.read(ex.ID)
	writeProfileFixture(t, rec.AuthPath, `{"oauth":{"access":"renewed-secret"}}`)
	f.logs = "{\"sessionID\":\"ses_private\",\"text\":\"original-secret access-secret refresh-secret renewed-secret\"}\n"
	c := f.containers[ex.ContainerID]
	c.State.Running = false
	c.State.Status = "exited"
	f.containers[ex.ContainerID] = c
	result := domain.AgentResult{Outcome: "changed", Summary: "original-secret renewed-secret", Assumptions: []string{"access-secret"}, Findings: []domain.Finding{{Message: "refresh-secret"}}}
	b, _ := json.Marshal(result)
	writeProfileFixture(t, rec.ResultPath, string(b))
	ex, err = r.Inspect(context.Background(), req.ExecutionID)
	if err != nil || ex.Status != "succeeded" || ex.SessionID != "ses_private" {
		t.Fatal(ex, err)
	}
	logs, _ := os.ReadFile(ex.LogPath)
	metadata, _ := os.ReadFile(r.path(ex.ID))
	artifact, _ := os.ReadFile(rec.ResultPath)
	for _, secret := range []string{"original-secret", "access-secret", "refresh-secret", "renewed-secret"} {
		if strings.Contains(string(logs), secret) || strings.Contains(string(metadata), secret) || strings.Contains(string(artifact), secret) {
			t.Fatal("credential leaked", secret)
		}
	}
}
func TestUnsafeProfileFilesFailWithoutExposingCredentials(t *testing.T) {
	for _, scenario := range []string{"auth-symlink", "parent-symlink", "auth-large", "config-large", "malformed-auth"} {
		t.Run(scenario, func(t *testing.T) {
			r, f, req := setup(t)
			auth := filepath.Join(masterHome(r), ".local/share/opencode/auth.json")
			switch scenario {
			case "auth-symlink":
				outside := filepath.Join(t.TempDir(), "outside")
				writeProfileFixture(t, outside, `{"p":{"key":"do-not-leak"}}`)
				os.MkdirAll(filepath.Dir(auth), 0700)
				os.Symlink(outside, auth)
			case "parent-symlink":
				os.MkdirAll(filepath.Dir(masterHome(r)), 0700)
				os.Symlink(t.TempDir(), masterHome(r))
			case "auth-large":
				writeProfileFixture(t, auth, strings.Repeat("x", profileFileLimit+1))
			case "config-large":
				writeProfileFixture(t, filepath.Join(masterHome(r), ".config/opencode/opencode.json"), strings.Repeat("x", profileFileLimit+1))
			case "malformed-auth":
				writeProfileFixture(t, auth, "do-not-leak")
			}
			_, err := r.Start(context.Background(), req)
			if err == nil || strings.Contains(err.Error(), "do-not-leak") {
				t.Fatal("unsafe profile accepted or exposed", err)
			}
			if f.creates != 0 {
				t.Fatal("container created despite invalid profile")
			}
		})
	}
}
func TestUnreadableAuthFailsClosedBeforeSavingLogs(t *testing.T) {
	for _, scenario := range []string{"deleted", "symlink", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			r, f, req := setup(t)
			writeProfileFixture(t, filepath.Join(masterHome(r), ".local/share/opencode/auth.json"), `{"p":{"key":"private-token"}}`)
			ex, err := r.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			rec, _ := r.read(ex.ID)
			os.Remove(rec.AuthPath)
			switch scenario {
			case "symlink":
				os.Symlink("/etc/passwd", rec.AuthPath)
			case "malformed":
				writeProfileFixture(t, rec.AuthPath, "private-token")
			}
			f.logs = "private-token\n"
			if _, err = r.Inspect(context.Background(), ex.ID); err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatal("redaction failed open", err)
			}
			if _, err = os.Stat(ex.LogPath); !os.IsNotExist(err) {
				t.Fatal("unsafe log saved")
			}
		})
	}
}
