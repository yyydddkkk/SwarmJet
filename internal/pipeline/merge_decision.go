package pipeline

import (
	"context"

	"go.temporal.io/sdk/temporal"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
)

// mergeEvidence owns the review evidence used by both automatic and manual
// merge paths. The callers still decide which workflow phase follows a result.
type mergeEvidence struct {
	client forge.Client
	task   *domain.Task
}

func (e mergeEvidence) reviewCurrent() bool {
	return e.task.ReviewDigest != "" && e.task.ReviewDigest == domain.Digest(e.task.Workspace.Revisions)
}

func (e mergeEvidence) verification() (required int, current bool) {
	current = true
	for _, v := range e.task.Verification {
		if !v.Required {
			continue
		}
		required++
		current = current && v.Passed && v.RevisionDigest == e.task.ReviewDigest
	}
	return required, current
}

// reviewedMR checks identity before any caller interprets the remote state,
// including a remote MR that is already merged.
func (e mergeEvidence) reviewedMR(ctx context.Context, r domain.Revision) (forge.MergeRequest, bool, error) {
	mr, err := e.client.GetMR(ctx, r.ProjectID, r.MRIID)
	if err != nil {
		return mr, false, err
	}
	return mr, sameMR(mr, r), nil
}

func (e mergeEvidence) automaticGate(ctx context.Context) (bool, string, error) {
	t := e.task
	if !t.AutoMergeEnabled() {
		return false, "此任务已关闭自动合并", nil
	}
	if t.AcceptanceIncomplete {
		return false, "验收不完整，只能人工处理，禁止自动合并", nil
	}
	if t.Issue.ProjectID > 0 && t.Issue.IID > 0 {
		fresh, err := e.client.GetIssue(ctx, t.Issue.ProjectID, t.Issue.IID)
		if err != nil {
			return false, "", err
		}
		t.Issue = fresh
	}
	if !e.reviewCurrent() {
		return false, "STALE: review digest no longer matches", nil
	}
	if t.Round == 0 && requiresApproval(*t) && t.ApprovalDigest != t.ReviewDigest {
		return false, "manual approval required", nil
	}
	if t.Project.AcceptanceStrategy != "none" {
		required, current := e.verification()
		if !current {
			return false, "verification not passed for reviewed revisions", nil
		}
		if required == 0 {
			return false, "required verification is missing", nil
		}
	}
	for i, r := range t.Workspace.Revisions {
		if !r.Changed {
			continue
		}
		mr, same, err := e.reviewedMR(ctx, r)
		if err != nil {
			return false, "", err
		}
		if !same {
			return false, "STALE: MR changed after review", nil
		}
		if mr.State == "merged" {
			t.Workspace.Revisions[i].Merged = true
			continue
		}
		if ok, reason, err := e.openRevisionGate(ctx, r, mr); !ok || err != nil {
			return ok, reason, err
		}
	}
	return true, "", nil
}

func (e mergeEvidence) openRevisionGate(ctx context.Context, r domain.Revision, mr forge.MergeRequest) (bool, string, error) {
	closes, err := e.client.ClosingIssues(ctx, r.ProjectID, r.MRIID)
	if err != nil {
		return false, "", err
	}
	if len(closes) > 0 {
		return false, "STALE: remove closing issue references from MR and commits; the workflow closes issues after the full set", nil
	}
	// Identity was checked by reviewedMR. The target and platform checks apply
	// only to open revisions; merged revisions can have an advanced target.
	target, err := e.client.GetBranch(ctx, r.ProjectID, r.TargetBranch)
	if err != nil {
		return false, "", err
	}
	if target.Commit.ID != r.BaseSHA {
		return false, "STALE: target branch advanced; development must synchronize and reverify", nil
	}
	if mr.HasConflicts {
		return false, "STALE: merge conflict", nil
	}
	if e.task.Project.HostingProvider() == domain.ProviderGitHub {
		return e.githubChecks(ctx, r, mr)
	}
	return e.gitlabChecks(ctx, r, mr)
}

func (e mergeEvidence) githubChecks(ctx context.Context, r domain.Revision, mr forge.MergeRequest) (bool, string, error) {
	client, err := githubClient(e.client)
	if err != nil {
		return false, "", err
	}
	checks, err := client.RequiredChecks(ctx, r.ProjectID, r.MRIID, r.HeadSHA)
	if err != nil {
		return false, "", err
	}
	if checks.RequiredCount == 0 {
		if e.task.Project.RequireCI || e.task.Project.AcceptanceStrategy == "platform_ci" {
			return false, "目标分支没有配置必需检查，无法满足 CI 要求", nil
		}
		if !checks.MergeQueue && mr.DetailedMergeStatus != "mergeable" {
			return false, "等待 GitHub PR 合并检查：" + mr.DetailedMergeStatus, nil
		}
		return true, "", nil
	}
	if checks.Failed {
		return false, "STALE: " + checks.Reason, nil
	}
	if !checks.Ready {
		return false, "等待 GitHub 必需检查：" + checks.Reason, nil
	}
	if !checks.MergeQueue && mr.DetailedMergeStatus != "mergeable" {
		return false, "等待 GitHub PR 合并检查：" + mr.DetailedMergeStatus, nil
	}
	return true, "", nil
}

func (e mergeEvidence) gitlabChecks(ctx context.Context, r domain.Revision, mr forge.MergeRequest) (bool, string, error) {
	requireCI := e.task.Project.RequireCI || e.task.Project.AcceptanceStrategy == "gitlab_ci" || e.task.Project.AcceptanceStrategy == "platform_ci"
	if requireCI && mr.HeadPipeline != nil && (mr.HeadPipeline.Status == "failed" || mr.HeadPipeline.Status == "canceled") {
		return false, "STALE: required CI failed; inspect the pipeline and repair", nil
	}
	if mr.DetailedMergeStatus != "mergeable" && mr.DetailedMergeStatus != "ci_still_running" && mr.DetailedMergeStatus != "checking" && mr.DetailedMergeStatus != "unchecked" {
		return false, "GitLab merge condition: " + mr.DetailedMergeStatus, nil
	}
	ok, err := e.client.RequiredCIPassed(ctx, r.ProjectID, r.MRIID, r.HeadSHA, requireCI)
	if err != nil {
		return false, "", err
	}
	if !ok || mr.DetailedMergeStatus != "mergeable" {
		return false, "等待 GitLab CI 或合并检查", nil
	}
	return true, "", nil
}

func (e mergeEvidence) manualVerificationCurrent() (bool, bool) {
	if e.task.Project.AcceptanceStrategy == "none" {
		return true, false
	}
	required, current := e.verification()
	return e.task.AcceptanceIncomplete || current, required == 0 && !e.task.AcceptanceIncomplete
}

func (e mergeEvidence) reconcileMergedRevisions(ctx context.Context) (bool, error) {
	allMerged := true
	for i, r := range e.task.Workspace.Revisions {
		if !r.Changed {
			continue
		}
		mr, same, err := e.reviewedMR(ctx, r)
		if err != nil {
			return false, err
		}
		if !same {
			return false, temporal.NewNonRetryableApplicationError("MR identity changed after review", "stale_review", nil)
		}
		if mr.State == "merged" {
			e.task.Workspace.Revisions[i].Merged = true
		} else {
			allMerged = false
		}
	}
	return allMerged, nil
}

func sameMR(m forge.MergeRequest, r domain.Revision) bool {
	return m.SHA == r.HeadSHA && m.SourceBranch == r.Branch && m.TargetBranch == r.TargetBranch
}
