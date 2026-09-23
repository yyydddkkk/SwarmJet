package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarmjet/internal/domain"
)

// The init role must read business sources through a read-only mount and write
// only into the generated directory. This is enforced by the container mounts,
// not by prompt text.
func TestInitRoleMountIsolation(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "initializations", "proj1")
	analysis := filepath.Join(root, "analysis")
	generated := filepath.Join(root, "generated")
	for _, path := range []string{analysis, generated} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeDocker{containers: map[string]container{}}
	r := New(dir)
	r.Executor = f
	req := domain.AgentRequest{
		ExecutionID:  "initexec",
		TaskID:       "proj1",
		Role:         "init",
		Workspace:    analysis,
		GeneratedDir: generated,
		Prompt:       "analyze",
		Config:       domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "test-image", TimeoutSeconds: 300},
	}
	result, err := r.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "running" && result.Status != "created" {
		t.Fatalf("unexpected status %s", result.Status)
	}
	var create []string
	for _, call := range f.calls {
		if len(call) > 0 && call[0] == "create" {
			create = call
		}
	}
	if create == nil {
		t.Fatal("no container create call recorded")
	}
	joined := strings.Join(create, " ")
	if !strings.Contains(joined, "src="+analysis+",dst="+analysis+",readonly") {
		t.Fatalf("analysis sources are not mounted read-only: %v", create)
	}
	if !strings.Contains(joined, "src="+generated+",dst="+generated) || strings.Contains(joined, "src="+generated+",dst="+generated+",readonly") {
		t.Fatalf("generated directory is not writable: %v", create)
	}
	if strings.Contains(joined, "src="+root+",dst="+root+",readonly") {
		t.Fatalf("whole initialization root is read-only, generated dir would be unusable: %v", create)
	}
	// A generated directory outside the initialization must be rejected.
	outside := filepath.Join(dir, "elsewhere")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	bad := req
	bad.ExecutionID = "initexec2"
	bad.GeneratedDir = outside
	if _, err := r.Start(context.Background(), bad); err == nil {
		t.Fatal("accepted a generated directory outside the initialization")
	}
	// A task role still mounts its private source tree read-write.
	if _, err := r.Start(context.Background(), domain.AgentRequest{ExecutionID: "devx", TaskID: "task", Role: "dev", Workspace: analysis, Prompt: "x", Config: req.Config}); err == nil {
		// The dev workspace lives under task-sources, so this must be rejected.
		t.Fatal("dev role accepted an initialization workspace")
	}
}
