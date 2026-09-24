// Package pipeline owns business sequencing. All I/O lives in Activities.
package pipeline

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"swarmjet/internal/domain"
)

const Queue = "swarmjet"
const WorkflowName = "WorkTask"
const SignalName = "command"

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

func WorkTask(ctx workflow.Context, t domain.Task) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Hour, HeartbeatTimeout: time.Minute, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute, MaximumAttempts: 3}})
	// Version gate for the project-level auto-merge switch. Executions saved
	// before this change replay with the previous decision order; new executions
	// honor the switch. DefaultVersion is the pre-switch behavior.
	autoMergeVersion := workflow.GetVersion(ctx, "auto-merge-switch", workflow.DefaultVersion, 1)
	evergreenVersion := workflow.GetVersion(ctx, "evergreen-issue", workflow.DefaultVersion, 1)
	evergreen := evergreenVersion >= 1 && t.Round > 0
	acceptanceVersion := workflow.GetVersion(ctx, "acceptance-strategy", workflow.DefaultVersion, 1)
	skipAcceptance := t.Project.AcceptanceStrategy == "none"
	reviewEnabled := t.Project.IndependentReview()
	earlyPublish := acceptanceVersion >= 1 && t.Project.AcceptanceStrategy != "" && !skipAcceptance
	commands := workflow.GetSignalChannel(ctx, SignalName)
	seen := map[string]bool{}
	cancelled := false
	retry := false
	rejected := false
	var serial int
	var previousPhase string
	ciWaits := 0
	if c := t.Checkpoint; c != nil {
		serial = c.Serial
		previousPhase = c.PreviousPhase
		if c.Seen != nil {
			seen = c.Seen
		}
		retry = c.Retry
		rejected = c.Rejected
		cancelled = c.Cancelled
		ciWaits = c.CIWaits
	}
	t.Checkpoint = nil
	var activeCancel workflow.CancelFunc
	interrupted := false
	controlVersion := 0
	autoMergeControlVersion := 0
	applyCommand := func(c domain.Command) {
		if seen[c.ID] {
			return
		}
		seen[c.ID] = true
		controlVersion++
		switch c.Kind {
		case "pause":
			t.Paused = true
			if activeCancel != nil {
				interrupted = true
				activeCancel()
			}
		case "resume":
			t.Paused = false
		case "continue_committed":
			if t.Paused && t.Stage == "developing" {
				if activeCancel != nil {
					interrupted = true
					activeCancel()
				}
				t.Project.AcceptanceStrategy = "none"
				skipAcceptance = true
				t.AcceptanceCommands = nil
				t.Verification = nil
				t.AcceptanceIncomplete = false
				t.Notes = append(t.Notes, "管理员确认已有提交，直接进入发布与审查："+c.Text)
				t.Message = "已确认现有提交，跳过重复开发和额外验收，进入发布与审查"
				t.Stage = "publishing"
				t.Paused = false
			}
		case "set_auto_merge":
			if c.AutoMerge == nil || t.Stage == "finalizing" || t.Stage == "completed" || t.Stage == "cancelled" || t.Stage == "partial_merge" {
				break
			}
			if t.Stage == "merging" && *c.AutoMerge {
				break
			}
			value := *c.AutoMerge
			t.AutoMergeOverride = &value
			autoMergeControlVersion++
			if value {
				t.Notes = append(t.Notes, "管理员开启了此任务自动合并")
			} else {
				t.Notes = append(t.Notes, "管理员关闭了此任务自动合并；合并阶段等待人工操作")
			}
			if !value && (t.Stage == "acquiring_merge" || t.Stage == "merging") {
				if activeCancel != nil {
					interrupted = true
					activeCancel()
				}
				t.Stage = "waiting_manual_merge"
			} else if !value && t.Stage == "waiting_approval" {
				t.Stage = "waiting_manual_merge"
			}
		case "cancel":
			cancelled = true
			if activeCancel != nil {
				activeCancel()
			}
		case "retry":
			retry = true
		case "note":
			t.Notes = append(t.Notes, c.Text)
		case "approve":
			if t.Stage == "waiting_approval" && c.Digest != "" && c.Digest == t.ReviewDigest {
				t.ApprovalDigest = c.Digest
			}
		case "reject":
			if t.Stage == "waiting_approval" {
				t.Notes = append(t.Notes, "人工审核要求修改: "+c.Text)
				rejected = true
			}
		case "dependencies":
			t.Dependencies = c.Dependencies
		}
	}
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var c domain.Command
			commands.Receive(ctx, &c)
			applyCommand(c)
		}
	})
	_ = workflow.SetQueryHandler(ctx, "state", func() (domain.Task, error) { return t, nil })
	persist := func() error {
		saveCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute}})
		return workflow.ExecuteActivity(saveCtx, "SaveTask", t).Get(saveCtx, nil)
	}
	waitTick := func(d time.Duration) {
		timerCtx, cancel := workflow.WithCancel(ctx)
		defer cancel()
		v := controlVersion
		f := workflow.NewTimer(timerCtx, d)
		_ = workflow.Await(timerCtx, func() bool { return f.IsReady() || cancelled || t.Paused || controlVersion != v })
	}
	// Yield through durable timers while the background signal handler applies commands.
	wait := func(test func() bool) error { return workflow.Await(ctx, func() bool { return cancelled || test() }) }
	advanceAfterReview := func() {
		// ReviewDigest pins the exact revision set for later merge checks. When
		// independent review is disabled it is still a revision checkpoint.
		t.ReviewDigest = domain.Digest(t.Workspace.Revisions)
		special := false
		changed := false
		for _, r := range t.Workspace.Revisions {
			special = special || r.Sensitive
			changed = changed || r.Changed
		}
		for _, label := range t.Issue.Labels {
			special = special || label == "no-auto-close"
		}
		if autoMergeVersion >= 1 && (!t.AutoMergeEnabled() || t.AcceptanceIncomplete) {
			if !changed {
				t.Stage = "finalizing"
			} else {
				t.Message = "等待真人验收并人工合并全部 MR"
				if t.AcceptanceIncomplete {
					t.Message = "自动验收不完整，禁止自动合并；请按 MR 顶部清单人工验收"
				}
				t.Stage = "waiting_manual_merge"
			}
		} else if !evergreen && special && t.ApprovalDigest != t.ReviewDigest {
			t.Stage = "waiting_approval"
		} else if !changed {
			t.Stage = "finalizing"
		} else {
			t.Stage = "acquiring_merge"
		}
	}
	steps := 0
	for {
		steps++
		if steps > 500 {
			var c domain.Command
			for commands.ReceiveAsync(&c) {
				applyCommand(c)
			}
			t.Checkpoint = &domain.WorkflowCheckpoint{Serial: serial, PreviousPhase: previousPhase, Seen: seen, Retry: retry, Rejected: rejected, Cancelled: cancelled, CIWaits: ciWaits}
			return workflow.NewContinueAsNewError(ctx, WorkflowName, t)
		}
		if cancelled {
			var result StepResult
			if err := workflow.ExecuteActivity(ctx, "Step", StepInput{Phase: "stop", Task: t}).Get(ctx, &result); err != nil {
				cancelled = false
				t.Stage = "blocked"
				t.Paused = false
				t.Message = "停止执行失败，请重试取消或检查现场：" + err.Error()
				previousPhase = "stopping"
				continue
			}
			t.Stage = "cancelled"
			t.Message = "任务已取消；远端分支、MR 与现场保留"
			return persist()
		}
		if t.Paused {
			if t.Project.HostingProvider() == domain.ProviderGitHub && (t.Stage == "merging" || t.Stage == "partial_merge") {
				var paused StepResult
				if err := workflow.ExecuteActivity(ctx, "Step", StepInput{Phase: "pause_queue", Task: t}).Get(ctx, &paused); err != nil {
					t.Paused = false
					t.Stage = "blocked"
					t.Message = "无法撤销 GitHub merge queue；请核实远端状态：" + err.Error()
					previousPhase = "partial_merge"
					continue
				}
				t = paused.Task
			}
			if err := persist(); err != nil {
				return err
			}
			v := controlVersion
			if err := wait(func() bool { return !t.Paused || controlVersion != v }); err != nil {
				return err
			}
			continue
		}
		if t.Stage == "blocked" {
			if err := persist(); err != nil {
				return err
			}
			v := controlVersion
			if err := wait(func() bool { return retry || t.Paused || controlVersion != v }); err != nil {
				return err
			}
			if retry {
				retry = false
				t.Stage = previousPhase
			}
			continue
		}
		if t.Stage == "waiting_approval" {
			if err := persist(); err != nil {
				return err
			}
			v := controlVersion
			if err := wait(func() bool {
				return t.ApprovalDigest == t.ReviewDigest && t.ReviewDigest != "" || rejected || t.Paused || controlVersion != v
			}); err != nil {
				return err
			}
			if cancelled || t.Paused {
				continue
			}
			if rejected {
				rejected = false
				t.ApprovalDigest = ""
				t.ReviewDigest = ""
				t.Reworks++
				previousPhase = "developing"
				if t.Reworks > t.Project.MaxReworks {
					t.Stage = "blocked"
					t.Message = "人工审核返工达到上限"
				} else {
					t.Stage = "developing"
				}
			} else if t.ApprovalDigest == t.ReviewDigest && t.ReviewDigest != "" {
				t.Stage = "acquiring_merge"
			}
			continue
		}
		if t.Stage == "completed" {
			return persist()
		}
		if t.Stage == "queued" {
			t.Stage = "waiting_dependencies"
		}
		previousPhase = t.Stage
		if err := persist(); err != nil {
			return err
		}
		serial++
		op := fmt.Sprintf("%s-%s-%d", t.ID, t.Stage, serial)
		child, cancel := workflow.WithCancel(ctx)
		interrupted = false
		activeCancel = cancel
		mergeControlAtStart := autoMergeControlVersion
		var result StepResult
		err := workflow.ExecuteActivity(child, "Step", StepInput{Phase: t.Stage, Task: t, ExecutionID: op}).Get(child, &result)
		activeCancel = nil
		cancel()
		if cancelled || t.Paused || interrupted {
			// Read the durable execution after cancellation to retain an exact resume session.
			var recovered StepResult
			if e := workflow.ExecuteActivity(ctx, "Step", StepInput{Phase: "recover", Task: t, ExecutionID: op}).Get(ctx, &recovered); e == nil {
				t.Executions = recovered.Task.Executions
			} else {
				t.Paused = false
				t.Stage = "blocked"
				t.Message = "无法确认执行已停止：" + e.Error()
			}
			continue
		}
		if err != nil {
			var recovered StepResult
			if e := workflow.ExecuteActivity(ctx, "Step", StepInput{Phase: "recover", Task: t, ExecutionID: op}).Get(ctx, &recovered); e == nil && recovered.Task.Executions != nil {
				t.Executions = recovered.Task.Executions
			}
			t.Stage = "blocked"
			t.Message = err.Error()
			continue
		}
		// Notes/signals received during an Activity belong to the live workflow state.
		notes, deps, approval, taskAutoMerge := t.Notes, t.Dependencies, t.ApprovalDigest, t.AutoMergeOverride
		t = result.Task
		t.Notes = notes
		t.Dependencies = deps
		t.ApprovalDigest = approval
		if autoMergeControlVersion != mergeControlAtStart {
			t.AutoMergeOverride = taskAutoMerge
		}
		if result.Rework {
			t.Reworks++
			t.ApprovalDigest = ""
			t.ReviewDigest = ""
			t.Notes = append(t.Notes, t.Message)
			if t.Reworks > t.Project.MaxReworks {
				t.Stage = "blocked"
				previousPhase = "developing"
			} else {
				t.Stage = "developing"
			}
			continue
		}
		switch previousPhase {
		case "stopping":
			t.Stage = "cancelled"
			t.Message = "任务已取消，现场保留"
			return persist()
		case "waiting_dependencies":
			if result.Ready {
				t.Stage = "preparing"
			} else {
				waitTick(120 * time.Second)
			}
		case "preparing":
			t.Stage = "developing"
		case "developing":
			ex := t.Executions["dev"]
			if ex.Result == nil || ex.Result.Outcome == "blocked" {
				t.Stage = "blocked"
				if ex.Result != nil {
					t.Message = ex.Result.NeedsHelp
				}
				continue
			}
			t.Assumptions = append(t.Assumptions, ex.Result.Assumptions...)
			if skipAcceptance || earlyPublish {
				t.Stage = "publishing"
			} else {
				t.Stage = "verifying"
			}
		case "verifying":
			if result.Waiting {
				ciWaits++
				if ciWaits <= 60 {
					waitTick(30 * time.Second)
					continue
				}
				t.AcceptanceIncomplete = true
				t.Message = "等待 CI 超过 30 分钟，保留 MR 供人工验收，禁止自动合并"
				result.Ready = true
			} else {
				ciWaits = 0
			}

			if result.Ready {
				if earlyPublish {
					if reviewEnabled {
						t.Stage = "reviewing"
					} else {
						advanceAfterReview()
					}
				} else {
					t.Stage = "publishing"
				}
			} else {
				t.Reworks++
				t.ApprovalDigest = ""
				t.ReviewDigest = ""
				failed := []string{}
				for _, check := range t.Verification {
					if check.Required && !check.Passed {
						failed = append(failed, check.Name)
					}
				}
				t.Message = "必需验收未通过，请读取报告并修复"
				if len(failed) > 0 {
					t.Message += "：" + strings.Join(failed, "、")
				}
				t.Notes = append(t.Notes, t.Message)
				if t.Reworks > t.Project.MaxReworks {
					t.Stage = "blocked"
					previousPhase = "developing"
				} else {
					t.Stage = "developing"
				}
			}
		case "publishing":
			ex := t.Executions["dev"]
			if ex.Result != nil && ex.Result.Outcome == "blocked" {
				t.Stage = "blocked"
				t.Message = ex.Result.NeedsHelp
				continue
			}
			if skipAcceptance {
				if reviewEnabled {
					t.Stage = "reviewing"
				} else {
					advanceAfterReview()
				}
			} else if earlyPublish {
				t.Stage = "verifying"
			} else {
				if reviewEnabled {
					t.Stage = "reviewing"
				} else {
					advanceAfterReview()
				}
			}
		case "reviewing":
			ex := t.Executions["review"]
			if ex.Result == nil || ex.Result.Outcome == "blocked" {
				t.Stage = "blocked"
				if ex.Result != nil {
					t.Message = ex.Result.NeedsHelp
				}
				continue
			}
			if ex.Result.Outcome != "approved" {
				t.Reworks++
				t.Notes = append(t.Notes, ex.Result.Summary)
				for _, f := range ex.Result.Findings {
					t.Notes = append(t.Notes, fmt.Sprintf("%s/%s: %s", f.RepositoryID, f.Path, f.Message))
				}
				t.ApprovalDigest = ""
				t.ReviewDigest = ""
				if t.Reworks > t.Project.MaxReworks {
					t.Stage = "blocked"
					previousPhase = "developing"
				} else {
					t.Stage = "developing"
				}
				continue
			}
			advanceAfterReview()
		case "acquiring_merge":
			if t.Stage == "waiting_approval" || t.Stage == "waiting_manual_merge" {
				continue
			}
			if result.Ready {
				ciWaits = 0
				t.Stage = "merging"
			} else {
				if strings.HasPrefix(t.Message, "等待 GitLab") {
					ciWaits++
					if ciWaits >= 180 {
						t.Stage = "blocked"
						t.Message = "GitLab CI 或合并检查等待超过 30 分钟，请检查后重试"
						ciWaits = 0
						continue
					}
				}
				waitTick(10 * time.Second)
			}
		case "waiting_manual_merge":
			if t.Stage == "waiting_approval" {
				continue
			}
			if t.AutoMergeEnabled() && !t.AcceptanceIncomplete && !result.Ready {
				t.Stage = "acquiring_merge"
				continue
			}
			if result.Ready {
				t.Stage = "finalizing"
			} else {
				waitTick(30 * time.Second)
			}
		case "merging", "partial_merge":
			if t.Stage == "waiting_approval" || t.Stage == "waiting_manual_merge" {
				continue
			}
			if result.Ready {
				t.Stage = "finalizing"
			} else if t.Project.HostingProvider() == domain.ProviderGitHub && strings.HasPrefix(t.Message, "等待 GitHub merge queue") {
				t.Stage = "partial_merge"
				if err := persist(); err != nil {
					return err
				}
				waitTick(30 * time.Second)
			} else {
				t.Stage = "partial_merge"
				if err := persist(); err != nil {
					return err
				}
				previousPhase = "partial_merge"
				t.Stage = "blocked"
			}
		case "finalizing":
			if t.Stage == "waiting_approval" {
				continue
			}
			t.Stage = "completed"
			if evergreen {
				t.Message = "所有仓库已合并，来源 Issue 保持打开以供复核和后续迭代"
			} else {
				t.Message = "所有仓库处理完成，来源 Issue 已关闭"
			}
		default:
			return fmt.Errorf("unknown phase %q", previousPhase)
		}
	}
}
