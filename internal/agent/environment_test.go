package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarmjet/internal/execution"
)

func TestBackendPreparesPrivateGitLabConfigAndAdoptsRetry(t *testing.T) {
	r, docker, req := setup(t)
	req.Env["GITLAB_URI"] = "https://git.example:8443"
	req.Env["GITLAB_TOKEN"] = "private-token"
	var backend execution.AgentBackend = r
	first, err := backend.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.DataDir, "runs", req.TaskID, "executions", req.ExecutionID, "glab")
	p := filepath.Join(dir, "config.yml")
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), `host: "git.example:8443"`) || strings.Contains(string(b), "private-token") {
		t.Fatalf("invalid private GitLab configuration: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("GitLab configuration must be private", err)
	}
	if _, exists := req.Env["GLAB_CONFIG_DIR"]; exists {
		t.Fatal("backend mutated caller's environment")
	}
	env := strings.Join(docker.containers[first.ContainerID].Config.Env, "\n")
	if !strings.Contains(env, "GLAB_CONFIG_DIR="+dir) {
		t.Fatal("backend-local configuration not passed to execution")
	}
	second, err := backend.Start(context.Background(), req)
	if err != nil || second.ID != first.ID || docker.creates != 1 {
		t.Fatalf("retry did not adopt existing execution: creates=%d err=%v", docker.creates, err)
	}
}

func TestBackendRejectsUnsafeGitLabConfigurationBeforeLaunching(t *testing.T) {
	for _, scenario := range []string{"invalid-url", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			r, docker, req := setup(t)
			req.Env["GITLAB_URI"] = "https://git.example"
			outside := t.TempDir()
			if scenario == "invalid-url" {
				req.Env["GITLAB_URI"] = "file:///tmp/gitlab"
			} else {
				dir := filepath.Join(r.DataDir, "runs", req.TaskID, "executions", req.ExecutionID)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, "glab")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Start(context.Background(), req); err == nil || docker.creates != 0 {
				t.Fatalf("unsafe configuration launched: creates=%d err=%v", docker.creates, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("wrote outside execution directory", err)
			}
		})
	}
}
