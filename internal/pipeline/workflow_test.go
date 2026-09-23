package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"swarmjet/internal/domain"
)

type workflowHarness struct {
	env    *testsuite.TestWorkflowEnvironment
	mu     sync.Mutex
	saved  []domain.Task
	phases []string
	counts map[string]int
	step   func(context.Context, StepInput, int) (StepResult, error)
}

func workflowTask() domain.Task {
	return domain.Task{ID: "task", Stage: "queued", Project: domain.Project{MaxReworks: 3}, Issue: domain.Issue{ProjectID: 1, IID: 1, Labels: []string{"autonomous"}}, Executions: map[string]domain.Execution{}, Workspace: domain.Workspace{TaskID: "task", Revisions: []domain.Revision{{RepositoryID: "service", BaseSHA: "base", HeadSHA: "head", TargetBranch: "main", Changed: true}}}}
}
func newWorkflowHarness(t *testing.T) *workflowHarness {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	h := &workflowHarness{env: suite.NewTestWorkflowEnvironment(), counts: map[string]int{}}
	h.env.SetTestTimeout(10 * time.Second)
	h.env.RegisterActivityWithOptions(func(ctx context.Context, task domain.Task) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.saved = append(h.saved, task)
		return nil
	}, activity.RegisterOptions{Name: "SaveTask"})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, input StepInput) (StepResult, error) {
		h.mu.Lock()
		h.phases = append(h.phases, input.Phase)
		h.counts[input.Phase]++
		count := h.counts[input.Phase]
		h.mu.Unlock()
		if h.step != nil {
			return h.step(ctx, input, count)
		}
		return successfulStep(input), nil
	}, activity.RegisterOptions{Name: "Step"})
	return h
}
func successfulStep(input StepInput) StepResult {
	task := input.Task
	if task.Executions == nil {
		task.Executions = map[string]domain.Execution{}
	}
	switch input.Phase {
	case "developing":
		task.Executions["dev"] = domain.Execution{ID: input.ExecutionID, Status: "succeeded", SessionID: "ses_dev", Result: &domain.AgentResult{Outcome: "changed", Summary: "implemented"}}
	case "reviewing":
		task.Executions["review"] = domain.Execution{Status: "succeeded", Result: &domain.AgentResult{Outcome: "approved", Summary: "reviewed"}}
	}
	return StepResult{Task: task, Ready: true}
}
func (h *workflowHarness) run(t *testing.T, task domain.Task) domain.Task {
	t.Helper()
	h.env.ExecuteWorkflow(WorkTask, task)
	if !h.env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := h.env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.saved) == 0 {
		t.Fatal("no persisted state")
	}
	return h.saved[len(h.saved)-1]
}
func (h *workflowHarness) state(t *testing.T) domain.Task {
	t.Helper()
	value, err := h.env.QueryWorkflow("state")
	if err != nil {
		t.Fatal(err)
	}
	var task domain.Task
	if err = value.Get(&task); err != nil {
		t.Fatal(err)
	}
	return task
}
func (h *workflowHarness) signalAfter(d time.Duration, c domain.Command) {
	h.env.RegisterDelayedCallback(func() { h.env.SignalWorkflow(SignalName, c) }, d)
}
func TestWorkflowHappyChanged(t *testing.T) {
	h := newWorkflowHarness(t)
	final := h.run(t, workflowTask())
	if final.Stage != "completed" {
		t.Fatal(final.Stage)
	}
	want := []string{"waiting_dependencies", "preparing", "developing", "verifying", "publishing", "reviewing", "acquiring_merge", "merging", "finalizing"}
	if !reflect.DeepEqual(h.phases, want) {
		t.Fatalf("phases %v", h.phases)
	}
	if final.ReviewDigest != domain.Digest(final.Workspace.Revisions) {
		t.Fatal("review digest missing")
	}
}
func TestEvergreenRoundAutoMergesSensitiveChangeWithoutApproval(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Round = 1
	task.Workspace.Revisions[0].Sensitive = true
	task.Issue.Labels = append(task.Issue.Labels, "no-auto-close")
	final := h.run(t, task)
	if final.Stage != "completed" || h.counts["waiting_approval"] != 0 || h.counts["merging"] != 1 || !strings.Contains(final.Message, "Issue 保持打开") {
		t.Fatalf("unexpected evergreen flow: %+v phases=%v", final, h.phases)
	}
}
func TestEvergreenRoundReworksMergeConflictBeforeMerging(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "acquiring_merge" && count == 1 {
			out.Ready = false
			out.Rework = true
			out.Task.Message = "STALE: merge conflict"
		}
		return out, nil
	}
	task := workflowTask()
	task.Round = 1
	final := h.run(t, task)
	if final.Stage != "completed" || final.Reworks != 1 || h.counts["developing"] != 2 || h.counts["verifying"] != 2 || h.counts["reviewing"] != 2 || h.counts["merging"] != 1 {
		t.Fatalf("conflict did not get reworked and rechecked: %+v %v", final, h.counts)
	}
}
func TestWorkflowClarificationAssumptionsContinue(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "developing" {
			out.Task.Executions["dev"].Result.Assumptions = []string{"Ambiguous naming: used existing convention"}
		}
		return out, nil
	}
	final := h.run(t, workflowTask())
	if final.Stage != "completed" || len(final.Assumptions) != 1 || h.counts["developing"] != 1 {
		t.Fatalf("%+v", final)
	}
}
func TestWorkflowReviewChangesRequestRework(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "reviewing" && count == 1 {
			out.Task.Executions["review"].Result.Outcome = "changes_requested"
			out.Task.Executions["review"].Result.Summary = "fix validation"
			out.Task.Executions["review"].Result.Findings = []domain.Finding{{RepositoryID: "service", Path: "main.go", Message: "validate input"}}
		}
		return out, nil
	}
	final := h.run(t, workflowTask())
	if final.Stage != "completed" || final.Reworks != 1 || h.counts["developing"] != 2 || h.counts["reviewing"] != 2 || len(final.Notes) != 2 {
		t.Fatalf("final %+v counts %v", final, h.counts)
	}
}
func TestWorkflowManualApprovalExactDigest(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Workspace.Revisions[0].Sensitive = true
	digest := domain.Digest(task.Workspace.Revisions)
	h.signalAfter(time.Second, domain.Command{ID: "wrong", Kind: "approve", Digest: "old-digest"})
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "waiting_approval" || state.ApprovalDigest != "" {
			t.Errorf("wrong approval accepted: %+v", state)
		}
	}, 2*time.Second)
	h.signalAfter(3*time.Second, domain.Command{ID: "correct", Kind: "approve", Digest: digest})
	final := h.run(t, task)
	if final.Stage != "completed" || final.ApprovalDigest != digest {
		t.Fatalf("%+v", final)
	}
}
func TestWorkflowManualRejectReworks(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Workspace.Revisions[0].Sensitive = true
	h.signalAfter(time.Second, domain.Command{ID: "reject", Kind: "reject", Text: "check edge case"})
	h.signalAfter(2*time.Second, domain.Command{ID: "approve", Kind: "approve", Digest: domain.Digest(task.Workspace.Revisions)})
	final := h.run(t, task)
	if final.Reworks != 1 || h.counts["developing"] != 2 || final.Stage != "completed" {
		t.Fatalf("%+v %v", final, h.counts)
	}
}
func TestWorkflowBlockedRetryAndTransientErrors(t *testing.T) {
	t.Run("transient activity retry", func(t *testing.T) {
		h := newWorkflowHarness(t)
		h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
			if input.Phase == "preparing" && count < 3 {
				return StepResult{}, errors.New("temporary transport error")
			}
			return successfulStep(input), nil
		}
		final := h.run(t, workflowTask())
		if final.Stage != "completed" || h.counts["preparing"] != 3 {
			t.Fatalf("%+v %v", final, h.counts)
		}
	})
	t.Run("blocked business retry", func(t *testing.T) {
		h := newWorkflowHarness(t)
		h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
			out := successfulStep(input)
			if input.Phase == "developing" && count == 1 {
				out.Task.Executions["dev"].Result.Outcome = "blocked"
				out.Task.Executions["dev"].Result.NeedsHelp = "missing access"
			}
			return out, nil
		}
		h.env.RegisterDelayedCallback(func() {
			if got := h.state(t); got.Stage != "blocked" {
				t.Errorf("expected blocked, got %s", got.Stage)
			}
			h.env.SignalWorkflow(SignalName, domain.Command{ID: "retry", Kind: "retry"})
		}, time.Second)
		final := h.run(t, workflowTask())
		if final.Stage != "completed" || h.counts["developing"] != 2 {
			t.Fatalf("%+v %v", final, h.counts)
		}
	})
	t.Run("nonretryable operation needs explicit retry", func(t *testing.T) {
		h := newWorkflowHarness(t)
		h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
			if input.Phase == "preparing" && count == 1 {
				return StepResult{}, temporal.NewNonRetryableApplicationError("invalid config", "configuration", nil)
			}
			return successfulStep(input), nil
		}
		h.signalAfter(time.Second, domain.Command{ID: "retry", Kind: "retry"})
		final := h.run(t, workflowTask())
		if final.Stage != "completed" || h.counts["preparing"] != 2 {
			t.Fatalf("%+v %v", final, h.counts)
		}
	})
}
func TestWorkflowNoChangeStillReviewed(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Workspace.Revisions[0].Changed = false
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "developing" {
			out.Task.Executions["dev"].Result.Outcome = "no_change"
		}
		return out, nil
	}
	final := h.run(t, task)
	if final.Stage != "completed" || h.counts["reviewing"] != 1 || h.counts["merging"] != 0 || h.counts["finalizing"] != 1 {
		t.Fatalf("%+v %v", final, h.counts)
	}
}
func TestWorkflowDuplicateCommandsAndDependenciesWait(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Dependencies = []domain.IssueRef{{ProjectID: 2, IID: 3}}
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "waiting_dependencies" {
			out.Ready = len(input.Task.Dependencies) == 0
		}
		return out, nil
	}
	note := domain.Command{ID: "same-note", Kind: "note", Text: "context does not block"}
	h.signalAfter(time.Second, note)
	h.signalAfter(2*time.Second, note)
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "waiting_dependencies" || len(state.Notes) != 1 {
			t.Errorf("dependency wait/dedup failed: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "clear-dependencies", Kind: "dependencies", Dependencies: []domain.IssueRef{}})
	}, 3*time.Second)
	final := h.run(t, task)
	if final.Stage != "completed" || len(final.Notes) != 1 || len(final.Dependencies) != 0 {
		t.Fatalf("%+v", final)
	}
}
func TestWorkflowPauseResumeAndCancellationDuringActivity(t *testing.T) {
	for _, kind := range []string{"pause", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			h := newWorkflowHarness(t)
			task := workflowTask()
			h.env.OnActivity("Step", mock.Anything, mock.MatchedBy(func(input StepInput) bool { return input.Phase == "developing" })).After(time.Hour).Return(func(_ context.Context, input StepInput) (StepResult, error) { return successfulStep(input), nil }).Once()
			h.env.OnActivity("Step", mock.Anything, mock.Anything).Return(func(_ context.Context, input StepInput) (StepResult, error) {
				h.mu.Lock()
				h.phases = append(h.phases, input.Phase)
				h.counts[input.Phase]++
				h.mu.Unlock()
				if input.Phase == "recover" {
					input.Task.Executions["dev"] = domain.Execution{SessionID: "ses_exact_resume", Status: "cancelled"}
					return StepResult{Task: input.Task, Ready: true}, nil
				}
				if input.Phase == "developing" && input.Task.Executions["dev"].SessionID != "ses_exact_resume" {
					t.Errorf("resume lost exact recovered session: %+v", input.Task.Executions)
				}
				return successfulStep(input), nil
			})
			h.signalAfter(time.Second, domain.Command{ID: kind, Kind: kind})
			if kind == "pause" {
				h.env.RegisterDelayedCallback(func() {
					state := h.state(t)
					if !state.Paused {
						t.Errorf("pause not retained: %+v", state)
					}
					h.env.SignalWorkflow(SignalName, domain.Command{ID: "resume", Kind: "resume"})
				}, 2*time.Second)
			}
			final := h.run(t, task)
			if kind == "cancel" {
				if final.Stage != "cancelled" || h.counts["stop"] != 1 || h.counts["merging"] != 0 {
					t.Fatalf("%+v %v", final, h.counts)
				}
			} else if final.Stage != "completed" || final.Paused {
				t.Fatalf("%+v", final)
			}
			if h.counts["recover"] != 1 {
				t.Fatalf("missing recovery after %s: %v", kind, h.counts)
			}
		})
	}
}

func TestWorkflowManualRejectionHonorsReworkBudget(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.MaxReworks = 0
	task.Workspace.Revisions[0].Sensitive = true
	h.signalAfter(time.Second, domain.Command{ID: "reject", Kind: "reject", Text: "requires another pass"})
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "blocked" {
			t.Errorf("manual rejection exceeded rework budget but stage=%s reworks=%d", state.Stage, state.Reworks)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel", Kind: "cancel"})
	}, 2*time.Second)
	final := h.run(t, task)
	if final.Stage != "cancelled" {
		t.Fatal(final.Stage)
	}
	if h.counts["developing"] != 1 {
		t.Errorf("development reran beyond configured budget: %d", h.counts["developing"])
	}
}

func TestWorkflowNoAutoCloseStillNeedsApprovalWithoutChanges(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Issue.Labels = append(task.Issue.Labels, "no-auto-close")
	task.Workspace.Revisions[0].Changed = false
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "developing" {
			out.Task.Executions["dev"].Result.Outcome = "no_change"
		}
		return out, nil
	}
	digest := domain.Digest(task.Workspace.Revisions)
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "waiting_approval" {
			t.Errorf("no-auto-close expected approval gate, got %s", state.Stage)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "approve", Kind: "approve", Digest: digest})
	}, time.Second)
	final := h.run(t, task)
	if final.ApprovalDigest != digest {
		t.Errorf("no-auto-close finalized without explicit approval: stage=%s digest=%q", final.Stage, final.ApprovalDigest)
	}
}

func TestWorkflowPartialMergeRetryPreservesCompletedRepository(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Workspace.Revisions = append(task.Workspace.Revisions, domain.Revision{RepositoryID: "second", BaseSHA: "a", HeadSHA: "b", TargetBranch: "main", Changed: true})
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		switch input.Phase {
		case "merging":
			out.Task.Workspace.Revisions[0].Merged = true
			out.Ready = false
		case "partial_merge":
			if !input.Task.Workspace.Revisions[0].Merged {
				t.Error("partial merge retry lost completed repository")
			}
			out.Task.Workspace.Revisions[1].Merged = true
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "blocked" || !state.Workspace.Revisions[0].Merged {
			t.Errorf("partial merge not retained: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "retry-remaining", Kind: "retry"})
	}, time.Second)
	final := h.run(t, task)
	if final.Stage != "completed" || h.counts["developing"] != 1 || h.counts["merging"] != 1 || h.counts["partial_merge"] != 1 {
		t.Fatalf("final=%+v phases=%v", final, h.counts)
	}
}

func TestWorkflowRequiredVerificationFailureConsumesReworkBudget(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.MaxReworks = 1
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "verifying" {
			out.Ready = false
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "blocked" || state.Reworks != 2 {
			t.Errorf("required verification failed without bounded rework: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel", Kind: "cancel"})
	}, time.Second)
	final := h.run(t, task)
	if final.Stage != "cancelled" || h.counts["developing"] != 2 || h.counts["publishing"] != 0 || h.counts["merging"] != 0 {
		t.Fatalf("final=%+v phases=%v", final, h.counts)
	}
}

func TestWorkflowLongDependencyWaitContinuesWithDurableCheckpoint(t *testing.T) {
	first := newWorkflowHarness(t)
	task := workflowTask()
	task.Dependencies = []domain.IssueRef{{ProjectID: 9, IID: 7}}
	first.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "waiting_dependencies" {
			out.Ready = false
		}
		return out, nil
	}
	note := domain.Command{ID: "persisted-note", Kind: "note", Text: "survives continue as new"}
	first.signalAfter(time.Second, note)
	first.env.ExecuteWorkflow(WorkTask, task)
	var continuation *workflow.ContinueAsNewError
	if err := first.env.GetWorkflowError(); !errors.As(err, &continuation) {
		t.Fatalf("expected continue-as-new, got %v", err)
	}
	var next domain.Task
	if err := converter.GetDefaultDataConverter().FromPayloads(continuation.Input, &next); err != nil {
		t.Fatal(err)
	}
	if next.Stage != "waiting_dependencies" || next.Checkpoint == nil {
		t.Fatalf("checkpoint lost waiting stage: %+v", next)
	}
	checkpoint := next.Checkpoint
	if checkpoint.Serial != 500 || checkpoint.PreviousPhase != "waiting_dependencies" || !checkpoint.Seen[note.ID] || len(next.Notes) != 1 {
		t.Fatalf("invalid checkpoint: %+v notes=%v", checkpoint, next.Notes)
	}
	if continuation.WorkflowType.Name != WorkflowName {
		t.Fatalf("continued wrong workflow: %s", continuation.WorkflowType.Name)
	}
	second := newWorkflowHarness(t)
	var executionIDs []string
	second.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		executionIDs = append(executionIDs, input.ExecutionID)
		out := successfulStep(input)
		if input.Phase == "waiting_dependencies" {
			out.Ready = len(input.Task.Dependencies) == 0
		}
		return out, nil
	}
	second.signalAfter(time.Second, note)
	second.signalAfter(2*time.Second, domain.Command{ID: "unblock", Kind: "dependencies", Dependencies: []domain.IssueRef{}})
	final := second.run(t, next)
	if final.Stage != "completed" || len(final.Notes) != 1 {
		t.Fatalf("continuation lost state or command dedup: %+v", final)
	}
	want := fmt.Sprintf("task-waiting_dependencies-%d", checkpoint.Serial+1)
	if len(executionIDs) == 0 || executionIDs[0] != want {
		t.Fatalf("execution serial reused after continuation: %v want first %s", executionIDs, want)
	}
}
func TestWorkflowStopFailureRequiresSuccessfulExplicitRetry(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		if input.Phase == "stop" {
			return StepResult{}, temporal.NewNonRetryableApplicationError("container still running", "stop_failed", nil)
		}
		out := successfulStep(input)
		if input.Phase == "waiting_dependencies" {
			out.Ready = false
		}
		return out, nil
	}
	h.signalAfter(time.Second, domain.Command{ID: "cancel-first", Kind: "cancel"})
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "blocked" {
			t.Errorf("failed stop falsely reported terminal stage: %s", state.Stage)
		}
		h.mu.Lock()
		for _, saved := range h.saved {
			if saved.Stage == "cancelled" {
				t.Error("cancelled persisted before successful stop")
			}
		}
		h.mu.Unlock()
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "retry-stop", Kind: "retry"})
	}, 2*time.Second)
	final := h.run(t, workflowTask())
	if final.Stage != "cancelled" || h.counts["stop"] != 1 || h.counts["stopping"] != 1 || h.counts["developing"] != 0 {
		t.Fatalf("final=%+v phases=%v", final, h.counts)
	}
}
func TestWorkflowRapidPauseResumeRetainsActivePhase(t *testing.T) {
	h := newWorkflowHarness(t)
	h.env.OnActivity("Step", mock.Anything, mock.MatchedBy(func(in StepInput) bool { return in.Phase == "developing" })).After(time.Hour).Return(func(_ context.Context, in StepInput) (StepResult, error) { return successfulStep(in), nil }).Once()
	recovered := false
	h.env.OnActivity("Step", mock.Anything, mock.Anything).Return(func(_ context.Context, in StepInput) (StepResult, error) {
		if in.Phase == "recover" {
			recovered = true
			in.Task.Executions["dev"] = domain.Execution{Status: "cancelled", SessionID: "ses_rapid_pause"}
			return StepResult{Task: in.Task, Ready: true}, nil
		}
		if in.Phase == "developing" && in.Task.Executions["dev"].SessionID != "ses_rapid_pause" {
			t.Error("rapid resume lost exact recovered session")
		}
		return successfulStep(in), nil
	})
	h.env.RegisterDelayedCallback(func() {
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "pause", Kind: "pause"})
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "resume", Kind: "resume"})
	}, time.Second)
	final := h.run(t, workflowTask())
	if final.Stage != "completed" || final.Paused || !recovered {
		t.Fatalf("rapid pause/resume did not recover: %+v recovered=%v", final, recovered)
	}
	for _, saved := range h.saved {
		if saved.Stage == "blocked" {
			t.Fatalf("rapid pause/resume became blocked: %+v", saved)
		}
	}
}
func TestWorkflowCheckpointPreservesBoundedCIWait(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Stage = "acquiring_merge"
	task.Checkpoint = &domain.WorkflowCheckpoint{Serial: 900, PreviousPhase: "acquiring_merge", CIWaits: 179, Seen: map[string]bool{}}
	h.step = func(_ context.Context, in StepInput, _ int) (StepResult, error) {
		out := successfulStep(in)
		if in.Phase == "acquiring_merge" {
			out.Ready = false
			out.Task.Message = "等待 GitLab CI 或合并检查"
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != "blocked" {
			t.Errorf("CI wait budget reset at checkpoint: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel", Kind: "cancel"})
	}, time.Second)
	final := h.run(t, task)
	if final.Stage != "cancelled" || h.counts["acquiring_merge"] != 1 {
		t.Fatalf("unbounded CI wait after restart: final=%s checks=%d", final.Stage, h.counts["acquiring_merge"])
	}
}
