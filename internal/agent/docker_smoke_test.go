package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// This smoke test uses a local fake-agent image and real Docker process/lifecycle
// handling. It never connects to a model provider or remote Git service.
func TestDockerSmokeIsolatedExecutionAndCancellation(t *testing.T) {
	if os.Getenv("SWARMJET_DOCKER_SMOKE") != "1" {
		t.Skip("set SWARMJET_DOCKER_SMOKE=1 for local Docker smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	base := os.Getenv("SWARMJET_DOCKER_SMOKE_BASE")
	if base == "" {
		base = "postgres:18.1-alpine3.23"
	}
	run := func(args ...string) []byte {
		t.Helper()
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return output
	}
	run("image", "inspect", base)
	build := t.TempDir()
	image := fmt.Sprintf("swarmjet-agent-smoke:%d", time.Now().UnixNano())
	script := `#!/bin/sh
set -eu
prompt=''
while [ "$#" -gt 0 ]; do
 if [ "$1" = '--file' ]; then shift; prompt="$1"; fi
 shift
done
if [ -e "$SWARMJET_SMOKE_CONTROL" ] || [ -e "$SWARMJET_SMOKE_SIBLING" ]; then
 echo 'control or sibling directory exposed' >&2
 exit 97
fi
printf '{"sessionID":"ses_docker_smoke","text":"%s"}\n' "$SMOKE_SECRET"
printf 'durable history\n' > "$XDG_DATA_HOME/smoke-session"
# OpenCode writes startup files alongside its private configuration snapshot.
printf '*\n' > "$XDG_CONFIG_HOME/opencode/.gitignore"
if [ "${SWARMJET_SMOKE_WAIT:-}" = 'yes' ]; then sleep 60; fi
result=$(sed -n 's/^Write your final structured result as a JSON object to \(.*\)\. Required fields:.*$/\1/p' "$prompt")
printf '{"outcome":"changed","summary":"Local smoke completed","assumptions":[],"findings":[],"needsHelp":""}\n' > "$result"
`
	if err := os.WriteFile(filepath.Join(build, "opencode"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM " + base + "\nCOPY --chmod=755 opencode /usr/local/bin/opencode\n"
	if err := os.WriteFile(filepath.Join(build, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	run("build", "--network=none", "--pull=false", "-t", image, build)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		exec.CommandContext(cleanup, "docker", "image", "rm", image).Run()
	})
	runtime := New(t.TempDir())
	workspace := filepath.Join(runtime.DataDir, "task-sources", "task", "meta", ".worktrees", "task")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(runtime.DataDir, "task-sources", "sibling")
	if err := os.MkdirAll(sibling, 0700); err != nil {
		t.Fatal(err)
	}
	makeRequest := func(id string) domain.AgentRequest {
		return domain.AgentRequest{ExecutionID: id, TaskID: "task", Role: "dev", Workspace: workspace, Prompt: "Run local smoke.", Config: domain.AgentConfig{Adapter: "opencode", Model: "fake/local", Image: image, TimeoutSeconds: 60}, Env: map[string]string{"SMOKE_SECRET": "fake-private-token", "SWARMJET_SMOKE_CONTROL": filepath.Join(runtime.DataDir, "agent-executions"), "SWARMJET_SMOKE_SIBLING": sibling}}
	}
	cleanupExecution := func(id string) {
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			exec.CommandContext(cleanup, "docker", "rm", "--force", "--volumes", "sj-agent-"+id).Run()
		})
	}
	id := fmt.Sprintf("smoke-success-%d", time.Now().UnixNano())
	cleanupExecution(id)
	req := makeRequest(id)
	execution, err := runtime.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for execution.Status == "running" || execution.Status == "starting" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
		execution, err = runtime.Inspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	if execution.Status != "succeeded" || execution.SessionID != "ses_docker_smoke" || execution.Result == nil {
		t.Fatalf("unexpected execution: %+v", execution)
	}
	logs, err := os.ReadFile(execution.LogPath)
	if err != nil || strings.Contains(string(logs), "fake-private-token") {
		t.Fatalf("logs not redacted: %s %v", logs, err)
	}
	id = fmt.Sprintf("smoke-cancel-%d", time.Now().UnixNano())
	cleanupExecution(id)
	req = makeRequest(id)
	req.Env["SWARMJET_SMOKE_WAIT"] = "yes"
	execution, err = runtime.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the fake process' session event without invoking Inspect, then cancel:
	// cancellation itself must recover that event into trusted execution metadata.
	for {
		logs, err := runtime.run(ctx, []string{"logs", execution.ContainerID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(logs), "ses_docker_smoke") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err = runtime.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	execution, err = runtime.Inspect(ctx, id)
	if err != nil || execution.Status != "cancelled" || execution.SessionID != "ses_docker_smoke" {
		t.Fatalf("cancel lost session: %+v %v", execution, err)
	}
	container, err := runtime.inspectContainer(ctx, execution.ContainerID)
	if err != nil || container.State.Running {
		t.Fatalf("cancelled container still alive: %+v %v", container, err)
	}
}
