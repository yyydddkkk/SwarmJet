// Package pipeline owns business sequencing. All I/O lives in Activities.
package pipeline

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const Queue = "swarmjet"
const WorkflowName = "WorkTask"
const SignalName = "command"

// Control phases are StepInput.Phase values that are not task stages. They
// orchestrate recovery and cancellation around a stage Activity.
const (
	phaseStop               = "stop"
	phaseStopping           = "stopping"
	phaseRecover            = "recover"
	phaseRecoverManualMerge = "recover_manual_merge"
	phasePauseQueue         = "pause_queue"
	phaseCancel             = "cancel"
	phaseFinalize           = "finalize"
)

type StepInput struct {
	Phase       string
	Task        domain.Task
	ExecutionID string
}
type StepResult struct {
	Task    domain.Task
	Ready   bool
	Rework  bool
	Waiting bool
}

// workTask holds the state of one WorkTask run. The fields listed under the
// checkpoint group are serialized into the Continue-As-New checkpoint; the rest
// is rebuilt from the version gates and the signal channel on every run.
type workTask struct {
	ctx      workflow.Context
	task     domain.Task
	commands workflow.ReceiveChannel

	// Checkpointed state.
	serial        int
	previousPhase string
	seen          map[string]bool
	retry         bool
	rejected      bool
	cancelled     bool
	ciWaits       int

	// Version gates and the project switches they derive.
	autoMergeVersion workflow.Version
	evergreen        bool
	skipAcceptance   bool
	reviewEnabled    bool
	earlyPublish     bool

	// Coordination between the background command handler and the run loop.
	controlVersion          int
	autoMergeControlVersion int
	activeCancel            workflow.CancelFunc
	interrupted             bool
}

// WorkTask is the durable workflow for one task. It only wires the pieces
// together; each phase advances in workflow_stages.go and the state lives in
// workTask.
func WorkTask(ctx workflow.Context, t domain.Task) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Hour, HeartbeatTimeout: time.Minute, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute, MaximumAttempts: 3}})
	w := &workTask{ctx: ctx, task: t}
	w.initGates()
	w.commands = workflow.GetSignalChannel(ctx, SignalName)
	w.restore()
	w.startCommandLoop()
	return w.run()
}

// initGates reads the workflow version markers. Their order, change IDs and
// bounds are part of the saved-history contract and must not change.
func (w *workTask) initGates() {
	// Version gate for the project-level auto-merge switch. Executions saved
	// before this change replay with the previous decision order; new executions
	// honor the switch. DefaultVersion is the pre-switch behavior.
	w.autoMergeVersion = workflow.GetVersion(w.ctx, "auto-merge-switch", workflow.DefaultVersion, 1)
	evergreenVersion := workflow.GetVersion(w.ctx, "evergreen-issue", workflow.DefaultVersion, 1)
	w.evergreen = evergreenVersion >= 1 && w.task.Round > 0
	acceptanceVersion := workflow.GetVersion(w.ctx, "acceptance-strategy", workflow.DefaultVersion, 1)
	w.skipAcceptance = w.task.Project.AcceptanceStrategy == "none"
	w.reviewEnabled = w.task.Project.IndependentReview()
	w.earlyPublish = acceptanceVersion >= 1 && w.task.Project.AcceptanceStrategy != "" && !w.skipAcceptance
}

// restore loads the checkpoint left by a Continue-As-New or a worker restart.
func (w *workTask) restore() {
	w.seen = map[string]bool{}
	if c := w.task.Checkpoint; c != nil {
		w.serial = c.Serial
		w.previousPhase = c.PreviousPhase
		if c.Seen != nil {
			w.seen = c.Seen
		}
		w.retry = c.Retry
		w.rejected = c.Rejected
		w.cancelled = c.Cancelled
		w.ciWaits = c.CIWaits
	}
	w.task.Checkpoint = nil
}

// startCommandLoop runs the background signal handler and exposes the state
// query. Commands are applied while the run loop is parked on a timer.
func (w *workTask) startCommandLoop() {
	workflow.Go(w.ctx, func(ctx workflow.Context) {
		for {
			var c domain.Command
			w.commands.Receive(ctx, &c)
			w.applyCommand(c)
		}
	})
	_ = workflow.SetQueryHandler(w.ctx, "state", func() (domain.Task, error) { return w.task, nil })
}

// applyCommand folds one signal command into the live task state. It is
// idempotent per command ID because the signal channel replays on restart.
func (w *workTask) applyCommand(c domain.Command) {
	if w.seen[c.ID] {
		return
	}
	w.seen[c.ID] = true
	w.controlVersion++
	switch c.Kind {
	case "pause":
		w.task.Paused = true
		if w.activeCancel != nil {
			w.interrupted = true
			w.activeCancel()
		}
	case "resume":
		w.task.Paused = false
	case "continue_committed":
		if w.task.Paused && w.task.Stage == domain.StageDeveloping {
			if w.activeCancel != nil {
				w.interrupted = true
				w.activeCancel()
			}
			w.task.Project.AcceptanceStrategy = "none"
			w.skipAcceptance = true
			w.task.AcceptanceCommands = nil
			w.task.Verification = nil
			w.task.AcceptanceIncomplete = false
			w.task.Notes = append(w.task.Notes, "管理员确认已有提交，直接进入发布与审查："+c.Text)
			w.task.Message = "已确认现有提交，跳过重复开发和额外验收，进入发布与审查"
			w.task.Stage = domain.StagePublishing
			w.task.Paused = false
		}
	case "set_auto_merge":
		if c.AutoMerge == nil || w.task.Stage == domain.StageFinalizing || w.task.Stage == domain.StageCompleted || w.task.Stage == domain.StageCancelled || w.task.Stage == domain.StagePartialMerge || w.task.Stage == domain.StageManualMerging {
			break
		}
		if w.task.Stage == domain.StageMerging && *c.AutoMerge {
			break
		}
		value := *c.AutoMerge
		w.task.AutoMergeOverride = &value
		w.autoMergeControlVersion++
		if value {
			w.task.Notes = append(w.task.Notes, "管理员开启了此任务自动合并")
		} else {
			w.task.Notes = append(w.task.Notes, "管理员关闭了此任务自动合并；合并阶段等待人工操作")
		}
		if !value && (w.task.Stage == domain.StageAcquiringMerge || w.task.Stage == domain.StageMerging) {
			if w.activeCancel != nil {
				w.interrupted = true
				w.activeCancel()
			}
			w.task.Stage = domain.StageWaitingManualMerge
		} else if !value && w.task.Stage == domain.StageWaitingApproval {
			w.task.Stage = domain.StageWaitingManualMerge
		}
	case "merge_round":
		if w.task.Project.HostingProvider() == domain.ProviderGitLab && w.task.Stage == domain.StageWaitingManualMerge && !w.task.Paused {
			disabled := false
			w.task.AutoMergeOverride = &disabled
			w.autoMergeControlVersion++
			w.task.Stage = domain.StageManualMerging
			w.task.Message = "正在按人工验收记录合并整轮 MR"
			if w.activeCancel != nil {
				w.interrupted = true
				w.activeCancel()
			}
		}
	case "cancel":
		w.cancelled = true
		if w.activeCancel != nil {
			w.activeCancel()
		}
	case "retry":
		w.retry = true
	case "note":
		w.task.Notes = append(w.task.Notes, c.Text)
	case "approve":
		if w.task.Stage == domain.StageWaitingApproval && c.Digest != "" && c.Digest == w.task.ReviewDigest {
			w.task.ApprovalDigest = c.Digest
		}
	case "reject":
		if w.task.Stage == domain.StageWaitingApproval {
			w.task.Notes = append(w.task.Notes, "人工审核要求修改: "+c.Text)
			w.rejected = true
		}
	case "dependencies":
		w.task.Dependencies = c.Dependencies
	}
}

func (w *workTask) persist() error {
	saveCtx := workflow.WithActivityOptions(w.ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute}})
	return workflow.ExecuteActivity(saveCtx, "SaveTask", w.task).Get(saveCtx, nil)
}

func (w *workTask) waitTick(d time.Duration) {
	timerCtx, cancel := workflow.WithCancel(w.ctx)
	defer cancel()
	v := w.controlVersion
	f := workflow.NewTimer(timerCtx, d)
	_ = workflow.Await(timerCtx, func() bool { return f.IsReady() || w.cancelled || w.task.Paused || w.controlVersion != v })
}

// wait yields through durable timers while the background signal handler
// applies commands.
func (w *workTask) wait(test func() bool) error {
	return workflow.Await(w.ctx, func() bool { return w.cancelled || test() })
}

// advanceAfterReview decides the phase after the revision set is fixed.
func (w *workTask) advanceAfterReview() {
	// ReviewDigest pins the exact revision set for later merge checks. When
	// independent review is disabled it is still a revision checkpoint.
	w.task.ReviewDigest = domain.Digest(w.task.Workspace.Revisions)
	special := false
	changed := false
	for _, r := range w.task.Workspace.Revisions {
		special = special || r.Sensitive
		changed = changed || r.Changed
	}
	for _, label := range w.task.Issue.Labels {
		special = special || label == "no-auto-close"
	}
	if w.autoMergeVersion >= 1 && (!w.task.AutoMergeEnabled() || w.task.AcceptanceIncomplete) {
		if !changed {
			w.task.Stage = domain.StageFinalizing
		} else {
			w.task.Message = "等待真人验收并人工合并全部 MR"
			if w.task.AcceptanceIncomplete {
				w.task.Message = "自动验收不完整，禁止自动合并；请按 MR 顶部清单人工验收"
			}
			w.task.Stage = domain.StageWaitingManualMerge
		}
	} else if !w.evergreen && special && w.task.ApprovalDigest != w.task.ReviewDigest {
		w.task.Stage = domain.StageWaitingApproval
	} else if !changed {
		w.task.Stage = domain.StageFinalizing
	} else {
		w.task.Stage = domain.StageAcquiringMerge
	}
}

// loopAction tells the run loop how a helper ended its iteration. The values
// mirror the continue/return statements of the original inline loop.
type loopAction int

const (
	// loopNext evaluates the next run-loop check.
	loopNext loopAction = iota
	// loopRestart restarts the loop, mirroring a continue.
	loopRestart
	// loopFinish returns err from WorkTask.
	loopFinish
)

// run drives one step per iteration until the workflow finishes or must
// Continue-As-New. Each helper reports loopFinish when WorkTask has to return
// its error as the final result.
func (w *workTask) run() error {
	steps := 0
	for {
		steps++
		if steps > 500 {
			return w.continueAsNew()
		}
		switch action, err := w.handleCancelled(); action {
		case loopFinish:
			return err
		case loopRestart:
			continue
		}
		switch action, err := w.handlePaused(); action {
		case loopFinish:
			return err
		case loopRestart:
			continue
		}
		switch action, err := w.handleBlocked(); action {
		case loopFinish:
			return err
		case loopRestart:
			continue
		}
		switch action, err := w.handleWaitingApproval(); action {
		case loopFinish:
			return err
		case loopRestart:
			continue
		}
		if w.task.Stage == domain.StageCompleted {
			return w.persist()
		}
		if w.task.Stage == domain.StageQueued {
			w.task.Stage = domain.StageWaitingDependencies
		}
		w.previousPhase = w.task.Stage
		if action, err := w.runStep(); action == loopFinish {
			return err
		}
	}
}

// continueAsNew drains pending commands, checkpoints the live state, and starts
// a fresh run with the same task snapshot.
func (w *workTask) continueAsNew() error {
	var c domain.Command
	for w.commands.ReceiveAsync(&c) {
		w.applyCommand(c)
	}
	w.task.Checkpoint = &domain.WorkflowCheckpoint{Serial: w.serial, PreviousPhase: w.previousPhase, Seen: w.seen, Retry: w.retry, Rejected: w.rejected, Cancelled: w.cancelled, CIWaits: w.ciWaits}
	return workflow.NewContinueAsNewError(w.ctx, WorkflowName, w.task)
}

// handleCancelled runs the stop Activity once a cancel command was applied.
func (w *workTask) handleCancelled() (loopAction, error) {
	if !w.cancelled {
		return loopNext, nil
	}
	var result StepResult
	if err := workflow.ExecuteActivity(w.ctx, "Step", StepInput{Phase: phaseStop, Task: w.task}).Get(w.ctx, &result); err != nil {
		w.cancelled = false
		w.task.Stage = domain.StageBlocked
		w.task.Paused = false
		w.task.Message = "停止执行失败，请重试取消或检查现场：" + err.Error()
		w.previousPhase = phaseStopping
		return loopRestart, nil
	}
	w.task.Stage = domain.StageCancelled
	w.task.Message = "任务已取消；远端分支、MR 与现场保留"
	return loopFinish, w.persist()
}

// handlePaused parks the workflow while the task is paused, first undoing a
// GitHub merge-queue entry so nothing merges behind the operator's back.
func (w *workTask) handlePaused() (loopAction, error) {
	if !w.task.Paused {
		return loopNext, nil
	}
	if w.task.Project.HostingProvider() == domain.ProviderGitHub && (w.task.Stage == domain.StageMerging || w.task.Stage == domain.StagePartialMerge) {
		var paused StepResult
		if err := workflow.ExecuteActivity(w.ctx, "Step", StepInput{Phase: phasePauseQueue, Task: w.task}).Get(w.ctx, &paused); err != nil {
			w.task.Paused = false
			w.task.Stage = domain.StageBlocked
			w.task.Message = "无法撤销 GitHub merge queue；请核实远端状态：" + err.Error()
			w.previousPhase = domain.StagePartialMerge
			return loopRestart, nil
		}
		w.task = paused.Task
	}
	if err := w.persist(); err != nil {
		return loopFinish, err
	}
	v := w.controlVersion
	if err := w.wait(func() bool { return !w.task.Paused || w.controlVersion != v }); err != nil {
		return loopFinish, err
	}
	return loopRestart, nil
}

// handleBlocked waits for a retry command and then resumes the phase that
// failed.
func (w *workTask) handleBlocked() (loopAction, error) {
	if w.task.Stage != domain.StageBlocked {
		return loopNext, nil
	}
	if err := w.persist(); err != nil {
		return loopFinish, err
	}
	v := w.controlVersion
	if err := w.wait(func() bool { return w.retry || w.task.Paused || w.controlVersion != v }); err != nil {
		return loopFinish, err
	}
	if w.retry {
		w.retry = false
		w.task.Stage = w.previousPhase
	}
	return loopRestart, nil
}

// handleWaitingApproval waits for an approval digest or a rejection and routes
// the task back to development or on to merging.
func (w *workTask) handleWaitingApproval() (loopAction, error) {
	if w.task.Stage != domain.StageWaitingApproval {
		return loopNext, nil
	}
	if err := w.persist(); err != nil {
		return loopFinish, err
	}
	v := w.controlVersion
	if err := w.wait(func() bool {
		return w.task.ApprovalDigest == w.task.ReviewDigest && w.task.ReviewDigest != "" || w.rejected || w.task.Paused || w.controlVersion != v
	}); err != nil {
		return loopFinish, err
	}
	if w.cancelled || w.task.Paused {
		return loopRestart, nil
	}
	if w.rejected {
		w.rejected = false
		w.task.ApprovalDigest = ""
		w.task.ReviewDigest = ""
		w.task.Reworks++
		w.previousPhase = domain.StageDeveloping
		if w.task.Reworks > w.task.Project.MaxReworks {
			w.task.Stage = domain.StageBlocked
			w.task.Message = "人工审核返工达到上限"
		} else {
			w.task.Stage = domain.StageDeveloping
		}
	} else if w.task.ApprovalDigest == w.task.ReviewDigest && w.task.ReviewDigest != "" {
		w.task.Stage = domain.StageAcquiringMerge
	}
	return loopRestart, nil
}

// runStep persists the current stage, executes its Step Activity, recovers from
// interruption or failure, and hands the result to dispatchPhase.
func (w *workTask) runStep() (loopAction, error) {
	if err := w.persist(); err != nil {
		return loopFinish, err
	}
	w.serial++
	op := fmt.Sprintf("%s-%s-%d", w.task.ID, w.task.Stage, w.serial)
	child, cancel := workflow.WithCancel(w.ctx)
	w.interrupted = false
	w.activeCancel = cancel
	mergeControlAtStart := w.autoMergeControlVersion
	var result StepResult
	err := workflow.ExecuteActivity(child, "Step", StepInput{Phase: w.task.Stage, Task: w.task, ExecutionID: op}).Get(child, &result)
	w.activeCancel = nil
	cancel()
	if w.cancelled || w.task.Paused || w.interrupted {
		if e := w.recoverAfterInterrupt(op); e != nil {
			return loopFinish, e
		}
		return loopRestart, nil
	}
	if err != nil {
		if e := w.recoverAfterFailure(op); e != nil {
			return loopFinish, e
		}
		w.task.Stage = domain.StageBlocked
		w.task.Message = err.Error()
		return loopRestart, nil
	}
	// Notes/signals received during an Activity belong to the live workflow state.
	notes, deps, approval, taskAutoMerge := w.task.Notes, w.task.Dependencies, w.task.ApprovalDigest, w.task.AutoMergeOverride
	w.task = result.Task
	w.task.Notes = notes
	w.task.Dependencies = deps
	w.task.ApprovalDigest = approval
	if w.autoMergeControlVersion != mergeControlAtStart {
		w.task.AutoMergeOverride = taskAutoMerge
	}
	if result.Rework {
		w.task.Reworks++
		w.task.ApprovalDigest = ""
		w.task.ReviewDigest = ""
		w.task.Notes = append(w.task.Notes, w.task.Message)
		if w.task.Reworks > w.task.Project.MaxReworks {
			w.task.Stage = domain.StageBlocked
			w.previousPhase = domain.StageDeveloping
		} else {
			w.task.Stage = domain.StageDeveloping
		}
		return loopRestart, nil
	}
	return w.dispatchPhase(w.previousPhase, result)
}

// recoverAfterInterrupt reconciles the interrupted execution before the run
// loop parks on the pause state.
func (w *workTask) recoverAfterInterrupt(op string) error {
	var recovered StepResult
	if w.previousPhase == domain.StageManualMerging {
		if e := workflow.ExecuteActivity(w.ctx, "Step", StepInput{Phase: phaseRecoverManualMerge, Task: w.task, ExecutionID: op}).Get(w.ctx, &recovered); e != nil {
			return e
		}
		w.task.Workspace = recovered.Task.Workspace
		return nil
	}
	// Read the durable execution after cancellation to retain an exact resume session.
	if e := workflow.ExecuteActivity(w.ctx, "Step", StepInput{Phase: phaseRecover, Task: w.task, ExecutionID: op}).Get(w.ctx, &recovered); e == nil {
		w.task.Executions = recovered.Task.Executions
	} else {
		w.task.Paused = false
		w.task.Stage = domain.StageBlocked
		w.task.Message = "无法确认执行已停止：" + e.Error()
	}
	return nil
}

// recoverAfterFailure reclaims any state the failed Activity left behind.
func (w *workTask) recoverAfterFailure(op string) error {
	var recovered StepResult
	if w.previousPhase == domain.StageManualMerging {
		if e := workflow.ExecuteActivity(w.ctx, "Step", StepInput{Phase: phaseRecoverManualMerge, Task: w.task, ExecutionID: op}).Get(w.ctx, &recovered); e != nil {
			return e
		}
		w.task.Workspace = recovered.Task.Workspace
	} else if e := workflow.ExecuteActivity(w.ctx, "Step", StepInput{Phase: phaseRecover, Task: w.task, ExecutionID: op}).Get(w.ctx, &recovered); e == nil && recovered.Task.Executions != nil {
		w.task.Executions = recovered.Task.Executions
	}
	return nil
}
