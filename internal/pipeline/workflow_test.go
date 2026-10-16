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
	"github.com/yyydddkkk/SwarmJet/internal/domain"
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
	return domain.Task{ID: "task", Stage: domain.StageQueued, Project: domain.Project{MaxReworks: 3}, Issue: domain.Issue{ProjectID: 1, IID: 1, Labels: []string{"autonomous"}}, Executions: map[string]domain.Execution{}, Workspace: domain.Workspace{TaskID: "task", Revisions: []domain.Revision{{RepositoryID: "service", BaseSHA: "base", HeadSHA: "head", TargetBranch: "main", Changed: true}}}}
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
	case domain.StageDeveloping:
		task.Executions["dev"] = domain.Execution{ID: input.ExecutionID, Status: "succeeded", SessionID: "ses_dev", Result: &domain.AgentResult{Outcome: "changed", Summary: "implemented"}}
	case domain.StageReviewing:
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
	if final.Stage != domain.StageCompleted {
		t.Fatal(final.Stage)
	}
	want := []string{domain.StageWaitingDependencies, domain.StagePreparing, domain.StageDeveloping, domain.StageVerifying, domain.StagePublishing, domain.StageReviewing, domain.StageAcquiringMerge, domain.StageMerging, domain.StageFinalizing}
	if !reflect.DeepEqual(h.phases, want) {
		t.Fatalf("phases %v", h.phases)
	}
	if final.ReviewDigest != domain.Digest(final.Workspace.Revisions) {
		t.Fatal("review digest missing")
	}
}

func TestVerificationFailureReportsFailedChecks(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.MaxReworks = 0
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageVerifying {
			out.Ready = false
			out.Task.Verification = []domain.Verification{{Name: "web typecheck", Required: true, Passed: false}}
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageBlocked || !strings.Contains(state.Message, "web typecheck") {
			t.Errorf("verification failure was hidden: stage=%s message=%q", state.Stage, state.Message)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel-after-verification", Kind: "cancel"})
	}, time.Second)
	if final := h.run(t, task); final.Stage != domain.StageCancelled {
		t.Fatal(final.Stage)
	}
}

func TestDisabledAcceptanceSkipsVerificationButKeepsReview(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.AcceptanceStrategy = "none"
	task.Project.Integration = domain.IntegrationConfig{}
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || h.counts[domain.StageVerifying] != 0 || h.counts[domain.StageDeveloping] != 1 || h.counts[domain.StagePublishing] != 1 || h.counts[domain.StageReviewing] != 1 || h.counts[domain.StageMerging] != 1 {
		t.Fatalf("disabled acceptance changed task flow: stage=%s phases=%v", final.Stage, h.phases)
	}
}

func TestDisabledReviewSkipsIndependentSessionAndKeepsMergeGate(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.AcceptanceStrategy = "none"
	task.Project.ReviewMode = "none"
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || h.counts[domain.StageVerifying] != 0 || h.counts[domain.StageReviewing] != 0 || h.counts[domain.StagePublishing] != 1 || h.counts[domain.StageAcquiringMerge] != 1 || h.counts[domain.StageMerging] != 1 || final.ReviewDigest != domain.Digest(final.Workspace.Revisions) {
		t.Fatalf("optional review did not preserve merge checks: stage=%s phases=%v digest=%s", final.Stage, h.phases, final.ReviewDigest)
	}
}

func TestPausedCommittedWorkCanContinueAtPublishingWithoutExtraAcceptance(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Stage = domain.StageDeveloping
	task.Paused = true
	task.Project.AcceptanceStrategy = "repository"
	h.signalAfter(time.Second, domain.Command{ID: "continue-committed", Kind: "continue_committed", Text: "Existing committed work is sufficient; proceed to publishing and review"})
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || final.Paused || final.Project.AcceptanceStrategy != "none" || h.counts[domain.StageDeveloping] != 0 || h.counts[domain.StageVerifying] != 0 || h.counts[domain.StagePublishing] != 1 || h.counts[domain.StageReviewing] != 1 || h.counts[domain.StageMerging] != 1 {
		t.Fatalf("committed work did not continue safely: stage=%s phases=%v strategy=%s", final.Stage, h.phases, final.Project.AcceptanceStrategy)
	}
}

func TestPublishingBlockedOutcomeKeepsOriginalReason(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.AcceptanceStrategy = "repository"
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StagePublishing {
			out.Task.Executions["dev"] = domain.Execution{Status: "succeeded", Result: &domain.AgentResult{Outcome: "blocked", Summary: "branch not pushed", NeedsHelp: "GitLab publishing credential unavailable"}}
			out.Task.Message = "GitLab publishing credential unavailable"
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageBlocked || state.Message != "GitLab publishing credential unavailable" {
			t.Errorf("publishing reason was masked: stage=%s message=%q", state.Stage, state.Message)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel-after-block", Kind: "cancel"})
	}, time.Second)
	final := h.run(t, task)
	if final.Stage != domain.StageCancelled || h.counts[domain.StagePublishing] != 1 {
		t.Fatalf("unexpected publishing retry: stage=%s counts=%v", final.Stage, h.counts)
	}
}
func TestEvergreenRoundAutoMergesSensitiveChangeWithoutApproval(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Round = 1
	task.Workspace.Revisions[0].Sensitive = true
	task.Issue.Labels = append(task.Issue.Labels, "no-auto-close")
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || h.counts[domain.StageWaitingApproval] != 0 || h.counts[domain.StageMerging] != 1 || !strings.Contains(final.Message, "Issue 保持打开") {
		t.Fatalf("unexpected evergreen flow: %+v phases=%v", final, h.phases)
	}
}
func TestEvergreenRoundReworksMergeConflictBeforeMerging(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageAcquiringMerge && count == 1 {
			out.Ready = false
			out.Rework = true
			out.Task.Message = "STALE: merge conflict"
		}
		return out, nil
	}
	task := workflowTask()
	task.Round = 1
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || final.Reworks != 1 || h.counts[domain.StageDeveloping] != 2 || h.counts[domain.StageVerifying] != 2 || h.counts[domain.StageReviewing] != 2 || h.counts[domain.StageMerging] != 1 {
		t.Fatalf("conflict did not get reworked and rechecked: %+v %v", final, h.counts)
	}
}
func TestWorkflowClarificationAssumptionsContinue(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageDeveloping {
			out.Task.Executions["dev"].Result.Assumptions = []string{"Ambiguous naming: used existing convention"}
		}
		return out, nil
	}
	final := h.run(t, workflowTask())
	if final.Stage != domain.StageCompleted || len(final.Assumptions) != 1 || h.counts[domain.StageDeveloping] != 1 {
		t.Fatalf("%+v", final)
	}
}
func TestWorkflowReviewChangesRequestRework(t *testing.T) {
	h := newWorkflowHarness(t)
	h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageReviewing && count == 1 {
			out.Task.Executions["review"].Result.Outcome = "changes_requested"
			out.Task.Executions["review"].Result.Summary = "fix validation"
			out.Task.Executions["review"].Result.Findings = []domain.Finding{{RepositoryID: "service", Path: "main.go", Message: "validate input"}}
		}
		return out, nil
	}
	final := h.run(t, workflowTask())
	if final.Stage != domain.StageCompleted || final.Reworks != 1 || h.counts[domain.StageDeveloping] != 2 || h.counts[domain.StageReviewing] != 2 || len(final.Notes) != 2 {
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
		if state.Stage != domain.StageWaitingApproval || state.ApprovalDigest != "" {
			t.Errorf("wrong approval accepted: %+v", state)
		}
	}, 2*time.Second)
	h.signalAfter(3*time.Second, domain.Command{ID: "correct", Kind: "approve", Digest: digest})
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || final.ApprovalDigest != digest {
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
	if final.Reworks != 1 || h.counts[domain.StageDeveloping] != 2 || final.Stage != domain.StageCompleted {
		t.Fatalf("%+v %v", final, h.counts)
	}
}
func TestWorkflowBlockedRetryAndTransientErrors(t *testing.T) {
	t.Run("transient activity retry", func(t *testing.T) {
		h := newWorkflowHarness(t)
		h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
			if input.Phase == domain.StagePreparing && count < 3 {
				return StepResult{}, errors.New("temporary transport error")
			}
			return successfulStep(input), nil
		}
		final := h.run(t, workflowTask())
		if final.Stage != domain.StageCompleted || h.counts[domain.StagePreparing] != 3 {
			t.Fatalf("%+v %v", final, h.counts)
		}
	})
	t.Run("blocked business retry", func(t *testing.T) {
		h := newWorkflowHarness(t)
		h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
			out := successfulStep(input)
			if input.Phase == domain.StageDeveloping && count == 1 {
				out.Task.Executions["dev"].Result.Outcome = "blocked"
				out.Task.Executions["dev"].Result.NeedsHelp = "missing access"
			}
			return out, nil
		}
		h.env.RegisterDelayedCallback(func() {
			if got := h.state(t); got.Stage != domain.StageBlocked {
				t.Errorf("expected blocked, got %s", got.Stage)
			}
			h.env.SignalWorkflow(SignalName, domain.Command{ID: "retry", Kind: "retry"})
		}, time.Second)
		final := h.run(t, workflowTask())
		if final.Stage != domain.StageCompleted || h.counts[domain.StageDeveloping] != 2 {
			t.Fatalf("%+v %v", final, h.counts)
		}
	})
	t.Run("nonretryable operation needs explicit retry", func(t *testing.T) {
		h := newWorkflowHarness(t)
		h.step = func(_ context.Context, input StepInput, count int) (StepResult, error) {
			if input.Phase == domain.StagePreparing && count == 1 {
				return StepResult{}, temporal.NewNonRetryableApplicationError("invalid config", "configuration", nil)
			}
			return successfulStep(input), nil
		}
		h.signalAfter(time.Second, domain.Command{ID: "retry", Kind: "retry"})
		final := h.run(t, workflowTask())
		if final.Stage != domain.StageCompleted || h.counts[domain.StagePreparing] != 2 {
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
		if input.Phase == domain.StageDeveloping {
			out.Task.Executions["dev"].Result.Outcome = "no_change"
		}
		return out, nil
	}
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || h.counts[domain.StageReviewing] != 1 || h.counts[domain.StageMerging] != 0 || h.counts[domain.StageFinalizing] != 1 {
		t.Fatalf("%+v %v", final, h.counts)
	}
}
func TestWorkflowDuplicateCommandsAndDependenciesWait(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Dependencies = []domain.IssueRef{{ProjectID: 2, IID: 3}}
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageWaitingDependencies {
			out.Ready = len(input.Task.Dependencies) == 0
		}
		return out, nil
	}
	note := domain.Command{ID: "same-note", Kind: "note", Text: "context does not block"}
	h.signalAfter(time.Second, note)
	h.signalAfter(2*time.Second, note)
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageWaitingDependencies || len(state.Notes) != 1 {
			t.Errorf("dependency wait/dedup failed: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "clear-dependencies", Kind: "dependencies", Dependencies: []domain.IssueRef{}})
	}, 3*time.Second)
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || len(final.Notes) != 1 || len(final.Dependencies) != 0 {
		t.Fatalf("%+v", final)
	}
}
func TestWorkflowPauseResumeAndCancellationDuringActivity(t *testing.T) {
	for _, kind := range []string{"pause", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			h := newWorkflowHarness(t)
			task := workflowTask()
			h.env.OnActivity("Step", mock.Anything, mock.MatchedBy(func(input StepInput) bool { return input.Phase == domain.StageDeveloping })).After(time.Hour).Return(func(_ context.Context, input StepInput) (StepResult, error) { return successfulStep(input), nil }).Once()
			h.env.OnActivity("Step", mock.Anything, mock.Anything).Return(func(_ context.Context, input StepInput) (StepResult, error) {
				h.mu.Lock()
				h.phases = append(h.phases, input.Phase)
				h.counts[input.Phase]++
				h.mu.Unlock()
				if input.Phase == phaseRecover {
					input.Task.Executions["dev"] = domain.Execution{SessionID: "ses_exact_resume", Status: "cancelled"}
					return StepResult{Task: input.Task, Ready: true}, nil
				}
				if input.Phase == domain.StageDeveloping && input.Task.Executions["dev"].SessionID != "ses_exact_resume" {
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
				if final.Stage != domain.StageCancelled || h.counts[phaseStop] != 1 || h.counts[domain.StageMerging] != 0 {
					t.Fatalf("%+v %v", final, h.counts)
				}
			} else if final.Stage != domain.StageCompleted || final.Paused {
				t.Fatalf("%+v", final)
			}
			if h.counts[phaseRecover] != 1 {
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
		if state.Stage != domain.StageBlocked {
			t.Errorf("manual rejection exceeded rework budget but stage=%s reworks=%d", state.Stage, state.Reworks)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel", Kind: "cancel"})
	}, 2*time.Second)
	final := h.run(t, task)
	if final.Stage != domain.StageCancelled {
		t.Fatal(final.Stage)
	}
	if h.counts[domain.StageDeveloping] != 1 {
		t.Errorf("development reran beyond configured budget: %d", h.counts[domain.StageDeveloping])
	}
}

func TestWorkflowNoAutoCloseStillNeedsApprovalWithoutChanges(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Issue.Labels = append(task.Issue.Labels, "no-auto-close")
	task.Workspace.Revisions[0].Changed = false
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageDeveloping {
			out.Task.Executions["dev"].Result.Outcome = "no_change"
		}
		return out, nil
	}
	digest := domain.Digest(task.Workspace.Revisions)
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageWaitingApproval {
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
		case domain.StageMerging:
			out.Task.Workspace.Revisions[0].Merged = true
			out.Ready = false
		case domain.StagePartialMerge:
			if !input.Task.Workspace.Revisions[0].Merged {
				t.Error("partial merge retry lost completed repository")
			}
			out.Task.Workspace.Revisions[1].Merged = true
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageBlocked || !state.Workspace.Revisions[0].Merged {
			t.Errorf("partial merge not retained: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "retry-remaining", Kind: "retry"})
	}, time.Second)
	final := h.run(t, task)
	if final.Stage != domain.StageCompleted || h.counts[domain.StageDeveloping] != 1 || h.counts[domain.StageMerging] != 1 || h.counts[domain.StagePartialMerge] != 1 {
		t.Fatalf("final=%+v phases=%v", final, h.counts)
	}
}

func TestWorkflowRequiredVerificationFailureConsumesReworkBudget(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.MaxReworks = 1
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageVerifying {
			out.Ready = false
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageBlocked || state.Reworks != 2 {
			t.Errorf("required verification failed without bounded rework: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel", Kind: "cancel"})
	}, time.Second)
	final := h.run(t, task)
	if final.Stage != domain.StageCancelled || h.counts[domain.StageDeveloping] != 2 || h.counts[domain.StagePublishing] != 0 || h.counts[domain.StageMerging] != 0 {
		t.Fatalf("final=%+v phases=%v", final, h.counts)
	}
}

func TestWorkflowLongDependencyWaitContinuesWithDurableCheckpoint(t *testing.T) {
	first := newWorkflowHarness(t)
	task := workflowTask()
	task.Dependencies = []domain.IssueRef{{ProjectID: 9, IID: 7}}
	first.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == domain.StageWaitingDependencies {
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
	if next.Stage != domain.StageWaitingDependencies || next.Checkpoint == nil {
		t.Fatalf("checkpoint lost waiting stage: %+v", next)
	}
	checkpoint := next.Checkpoint
	if checkpoint.Serial != 500 || checkpoint.PreviousPhase != domain.StageWaitingDependencies || !checkpoint.Seen[note.ID] || len(next.Notes) != 1 {
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
		if input.Phase == domain.StageWaitingDependencies {
			out.Ready = len(input.Task.Dependencies) == 0
		}
		return out, nil
	}
	second.signalAfter(time.Second, note)
	second.signalAfter(2*time.Second, domain.Command{ID: "unblock", Kind: "dependencies", Dependencies: []domain.IssueRef{}})
	final := second.run(t, next)
	if final.Stage != domain.StageCompleted || len(final.Notes) != 1 {
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
		if input.Phase == phaseStop {
			return StepResult{}, temporal.NewNonRetryableApplicationError("container still running", "stop_failed", nil)
		}
		out := successfulStep(input)
		if input.Phase == domain.StageWaitingDependencies {
			out.Ready = false
		}
		return out, nil
	}
	h.signalAfter(time.Second, domain.Command{ID: "cancel-first", Kind: "cancel"})
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageBlocked {
			t.Errorf("failed stop falsely reported terminal stage: %s", state.Stage)
		}
		h.mu.Lock()
		for _, saved := range h.saved {
			if saved.Stage == domain.StageCancelled {
				t.Error("cancelled persisted before successful stop")
			}
		}
		h.mu.Unlock()
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "retry-stop", Kind: "retry"})
	}, 2*time.Second)
	final := h.run(t, workflowTask())
	if final.Stage != domain.StageCancelled || h.counts[phaseStop] != 1 || h.counts[phaseStopping] != 1 || h.counts[domain.StageDeveloping] != 0 {
		t.Fatalf("final=%+v phases=%v", final, h.counts)
	}
}
func TestWorkflowRapidPauseResumeRetainsActivePhase(t *testing.T) {
	h := newWorkflowHarness(t)
	h.env.OnActivity("Step", mock.Anything, mock.MatchedBy(func(in StepInput) bool { return in.Phase == domain.StageDeveloping })).After(time.Hour).Return(func(_ context.Context, in StepInput) (StepResult, error) { return successfulStep(in), nil }).Once()
	recovered := false
	h.env.OnActivity("Step", mock.Anything, mock.Anything).Return(func(_ context.Context, in StepInput) (StepResult, error) {
		if in.Phase == phaseRecover {
			recovered = true
			in.Task.Executions["dev"] = domain.Execution{Status: "cancelled", SessionID: "ses_rapid_pause"}
			return StepResult{Task: in.Task, Ready: true}, nil
		}
		if in.Phase == domain.StageDeveloping && in.Task.Executions["dev"].SessionID != "ses_rapid_pause" {
			t.Error("rapid resume lost exact recovered session")
		}
		return successfulStep(in), nil
	})
	h.env.RegisterDelayedCallback(func() {
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "pause", Kind: "pause"})
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "resume", Kind: "resume"})
	}, time.Second)
	final := h.run(t, workflowTask())
	if final.Stage != domain.StageCompleted || final.Paused || !recovered {
		t.Fatalf("rapid pause/resume did not recover: %+v recovered=%v", final, recovered)
	}
	for _, saved := range h.saved {
		if saved.Stage == domain.StageBlocked {
			t.Fatalf("rapid pause/resume became blocked: %+v", saved)
		}
	}
}
func TestWorkflowCheckpointPreservesBoundedCIWait(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Stage = domain.StageAcquiringMerge
	task.Checkpoint = &domain.WorkflowCheckpoint{Serial: 900, PreviousPhase: domain.StageAcquiringMerge, CIWaits: 179, Seen: map[string]bool{}}
	h.step = func(_ context.Context, in StepInput, _ int) (StepResult, error) {
		out := successfulStep(in)
		if in.Phase == domain.StageAcquiringMerge {
			out.Ready = false
			out.Task.Message = "等待 GitLab CI 或合并检查"
		}
		return out, nil
	}
	h.env.RegisterDelayedCallback(func() {
		state := h.state(t)
		if state.Stage != domain.StageBlocked {
			t.Errorf("CI wait budget reset at checkpoint: %+v", state)
		}
		h.env.SignalWorkflow(SignalName, domain.Command{ID: "cancel", Kind: "cancel"})
	}, time.Second)
	final := h.run(t, task)
	if final.Stage != domain.StageCancelled || h.counts[domain.StageAcquiringMerge] != 1 {
		t.Fatalf("unbounded CI wait after restart: final=%s checks=%d", final.Stage, h.counts[domain.StageAcquiringMerge])
	}
}
