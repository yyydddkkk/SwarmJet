package pipeline

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

func TestGitHubWorkflowWaitsForQueuedMerge(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Round = 1
	task.Project.Provider = domain.ProviderGitHub
	task.Project.AcceptanceStrategy = "none"
	task.Project.ReviewMode = "none"
	task.Issue.Provider = domain.ProviderGitHub
	h.step = func(_ context.Context, input StepInput, _ int) (StepResult, error) {
		out := successfulStep(input)
		if input.Phase == "merging" {
			out.Ready = false
			out.Task.Message = "等待 GitHub merge queue 完成"
			out.Task.Workspace.Revisions[0].MergeQueueEntryID = "entry-1"
		}
		if input.Phase == "partial_merge" {
			out.Task.Workspace.Revisions[0].Merged = true
		}
		return out, nil
	}
	final := h.run(t, task)
	if final.Stage != "completed" || h.counts["merging"] != 1 || h.counts["partial_merge"] != 1 {
		t.Fatalf("queue was not awaited: stage=%s phases=%v", final.Stage, h.phases)
	}
}

type githubFlowStub struct {
	forge.Client
	pr          forge.MergeRequest
	queue       github.MergeResult
	requests    int
	checkResult github.CheckResult
}

func (s *githubFlowStub) FindMR(context.Context, int64, string, string) (*forge.MergeRequest, error) {
	return nil, nil
}
func (s *githubFlowStub) CreateOrUpdatePR(_ context.Context, _ int64, _, _, _, _ string) (forge.MergeRequest, error) {
	return s.pr, nil
}
func (s *githubFlowStub) GetMR(context.Context, int64, int64) (forge.MergeRequest, error) {
	return s.pr, nil
}
func (s *githubFlowStub) GetIssue(_ context.Context, id, number int64) (domain.Issue, error) {
	return domain.Issue{Provider: domain.ProviderGitHub, ProjectID: id, IID: number, State: "opened"}, nil
}
func (s *githubFlowStub) ClosingIssues(context.Context, int64, int64) ([]domain.IssueRef, error) {
	return nil, nil
}
func (s *githubFlowStub) GetBranch(context.Context, int64, string) (forge.Branch, error) {
	b := forge.Branch{}
	b.Commit.ID = "base"
	return b, nil
}
func (s *githubFlowStub) RequiredChecks(context.Context, int64, int64, string) (github.CheckResult, error) {
	return s.checkResult, nil
}
func (s *githubFlowStub) RequestMerge(context.Context, int64, int64, string, string) (github.MergeResult, error) {
	s.requests++
	return s.queue, nil
}
func (s *githubFlowStub) GetMergeState(context.Context, int64, int64, string) (github.MergeResult, error) {
	return s.queue, nil
}
func (s *githubFlowStub) CancelMergeQueue(context.Context, int64, int64) error {
	s.queue = github.MergeResult{}
	return nil
}

type githubPublishingNode struct {
	execution.Node
	env map[string]string
}

func (n *githubPublishingNode) PushWorkspace(_ context.Context, _ domain.Workspace, _ domain.Project, env map[string]string) error {
	n.env = env
	return nil
}

type githubNodeResolver struct{ node execution.Node }

func (r githubNodeResolver) Node(context.Context, string) (execution.Node, error) { return r.node, nil }

func githubTask() domain.Task {
	repo := domain.Repository{ID: "api", ProjectID: 91, TargetBranch: "main"}
	revision := domain.Revision{RepositoryID: "api", ProjectID: 91, BaseSHA: "base", HeadSHA: "head", Branch: "swarmjet/task", TargetBranch: "main", Changed: true, MRIID: 7}
	return domain.Task{ID: "task", Round: 1, NodeID: "node", Project: domain.Project{Provider: domain.ProviderGitHub, AcceptanceStrategy: "none", RequireCI: true, ReviewMode: "independent", Repositories: []domain.Repository{repo}}, Issue: domain.Issue{Provider: domain.ProviderGitHub, ProjectID: 91, IID: 2, Title: "Improve API", URL: "https://github.com/org/api/issues/2", State: "opened"}, Workspace: domain.Workspace{TaskID: "task", Revisions: []domain.Revision{revision}}, Executions: map[string]domain.Execution{}}
}

func TestGitHubPublicationUsesNodeAndDoesNotRunAgent(t *testing.T) {
	task := githubTask()
	stub := &githubFlowStub{pr: forge.MergeRequest{IID: 7, URL: "https://github.com/org/api/pull/7", SourceBranch: "swarmjet/task", TargetBranch: "main", SHA: "head"}}
	node := &githubPublishingNode{}
	a := &Activities{Nodes: githubNodeResolver{node}, ProjectCredential: func(domain.Project, bool) (string, error) { return "fresh-installation-token", nil }}
	out, err := a.publishGitHub(context.Background(), StepInput{ExecutionID: "publish-1"}, task, stub)
	if err != nil || !out.Ready || out.Task.Workspace.Revisions[0].MRURL != stub.pr.URL || node.env["GITHUB_TOKEN"] != "fresh-installation-token" {
		t.Fatalf("publication = %+v, env = %+v, err = %v", out, node.env, err)
	}
	if got := out.Task.Executions["dev"]; got.Result == nil || got.Result.Outcome != "changed" {
		t.Fatalf("publishing did not produce a workflow handoff: %+v", got)
	}
}

func TestGitHubMergeQueueWaitsWithoutSubmittingTwice(t *testing.T) {
	task := githubTask()
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	stub := &githubFlowStub{pr: forge.MergeRequest{IID: 7, URL: "https://github.com/org/api/pull/7", SourceBranch: "swarmjet/task", TargetBranch: "main", SHA: "head", State: "opened", DetailedMergeStatus: "mergeable"}, queue: github.MergeResult{Queued: true, QueueEntryID: "entry-7"}, checkResult: github.CheckResult{Ready: true, RequiredCount: 1}}
	a := &Activities{}
	first, err := a.mergeGitHub(context.Background(), task, stub)
	if err != nil || first.Ready || first.Task.Workspace.Revisions[0].MergeQueueEntryID != "entry-7" || stub.requests != 1 {
		t.Fatalf("queue submission = %+v, requests = %d, err = %v", first, stub.requests, err)
	}
	second, err := a.mergeGitHub(context.Background(), first.Task, stub)
	if err != nil || second.Ready || stub.requests != 1 {
		t.Fatalf("queue poll = %+v, requests = %d, err = %v", second, stub.requests, err)
	}
	stub.pr.State = "merged"
	third, err := a.mergeGitHub(context.Background(), second.Task, stub)
	if err != nil || !third.Ready || !third.Task.Workspace.Revisions[0].Merged {
		t.Fatalf("merged queue result = %+v, err = %v", third, err)
	}
}

func TestGitHubQueueCancellationAndMergedPollRace(t *testing.T) {
	task := githubTask()
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	task.Workspace.Revisions[0].MergeQueueEntryID = "entry-7"
	stub := &githubFlowStub{pr: forge.MergeRequest{IID: 7, SourceBranch: "swarmjet/task", TargetBranch: "main", SHA: "head", State: "opened"}, queue: github.MergeResult{Queued: true, QueueEntryID: "entry-7"}}
	a := &Activities{ForgeClient: func(domain.Project, bool) (forge.Client, error) { return stub, nil }}
	if err := a.cancelQueuedGitHub(context.Background(), &task); err != nil || task.Workspace.Revisions[0].MergeQueueEntryID != "" || stub.queue.Queued {
		t.Fatalf("queue cancellation failed: task=%+v queue=%+v err=%v", task.Workspace.Revisions[0], stub.queue, err)
	}
	task.Workspace.Revisions[0].MergeQueueEntryID = "entry-7"
	stub.queue = github.MergeResult{Merged: true}
	out, err := a.mergeGitHub(context.Background(), task, stub)
	if err != nil || !out.Ready || !out.Task.Workspace.Revisions[0].Merged {
		t.Fatalf("merged PR during poll was lost: %+v, err=%v", out, err)
	}
}
