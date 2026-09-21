package initflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"swarmjet/internal/domain"
)

// A manually configured integration must satisfy readiness even when the agent
// only produced verification commands.
func TestManualIntegrationSatisfiesReadiness(t *testing.T) {
	f := newFixture(t)
	p := f.repo.projects["p1"]
	p.Integration = domain.IntegrationConfig{ComposeFile: "integration/compose.yaml",
		Checks: []domain.Check{{Name: "t", Service: "runner", Command: []string{"true"}, Required: true}}}
	f.repo.projects["p1"] = p
	f.agents.onStart = func(req domain.AgentRequest) (domain.Execution, error) {
		if err := os.MkdirAll(req.GeneratedDir, 0700); err != nil {
			return domain.Execution{}, err
		}
		if err := os.WriteFile(filepath.Join(req.GeneratedDir, "PROJECT.md"), []byte("# Demo"), 0600); err != nil {
			return domain.Execution{}, err
		}
		spec, err := json.Marshal(map[string]any{"commands": []map[string]any{{"name": "unit", "path": "services/svc", "argv": []string{"go", "test", "./..."}}}})
		if err != nil {
			return domain.Execution{}, err
		}
		if err := os.WriteFile(filepath.Join(req.GeneratedDir, "verification.json"), spec, 0600); err != nil {
			return domain.Execution{}, err
		}
		if err := os.MkdirAll(filepath.Join(req.GeneratedDir, "integration"), 0700); err != nil {
			return domain.Execution{}, err
		}
		nl := string(byte(10))
		compose := "services:" + nl + "  runner:" + nl + "    image: alpine" + nl
		if err := os.WriteFile(filepath.Join(req.GeneratedDir, "integration", "compose.yaml"), []byte(compose), 0600); err != nil {
			return domain.Execution{}, err
		}
		return domain.Execution{ID: req.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "ready", Summary: "done"}}, nil
	}
	init := f.drive(t, 12)
	if init.Status != domain.InitReady || !init.ReadyForTasks {
		t.Fatalf("manual integration did not satisfy readiness: %+v", init)
	}
	if !f.m.ReadyForTasks(context.Background(), "p1") {
		t.Fatal("ReadyForTasks false after a verified run")
	}
}
