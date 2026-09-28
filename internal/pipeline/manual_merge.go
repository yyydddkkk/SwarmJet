package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/store"
)

// manualMergeRound runs only as a Workflow activity. The command and Temporal
// history contain a Task ID, never a personal access token.
func (a *Activities) manualMergeRound(ctx context.Context, task domain.Task) (StepResult, error) {
	out := StepResult{Task: task}
	if a.Store == nil || a.PersonalGitLabToken == nil || task.Project.HostingProvider() != domain.ProviderGitLab {
		return out, errors.New("personal GitLab merge identity is unavailable")
	}
	record, err := a.Store.ManualAcceptance(ctx, task.ID)
	if err != nil {
		return out, err
	}
	if record.Digest == "" || task.ReviewDigest != domain.Digest(task.Workspace.Revisions) || record.Digest != domain.ManualMergeEvidenceDigest(task) {
		out.Task.Message = "人工验收版本已失效，请重新验收当前提交"
		return out, nil
	}
	count := 0
	if task.HumanAcceptance != nil {
		count = len(task.HumanAcceptance.Prerequisites) + len(task.HumanAcceptance.Steps)
	}
	if len(record.Checks) != count || strings.TrimSpace(record.Note) == "" {
		out.Task.Message = "人工验收记录不完整"
		return out, nil
	}
	token, err := a.PersonalGitLabToken(ctx, task.Project.GitLabURL)
	if err != nil {
		return out, fmt.Errorf("personal GitLab identity unavailable: %w", err)
	}
	client := gitlab.New(task.Project.GitLabURL, token)
	evidence := mergeEvidence{client: client, task: &task}
	if !evidence.reviewCurrent() {
		out.Task.Message = "审查摘要已变化，请重新验收"
		return out, nil
	}
	if current, missing := evidence.manualVerificationCurrent(); !current && !task.AcceptanceIncomplete || missing && !task.AcceptanceIncomplete {
		out.Task.Message = "必需验证已失效，请重新验证"
		return out, nil
	}
	defer a.Store.ReleaseLeases(ctx, task.ID)
	for _, repositoryID := range domain.MergeOrder(task.Project.Repositories) {
		for i, revision := range task.Workspace.Revisions {
			if revision.RepositoryID != repositoryID || !revision.Changed || revision.Merged {
				continue
			}
			mr, same, err := evidence.reviewedMR(ctx, revision)
			if err != nil {
				return StepResult{Task: task}, err
			}
			if !same {
				out.Task = task
				out.Task.Message = "MR 提交已变化，人工验收失效"
				return out, nil
			}
			if mr.State == "merged" {
				task.Workspace.Revisions[i].Merged = true
				if err = a.Store.SaveTask(ctx, task); err != nil {
					return StepResult{Task: task}, err
				}
				continue
			}
			ok, err := a.Store.Lease(ctx, mergeLock(task, revision), task.ID, 3*time.Minute)
			if err != nil {
				return StepResult{Task: task}, err
			}
			if !ok {
				out.Task = task
				out.Task.Message = "目标分支正在被另一任务合并，请稍后重试"
				return out, nil
			}
			// Each remote write gets a fresh status, target SHA, CI and MR head
			// check. An ambiguous response is reconciled before another attempt.
			mr, same, err = evidence.reviewedMR(ctx, revision)
			if err != nil {
				return StepResult{Task: task}, err
			}
			if !same {
				out.Task = task
				out.Task.Message = "MR 提交已变化，人工验收失效"
				return out, nil
			}
			ready, reason, err := evidence.openRevisionGate(ctx, revision, mr)
			if err != nil {
				return StepResult{Task: task}, err
			}
			if !ready {
				out.Task = task
				out.Task.Message = reason
				return out, nil
			}
			requireCI := task.Project.RequireCI || task.Project.AcceptanceStrategy == "gitlab_ci" || task.Project.AcceptanceStrategy == "platform_ci"
			err = client.MergePinned(ctx, revision.ProjectID, revision.MRIID, revision.HeadSHA, revision.TargetBranch, requireCI)
			if err != nil {
				observed, same, readErr := evidence.reviewedMR(ctx, revision)
				if readErr != nil || !same || observed.State != "merged" {
					return StepResult{Task: task}, err
				}
			}
			task.Workspace.Revisions[i].Merged = true
			if err = a.Store.SaveTask(ctx, task); err != nil {
				return StepResult{Task: task}, err
			}
		}
	}
	out.Task = task
	out.Task.Message = "整轮 MR 已合并，正在完成任务"
	out.Ready = true
	return out, nil
}

// Compile-time evidence that manual records are owned by the durable store.
var _ = store.ManualAcceptanceRecord{}
