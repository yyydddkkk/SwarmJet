package pipeline

import (
	"path/filepath"
	"testing"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// replayLogger keeps replayer output out of the test log.
type replayLogger struct{}

func (replayLogger) Debug(string, ...interface{}) {}
func (replayLogger) Info(string, ...interface{})  {}
func (replayLogger) Warn(string, ...interface{})  {}
func (replayLogger) Error(string, ...interface{}) {}

// TestWorkflowReplay replays histories captured from a real Temporal server
// before the stage and phase refactor. Any change to the kind, order or
// arguments of Activities, timers, signal reads or version markers makes replay
// fail, so this guards the determinism contract.
func TestWorkflowReplay(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "worktask_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no replay histories in testdata")
	}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(WorkTask, workflow.RegisterOptions{Name: WorkflowName})
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			if err := replayer.ReplayWorkflowHistoryFromJSONFile(replayLogger{}, file); err != nil {
				t.Fatal(err)
			}
		})
	}
}
