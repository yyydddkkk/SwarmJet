package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"swarmjet/internal/domain"
)

func fixture(t *testing.T) (*Manager, domain.Workspace, domain.IntegrationConfig) {
	t.Helper()
	root := t.TempDir()
	b, e := os.ReadFile("testdata/compose.yaml")
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(root, "compose.yaml"), b, 0600)
	return New(t.TempDir()), domain.Workspace{TaskID: "task-a", Root: root}, domain.IntegrationConfig{ComposeFile: "compose.yaml", StartupTimeoutSeconds: 5, Checks: []domain.Check{{Name: "flow", Service: "verifier", Command: []string{"cat", "/data/result"}, Required: true}}}
}
func TestValidateIsolation(t *testing.T) {
	m, ws, _ := fixture(t)
	base := `{"services":{"app":{"image":"busybox"}},"networks":{"default":{"name":"sj-task-a_default"}},"volumes":{"data":{"name":"sj-task-a_data"}}}`
	if _, e := m.validate(ws, ws.Root, []byte(base)); e != nil {
		t.Fatal(e)
	}
	cases := []string{`"container_name":"fixed"`, `"network_mode":"host"`, `"privileged":true`, `"ports":[{"published":"8000"}]`, `"volumes":[{"type":"bind","source":"/var/run/docker.sock","target":"/var/run/docker.sock"}]`, `"volumes":[{"type":"bind","source":"/etc","target":"/host"}]`}
	for _, v := range cases {
		raw := `{"services":{"app":{` + v + `}}}`
		if _, e := m.validate(ws, ws.Root, []byte(raw)); e == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, r := range []string{`{"name":"shared"}`, `{"external":true}`, `{"driver_opts":{"device":"/etc","type":"none","o":"bind"}}`} {
		raw := `{"services":{"app":{}},"volumes":{"data":` + r + `}}`
		if _, e := m.validate(ws, ws.Root, []byte(raw)); e == nil {
			t.Errorf("accepted %s", r)
		}
	}
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(ws.Root, "escape"))
	raw := `{"services":{"app":{"volumes":[{"type":"bind","source":"` + filepath.Join(ws.Root, "escape") + `","target":"/host"}]}}}`
	if _, e := m.validate(ws, ws.Root, []byte(raw)); e == nil {
		t.Fatal("symlink escape accepted")
	}
}
func TestRealComposeConfigAndExecutionFailures(t *testing.T) {
	if _, e := exec.LookPath("docker"); e != nil {
		t.Skip("docker CLI unavailable")
	}
	m, ws, cfg := fixture(t)
	args, root, e := m.setup(ws, cfg)
	if e != nil {
		t.Fatal(e)
	}
	b, _, e := execute(context.Background(), root, append(args, "config", "--format", "json"), m.environment(ws))
	if e != nil {
		t.Skipf("compose CLI unavailable: %v %s", e, b)
	}
	if _, e = m.validate(ws, root, b); e != nil {
		t.Fatal(e)
	}
	var seen [][]string
	m.Execute = func(ctx context.Context, dir string, args, env []string) ([]byte, int, error) {
		seen = append(seen, append([]string{}, args...))
		if strings.Contains(strings.Join(args, " "), "config --format json") {
			return b, 0, nil
		}
		if strings.Contains(strings.Join(args, " "), "exec -T") {
			return []byte("assertion failed"), 7, errors.New("exit status 7")
		}
		return []byte("started"), 0, nil
	}
	if e = m.Start(context.Background(), ws, cfg); e != nil {
		t.Fatal(e)
	}
	results, e := m.Verify(context.Background(), ws, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if len(results) != 1 || results[0].Passed || results[0].ExitCode != 7 {
		t.Fatalf("wrong results %#v", results)
	}
	report, e := os.ReadFile(results[0].Report)
	if e != nil || !strings.Contains(string(report), "assertion failed") {
		t.Fatal("missing report")
	}
	if e = m.Stop(context.Background(), ws, cfg); e != nil {
		t.Fatal(e)
	}
	for _, args := range seen {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--project-name sj-task-a") {
			t.Fatalf("unscoped invocation %s", joined)
		}
	}
	cfg.Checks = nil
	if _, e = m.Verify(context.Background(), ws, cfg); e == nil {
		t.Fatal("missing required checks accepted")
	}
}
func TestUnsafeComposePath(t *testing.T) {
	m, ws, cfg := fixture(t)
	out := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(out, []byte("services: {}"), 0600)
	os.Symlink(out, filepath.Join(ws.Root, "escape.yaml"))
	cfg.ComposeFile = "escape.yaml"
	if _, _, e := m.setup(ws, cfg); e == nil {
		t.Fatal("compose symlink escape accepted")
	}
}
func TestStartupFailureRetainsStack(t *testing.T) {
	m, ws, cfg := fixture(t)
	var commands []string
	m.Execute = func(ctx context.Context, dir string, args, env []string) ([]byte, int, error) {
		s := strings.Join(args, " ")
		commands = append(commands, s)
		if strings.Contains(s, "config --format json") {
			b, _ := json.Marshal(map[string]any{"services": map[string]any{"verifier": map[string]any{}}})
			return b, 0, nil
		}
		return []byte("health timeout"), 1, errors.New("failed")
	}
	if e := m.Start(context.Background(), ws, cfg); e == nil {
		t.Fatal("startup failure hidden")
	}
	for _, s := range commands {
		if strings.Contains(s, " down ") {
			t.Fatal("failure tore down diagnostics")
		}
	}
	if _, e := os.Stat(filepath.Join(m.DataDir, "runs", ws.TaskID, "integration", "startup.log")); e != nil {
		t.Fatal(e)
	}
}
func TestDockerRuntimeIsolation(t *testing.T) {
	if os.Getenv("SWARMJET_DOCKER_TEST") != "1" {
		t.Skip("set SWARMJET_DOCKER_TEST=1 for disposable Docker runtime test")
	}
	m, a, cfg := fixture(t)
	a.TaskID = "runtime-a-" + strconv.Itoa(os.Getpid())
	b := a
	b.TaskID = "runtime-b-" + strconv.Itoa(os.Getpid())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, ws := range []domain.Workspace{a, b} {
		if e := m.Start(ctx, ws, cfg); e != nil {
			t.Fatal(e)
		}
		defer m.Stop(context.Background(), ws, cfg)
	}
	for _, ws := range []domain.Workspace{a, b} {
		checks := cfg
		checks.Checks = []domain.Check{{Name: "own task data", Service: "verifier", Command: []string{"sh", "-c", `test "$(cat /data/result)" = "$1"`, "check", ws.TaskID}, Required: true}}
		v, e := m.Verify(ctx, ws, checks)
		if e != nil || len(v) != 1 || !v[0].Passed {
			t.Fatalf("task %s not isolated: %#v %v", ws.TaskID, v, e)
		}
	}
}
func TestRunStateCannotBeMounted(t *testing.T) {
	m, ws, _ := fixture(t)
	for _, rel := range []string{"workspace.json", "executions/other/result.json"} {
		source := filepath.Join(m.DataDir, "runs", ws.TaskID, rel)
		raw := `{"services":{"app":{"volumes":[{"type":"bind","source":"` + source + `","target":"/data"}]}}}`
		if _, e := m.validate(ws, ws.Root, []byte(raw)); e == nil {
			t.Fatalf("control state mount accepted %s", rel)
		}
	}
	source := filepath.Join(m.DataDir, "runs", ws.TaskID, "integration", "fixtures")
	raw := `{"services":{"app":{"volumes":[{"type":"bind","source":"` + source + `","target":"/fixtures"}]}}}`
	if _, e := m.validate(ws, ws.Root, []byte(raw)); e != nil {
		t.Fatal(e)
	}
}
func TestComposeEnvironmentDoesNotInheritWorkerSecrets(t *testing.T) {
	m, ws, _ := fixture(t)
	for _, key := range []string{"GITLAB_TOKEN", "GITLAB_MERGE_TOKEN", "ADMIN_PASSWORD", "DATABASE_URL", "OPENAI_API_KEY"} {
		t.Setenv(key, "worker-only-secret")
	}
	m.Env = map[string]string{"TEST_SEED_VALUE": "allowed-test-value"}
	env := strings.Join(m.environment(ws), "\n")
	if strings.Contains(env, "worker-only-secret") {
		t.Fatal("worker credentials exposed to compose interpolation")
	}
	if !strings.Contains(env, "TEST_SEED_VALUE=allowed-test-value") || !strings.Contains(env, "SWARMJET_TASK_ID="+ws.TaskID) {
		t.Fatal("explicit test/task environment missing")
	}
}
func TestStopAfterWorkspaceReleaseIsIdempotent(t *testing.T) {
	m, ws, cfg := fixture(t)
	calls := 0
	m.Execute = func(ctx context.Context, dir string, args, env []string) ([]byte, int, error) {
		calls++
		if strings.Contains(strings.Join(args, " "), "config --format json") {
			return []byte(`{"services":{"verifier":{}}}`), 0, nil
		}
		return nil, 0, nil
	}
	if e := m.Stop(context.Background(), ws, cfg); e != nil {
		t.Fatal(e)
	}
	firstCalls := calls
	os.Remove(filepath.Join(ws.Root, "compose.yaml"))
	os.Remove(ws.Root)
	if e := m.Stop(context.Background(), ws, cfg); e != nil {
		t.Fatal(e)
	}
	if calls != firstCalls {
		t.Fatal("repeated teardown after workspace release")
	}
	if strings.Contains(m.stopMarker(ws.TaskID), filepath.Join("runs", ws.TaskID)) {
		t.Fatal("control marker exposed under run artifacts")
	}
}
func TestStopBeforePreparationDoesNothing(t *testing.T) {
	m := New(t.TempDir())
	m.Execute = func(context.Context, string, []string, []string) ([]byte, int, error) {
		t.Fatal("empty workspace invoked Docker")
		return nil, 0, nil
	}
	if e := m.Stop(context.Background(), domain.Workspace{}, domain.IntegrationConfig{}); e != nil {
		t.Fatal(e)
	}
}
