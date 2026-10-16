package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
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
		if input.Phase == domain.StageWaitingManualMerge {
			waits++
			if waits < 3 {
				out.Ready = false
				out.Task.Message = "waiting for human merge"
			}
		}
		return out, nil
	}
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted {
		t.Fatalf("stage %s", final.Stage)
	}
	if waits != 3 {
		t.Fatalf("expected to wait for the human merge, got %d", waits)
	}
	sawWait := false
	for _, phase := range h.phases {
		if phase == domain.StageAcquiringMerge || phase == domain.StageMerging {
			t.Fatalf("system initiated a merge while auto-merge is disabled: %v", h.phases)
		}
		if phase == domain.StageWaitingManualMerge {
			sawWait = true
		}
	}
	if !sawWait {
		t.Fatalf("missing manual merge wait: %v", h.phases)
	}
}

func TestManualMergeCommandRunsOnceInsideWorkflow(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Stage = domain.StageWaitingManualMerge
	task.Project.AutoMerge = autoMerge(false)
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	h.step = func(_ context.Context, in StepInput, _ int) (StepResult, error) {
		out := successfulStep(in)
		if in.Phase == domain.StageWaitingManualMerge {
			out.Ready = false
		}
		return out, nil
	}
	command := domain.Command{ID: "manual-merge", Kind: "merge_round"}
	h.signalAfter(time.Second, command)
	h.signalAfter(2*time.Second, command)
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || h.counts[domain.StageManualMerging] != 1 || h.counts[domain.StageMerging] != 0 || final.AutoMergeEnabled() {
		t.Fatalf("manual command was not serialized: stage=%s phases=%v auto=%v", final.Stage, h.phases, final.AutoMergeEnabled())
	}
}

func TestManualMergeFailureRetainsEarlierRepositoryResult(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Stage = domain.StageManualMerging
	task.Workspace.Revisions = append(task.Workspace.Revisions, domain.Revision{RepositoryID: "second", BaseSHA: "base-2", HeadSHA: "head-2", TargetBranch: "main", Changed: true})
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	h.step = func(_ context.Context, in StepInput, count int) (StepResult, error) {
		if in.Phase == phaseRecoverManualMerge {
			h.mu.Lock()
			defer h.mu.Unlock()
			return StepResult{Task: h.saved[len(h.saved)-1], Ready: true}, nil
		}
		if in.Phase == domain.StageManualMerging && count <= 3 {
			persisted := in.Task
			persisted.Workspace.Revisions[0].Merged = true
			h.mu.Lock()
			h.saved = append(h.saved, persisted)
			h.mu.Unlock()
			return StepResult{}, errors.New("second repository unavailable")
		}
		if in.Phase == domain.StageManualMerging {
			if !in.Task.Workspace.Revisions[0].Merged {
				t.Fatal("retry lost the earlier remote merge")
			}
			out := successfulStep(in)
			out.Task.Workspace.Revisions[1].Merged = true
			return out, nil
		}
		return successfulStep(in), nil
	}
	h.signalAfter(time.Second, domain.Command{ID: "retry-manual", Kind: "retry"})
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || !final.Workspace.Revisions[0].Merged || !final.Workspace.Revisions[1].Merged || h.counts[phaseRecoverManualMerge] != 1 {
		t.Fatalf("partial merge was not recovered: stage=%s phases=%v revisions=%+v", final.Stage, h.phases, final.Workspace.Revisions)
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
			if final.Stage != domain.StageCompleted || h.counts[domain.StageAcquiringMerge] == 0 || h.counts[domain.StageMerging] == 0 {
				t.Fatalf("legacy merge path changed: %s %v", final.Stage, h.counts)
			}
		})
	}
}

func TestTaskAutoMergeOverrideWinsOverProjectDefault(t *testing.T) {
	task := workflowTask()
	task.Project.AutoMerge = autoMerge(true)
	if !task.AutoMergeEnabled() {
		t.Fatal("project default was not inherited")
	}
	task.AutoMergeOverride = autoMerge(false)
	if task.AutoMergeEnabled() {
		t.Fatal("task switch did not override project default")
	}
	task.Project.AutoMerge = autoMerge(false)
	task.AutoMergeOverride = autoMerge(true)
	if !task.AutoMergeEnabled() {
		t.Fatal("task could not opt into automatic merge")
	}
}

func TestTurningOffAutoMergeDuringDevelopmentKeepsManualMergePath(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.AutoMerge = autoMerge(true)
	h.env.OnActivity("Step", mock.Anything, mock.MatchedBy(func(in StepInput) bool { return in.Phase == domain.StageDeveloping })).After(2 * time.Second).Return(func(_ context.Context, in StepInput) (StepResult, error) { return successfulStep(in), nil }).Once()
	var phases []string
	h.env.OnActivity("Step", mock.Anything, mock.Anything).Return(func(_ context.Context, in StepInput) (StepResult, error) {
		phases = append(phases, in.Phase)
		return successfulStep(in), nil
	})
	h.signalAfter(time.Second, domain.Command{ID: "disable-task-merge", Kind: "set_auto_merge", AutoMerge: autoMerge(false)})
	final := h.run(t, task)
	sawManual := false
	for _, phase := range phases {
		if phase == domain.StageAcquiringMerge || phase == domain.StageMerging {
			t.Fatalf("task switch was lost during Agent execution: phases=%v", phases)
		}
		sawManual = sawManual || phase == domain.StageWaitingManualMerge
	}
	if final.Stage != domain.StageCompleted || final.AutoMergeEnabled() || !sawManual {
		t.Fatalf("task switch was lost during Agent execution: stage=%s phases=%v enabled=%v", final.Stage, phases, final.AutoMergeEnabled())
	}
}

func TestTurningOffAutoMergeDuringMergeGateStopsBeforeMerge(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Stage = domain.StageAcquiringMerge
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	h.env.OnActivity("Step", mock.Anything, mock.MatchedBy(func(in StepInput) bool { return in.Phase == domain.StageAcquiringMerge })).After(2 * time.Second).Return(func(_ context.Context, in StepInput) (StepResult, error) { return successfulStep(in), nil }).Once()
	var phases []string
	h.env.OnActivity("Step", mock.Anything, mock.Anything).Return(func(_ context.Context, in StepInput) (StepResult, error) {
		phases = append(phases, in.Phase)
		return successfulStep(in), nil
	})
	h.signalAfter(time.Second, domain.Command{ID: "disable-before-merge", Kind: "set_auto_merge", AutoMerge: autoMerge(false)})
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || final.AutoMergeEnabled() {
		t.Fatalf("switch not retained: %+v", final)
	}
	for _, phase := range phases {
		if phase == domain.StageMerging {
			t.Fatalf("merge started after off command: %v", phases)
		}
	}
}

func TestTaskCanTurnAutoMergeBackOnBeforeMerge(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Stage = domain.StageWaitingManualMerge
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	task.AutoMergeOverride = autoMerge(false)
	h.step = func(_ context.Context, in StepInput, _ int) (StepResult, error) {
		out := successfulStep(in)
		if in.Phase == domain.StageWaitingManualMerge {
			out.Ready = false
		}
		return out, nil
	}
	h.signalAfter(time.Second, domain.Command{ID: "enable-before-merge", Kind: "set_auto_merge", AutoMerge: autoMerge(true)})
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || !final.AutoMergeEnabled() || h.counts[domain.StageAcquiringMerge] != 1 || h.counts[domain.StageMerging] != 1 {
		t.Fatalf("task did not resume automatic merge: stage=%s phases=%v enabled=%v", final.Stage, h.phases, final.AutoMergeEnabled())
	}
}

// The manual-merge wait still refuses to finish when required verification is
// not current, and marks a rework instead of silently closing the issue.
func TestWorkflowManualMergeRequiresCurrentVerification(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageWaitingManualMerge && count == 1 {
			out.Ready = false
			out.Rework = true
			out.Task.Verification = []domain.Verification{{Name: "check", Required: true, Passed: false, RevisionDigest: "stale"}}
		}
		return out, nil
	}
	task := workflowTask()
	task.Project.AutoMerge = autoMerge(false)
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || final.Reworks != 1 || h.counts[domain.StageDeveloping] < 2 {
		t.Fatalf("stale verification was not reworked: %+v %v", final, h.counts)
	}
}
