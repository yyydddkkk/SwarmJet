package initflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"swarmjet/internal/domain"
)

func writingAgent(writeIntegration bool, composeServices []string) func(domain.AgentRequest) (domain.Execution, error) {
	return func(req domain.AgentRequest) (domain.Execution, error) {
		if err := os.MkdirAll(req.GeneratedDir, 0700); err != nil {
			return domain.Execution{}, err
		}
		if err := os.WriteFile(filepath.Join(req.GeneratedDir, "PROJECT.md"), []byte("# Demo"), 0600); err != nil {
			return domain.Execution{}, err
		}
		verification, _ := json.Marshal(map[string]any{"commands": []map[string]any{{"name": "unit", "path": "services/svc", "argv": []string{"go", "test", "./..."}}}})
		if err := os.WriteFile(filepath.Join(req.GeneratedDir, "verification.json"), verification, 0600); err != nil {
			return domain.Execution{}, err
		}
		if writeIntegration {
			spec, _ := json.Marshal(map[string]any{"composeFile": "integration/compose.yaml", "checks": []map[string]any{{"name": "integration", "service": "runner", "command": []string{"go", "test", "./..."}, "required": true}}})
			if err := os.WriteFile(filepath.Join(req.GeneratedDir, "integration.json"), spec, 0600); err != nil {
				return domain.Execution{}, err
			}
		}
		if len(composeServices) > 0 {
			nl := string(byte(10))
			doc := "services:" + nl
			for _, service := range composeServices {
				doc += "  " + service + ":" + nl + "    image: alpine" + nl
			}
			if err := os.MkdirAll(filepath.Join(req.GeneratedDir, "integration"), 0700); err != nil {
				return domain.Execution{}, err
			}
			if err := os.WriteFile(filepath.Join(req.GeneratedDir, "integration", "compose.yaml"), []byte(doc), 0600); err != nil {
				return domain.Execution{}, err
			}
		}
		return domain.Execution{ID: req.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "ready", Summary: "done"}}, nil
	}
}

// Static initialization is independent of the task execution environment.
func TestStaticInitializationDoesNotRequireCompose(t *testing.T) {
	t.Run("file missing", func(t *testing.T) {
		f := newFixture(t)
		f.agents.onStart = writingAgent(true, nil)
		init := f.drive(t, 12)
		if init.Status != domain.InitReady || !init.AnalysisComplete || len(f.runs) != 0 {
			t.Fatalf("missing compose file blocked static initialization: %+v", init)
		}
	})
	t.Run("service missing", func(t *testing.T) {
		f := newFixture(t)
		f.agents.onStart = writingAgent(true, []string{"other"})
		init := f.drive(t, 12)
		if init.Status != domain.InitReady || !init.AnalysisComplete || len(f.runs) != 0 {
			t.Fatalf("missing compose service blocked static initialization: %+v", init)
		}
	})
	t.Run("complete", func(t *testing.T) {
		f := newFixture(t)
		f.agents.onStart = writingAgent(true, []string{"runner"})
		init := f.drive(t, 12)
		if init.Status != domain.InitReady || !init.ReadyForTasks {
			t.Fatalf("complete integration rejected: %+v", init)
		}
	})
}
