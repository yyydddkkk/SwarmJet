package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

type githubFlow interface {
	forge.Client
	CreateOrUpdatePR(context.Context, int64, string, string, string, string) (forge.MergeRequest, error)
	RequiredChecks(context.Context, int64, int64, string) (github.CheckResult, error)
	RequestMerge(context.Context, int64, int64, string, string) (github.MergeResult, error)
	GetMergeState(context.Context, int64, int64, string) (github.MergeResult, error)
	CancelMergeQueue(context.Context, int64, int64) error
}

func githubClient(c forge.Client) (githubFlow, error) {
	client, ok := c.(githubFlow)
	if !ok {
		return nil, errors.New("GitHub hosting client is unavailable")
	}
	return client, nil
}

func (a *Activities) cancelQueuedGitHub(ctx context.Context, t *domain.Task) error {
	hasChange := false
	for _, r := range t.Workspace.Revisions {
		hasChange = hasChange || r.Changed && r.MRIID > 0 && !r.Merged
	}
	if !hasChange {
		return nil
	}
	c, err := a.client(t.Project, false)
	if err != nil {
		return err
	}
	client, err := githubClient(c)
	if err != nil {
		return err
	}
	for i, r := range t.Workspace.Revisions {
		if !r.Changed || r.MRIID <= 0 || r.Merged {
			continue
		}
		state, err := client.GetMergeState(ctx, r.ProjectID, r.MRIID, r.HeadSHA)
		if err != nil {
			return err
		}
		if state.Merged {
			t.Workspace.Revisions[i].Merged = true
		} else if state.Queued {
			if err = client.CancelMergeQueue(ctx, r.ProjectID, r.MRIID); err != nil {
				return err
			}
			state, err = client.GetMergeState(ctx, r.ProjectID, r.MRIID, r.HeadSHA)
			if err != nil {
				return err
			}
			if state.Queued {
				return errors.New("GitHub merge queue entry remains active after cancellation")
			}
			if state.Merged {
				t.Workspace.Revisions[i].Merged = true
			}
		}
		t.Workspace.Revisions[i].MergeQueueEntryID = ""
	}
	if a.Store != nil {
		_ = a.Store.ReleaseLeases(ctx, t.ID)
	}
	return nil
}

// publishGitHub keeps the remote mutation on the platform side. No installation
// credential is passed to an Agent execution.
func (a *Activities) publishGitHub(ctx context.Context, in StepInput, t domain.Task, c forge.Client) (StepResult, error) {
	out := StepResult{Task: t, Ready: true}
	client, err := githubClient(c)
	if err != nil {
		return out, err
	}
	n, err := a.node(ctx, t)
	if err != nil {
		return out, err
	}
	publisher, ok := n.(execution.WorkspacePublisher)
	if !ok {
		return out, errors.New("execution node cannot publish a GitHub workspace")
	}
	env, err := a.gitEnv(t)
	if err != nil {
		return out, err
	}
	if err = publisher.PushWorkspace(ctx, t.Workspace, t.Project, env); err != nil {
		return out, err
	}
	for i, r := range t.Workspace.Revisions {
		if !r.Changed {
			continue
		}
		body := fmt.Sprintf("SwarmJet task `%s`\n\nSource Issue: %s\n\nReviewed head: `%s`\n", t.ID, t.Issue.URL, r.HeadSHA)
		previous, err := c.FindMR(ctx, r.ProjectID, r.Branch, r.TargetBranch)
		if err != nil {
			return out, err
		}
		if previous != nil {
			body = previous.Description
		}
		if t.Round == 0 && ((t.Project.AcceptanceStrategy != "" && t.Project.AcceptanceStrategy != "none") || t.HumanAcceptance != nil) {
			body, err = manualDescription(t, r, body)
			if err != nil {
				return out, err
			}
		}
		pr, err := client.CreateOrUpdatePR(ctx, r.ProjectID, r.Branch, r.TargetBranch, t.Issue.Title, body)
		if err != nil {
			return out, err
		}
		if !sameMR(pr, r) || pr.IID <= 0 || pr.URL == "" {
			return out, fmt.Errorf("GitHub PR for %s does not match the inspected commit", r.RepositoryID)
		}
		t.Workspace.Revisions[i].MRIID = pr.IID
		t.Workspace.Revisions[i].MRURL = pr.URL
	}
	if t.Round > 0 && !t.Project.IndependentReview() {
		if err = publishIssueReviewGuide(ctx, c, t); err != nil {
			return out, err
		}
	}
	t.Executions["dev"] = domain.Execution{ID: in.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "changed"}}
	out.Task = t
	return out, nil
}

func (a *Activities) mergeGitHub(ctx context.Context, t domain.Task, c forge.Client) (StepResult, error) {
	out := StepResult{Task: t, Ready: true}
	if a.Store != nil {
		if err := a.renewMerge(ctx, t); err != nil {
			return out, err
		}
	}
	keepLease := false
	defer func() {
		if a.Store != nil && !keepLease {
			_ = a.Store.ReleaseLeases(ctx, t.ID)
		}
	}()
	client, err := githubClient(c)
	if err != nil {
		return out, err
	}
	evidence := mergeEvidence{client: c, task: &t}
	if !evidence.reviewCurrent() {
		out.Ready, out.Rework = false, true
		t.Message = "STALE: reviewed revisions changed"
		out.Task = t
		return out, nil
	}
	for _, id := range domain.MergeOrder(t.Project.Repositories) {
		for i, r := range t.Workspace.Revisions {
			if r.RepositoryID != id || !r.Changed || r.Merged {
				continue
			}
			pr, same, err := evidence.reviewedMR(ctx, r)
			if err != nil {
				return out, err
			}
			if !same {
				out.Ready, out.Rework = false, true
				t.Message = "STALE: PR changed after review"
				out.Task = t
				return out, nil
			}
			if pr.State == "merged" {
				t.Workspace.Revisions[i].Merged = true
				continue
			}
			if r.MergeQueueEntryID != "" {
				state, err := client.GetMergeState(ctx, r.ProjectID, r.MRIID, r.HeadSHA)
				if err != nil {
					return out, err
				}
				if state.Merged {
					t.Workspace.Revisions[i].Merged = true
					continue
				}
				if !state.Queued || state.QueueEntryID != r.MergeQueueEntryID {
					out.Ready = false
					t.Workspace.Revisions[i].MergeQueueEntryID = ""
					t.Message = "GitHub merge queue entry disappeared; inspect PR and retry"
					out.Task = t
					return out, nil
				}
				out.Ready = false
				t.Message = "等待 GitHub merge queue 完成"
				keepLease = true
				out.Task = t
				return out, nil
			}
			allowed, err := a.taskAutoMergeAllowed(ctx, t)
			if err != nil {
				return out, err
			}
			if !allowed {
				out.Ready = false
				t.Stage = domain.StageWaitingManualMerge
				t.Message = "已关闭自动合并，等待人工合并 PR"
				out.Task = t
				return out, nil
			}
			ready, reason, err := a.gates(ctx, c, &t)
			if err != nil {
				return out, err
			}
			if !ready {
				out.Ready = false
				out.Rework = strings.HasPrefix(reason, "STALE:")
				t.Message = reason
				out.Task = t
				return out, nil
			}
			result, err := client.RequestMerge(ctx, r.ProjectID, r.MRIID, r.HeadSHA, t.Project.EffectiveMergeMethod())
			if err != nil {
				return out, err
			}
			if result.Queued {
				t.Workspace.Revisions[i].MergeQueueEntryID = result.QueueEntryID
				out.Ready = false
				t.Message = "等待 GitHub merge queue 完成"
				keepLease = true
				out.Task = t
				return out, nil
			}
			if !result.Merged {
				return out, errors.New("GitHub did not confirm PR merge")
			}
			merged, same, err := evidence.reviewedMR(ctx, r)
			if err != nil || !same || merged.State != "merged" {
				return out, errors.New("GitHub merged PR identity could not be verified")
			}
			t.Workspace.Revisions[i].Merged = true
		}
	}
	out.Task = t
	return out, nil
}
