package pipeline

import (
	"context"
	"testing"

	"swarmjet/internal/domain"
)

func autoMerge(value bool) *bool { return &value }

// A closed switch must never let the system initiate a merge, and sensitive
// changes must not turn into a system approval gate instead.
func TestWorkflowAutoMergeDisabledWaitsForManualMerge(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.AutoMerge = autoMerge(false)
	task.Workspace.Revisions[0].Sensitive = true
	waits := 0
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "waiting_manual_merge" {
			waits++
			if waits < 3 {
				out.Ready = false
				out.Task.Message = "waiting for human merge"
			}
		}
		return out, nil
	}
	final := h.run(t, task)
	if final.Stage != "completed" {
		t.Fatalf("stage %s", final.Stage)
	}
	if waits != 3 {
		t.Fatalf("expected to wait for the human merge, got %d", waits)
	}
	sawWait := false
	for _, phase := range h.phases {
		if phase == "acquiring_merge" || phase == "merging" {
			t.Fatalf("system initiated a merge while auto-merge is disabled: %v", h.phases)
		}
		if phase == "waiting_manual_merge" {
			sawWait = true
		}
	}
	if !sawWait {
		t.Fatalf("missing manual merge wait: %v", h.phases)
	}
}

// An explicit enabled switch keeps the previous behavior, and a nil switch is
// the legacy default (enabled) so existing projects do not change.
func TestWorkflowAutoMergeDefaultsPreserveLegacyBehavior(t *testing.T) {
	for name, value := range map[string]*bool{"nil": nil, "true": autoMerge(true)} {
		t.Run(name, func(t *testing.T) {
			h := newWorkflowHarness(t)
			task := workflowTask()
			task.Project.AutoMerge = value
			final := h.run(t, task)
			if final.Stage != "completed" || h.counts["acquiring_merge"] == 0 || h.counts["merging"] == 0 {
				t.Fatalf("legacy merge path changed: %s %v", final.Stage, h.counts)
			}
		})
	}
}

// The manual-merge wait still refuses to finish when required verification is
// not current, and marks a rework instead of silently closing the issue.
func TestWorkflowManualMergeRequiresCurrentVerification(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "waiting_manual_merge" && count == 1 {
			out.Ready = false
			out.Rework = true
			out.Task.Verification = []domain.Verification{{Name: "check", Required: true, Passed: false, RevisionDigest: "stale"}}
		}
		return out, nil
	}
	task := workflowTask()
	task.Project.AutoMerge = autoMerge(false)
	final := h.run(t, task)
	if final.Stage != "completed" || final.Reworks != 1 || h.counts["developing"] < 2 {
		t.Fatalf("stale verification was not reworked: %+v %v", final, h.counts)
	}
}
