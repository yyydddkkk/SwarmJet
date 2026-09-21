package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/store"
	"swarmjet/internal/workspace"
)

type IntegrationRunner interface {
	Start(context.Context, domain.Workspace, domain.IntegrationConfig) error
	Verify(context.Context, domain.Workspace, domain.IntegrationConfig) ([]domain.Verification, error)
	Stop(context.Context, domain.Workspace, domain.IntegrationConfig) error
}
type Activities struct {
	Store          *store.Store
	Workspace      *workspace.Manager
	Agents         execution.AgentBackend
	Integration    IntegrationRunner
	Nodes          execution.NodeResolver
	IntegrationEnv map[string]string
	DataDir        string
	Concurrency    int
	Credential     func(string) (string, error)
	AgentEnv       map[string]string
}

func (a *Activities) client(p domain.Project, merge bool) (*gitlab.Client, error) {
	name := p.CredentialRef
	if merge {
		name = p.MergeCredentialRef
	}
	token, e := a.Credential(name)
	if e != nil {
		return nil, temporal.NewNonRetryableApplicationError("credential unavailable: "+name, "configuration", e)
	}
	return gitlab.New(p.GitLabURL, token), nil
}

// node resolves the execution environment for a task. An empty NodeID keeps the
// in-process managers, which is the local development and single-host default.
func (a *Activities) node(ctx context.Context, t domain.Task) (execution.Node, error) {
	if t.NodeID != "" {
		if a.Nodes == nil {
			return nil, fmt.Errorf("task %s is assigned to node %s but no node transport is configured", t.ID, t.NodeID)
		}
		return a.Nodes.Node(ctx, t.NodeID)
	}
	return &execution.LocalNode{AgentBackend: a.Agents, Workspace: a.Workspace, Integration: a.Integration, DataDir: a.DataDir}, nil
}

// gitEnv resolves the phase credential into the Git environment used on the
// execution host. It never reaches Temporal history because it stays inside the
// Activity.
func (a *Activities) gitEnv(t domain.Task) (map[string]string, error) {
	token, e := a.Credential(t.Project.CredentialRef)
	if e != nil {
		return nil, temporal.NewNonRetryableApplicationError("credential unavailable: "+t.Project.CredentialRef, "configuration", e)
	}
	env := map[string]string{"GITLAB_TOKEN": token, "GIT_TERMINAL_PROMPT": "0"}
	// The execution host decides its own credential-helper path. Only the local
	// host uses the control plane's path; a remote node keeps its own.
	if t.NodeID == "" {
		if a.Workspace != nil && a.Workspace.Env["GIT_ASKPASS"] != "" {
			env["GIT_ASKPASS"] = a.Workspace.Env["GIT_ASKPASS"]
		} else {
			env["GIT_ASKPASS"] = "/usr/local/bin/swarmjet-askpass"
		}
	}
	return env, nil
}

func (a *Activities) integrationEnv(t domain.Task) map[string]string {
	// Integration variables belong to the execution host. A remote node uses its
	// own allowlist instead of receiving the control plane's values.
	if t.NodeID != "" || len(a.IntegrationEnv) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range a.IntegrationEnv {
		out[k] = v
	}
	return out
}
func (a *Activities) SaveTask(ctx context.Context, t domain.Task) error {
	old, e := a.Store.Task(ctx, t.ID)
	if e != nil {
		return e
	}
	if e = a.Store.SaveTask(ctx, t); e != nil {
		return e
	}
	if old.Stage != t.Stage || old.Paused != t.Paused || old.Message != t.Message {
		return a.Store.Event(ctx, t.ID, t.Stage, t.Message)
	}
	return nil
}
func (a *Activities) Step(ctx context.Context, in StepInput) (StepResult, error) {
	t := in.Task
	out := StepResult{Task: t, Ready: true}
	if t.Executions == nil {
		t.Executions = map[string]domain.Execution{}
	}
	// Heartbeats track liveness of all blocking I/O, not just agent output.
	hbCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func(heartbeatCtx context.Context) {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-tick.C:
				activity.RecordHeartbeat(heartbeatCtx, in.ExecutionID)
			}
		}
	}(ctx)
	if in.Phase == "recover" {
		n, e := a.node(ctx, t)
		if e != nil {
			return out, e
		}
		executions, e := n.TaskExecutions(ctx, t.ID)
		if e != nil {
			return out, e
		}
		for _, ex := range executions {
			if ex.ID == in.ExecutionID || strings.HasPrefix(ex.ID, in.ExecutionID+"-") {
				if ex.Status == "running" || ex.Status == "starting" {
					if e = n.Cancel(ctx, ex.ID); e != nil {
						return out, e
					}
					ex, e = n.Inspect(ctx, ex.ID)
					if e != nil {
						return out, e
					}
				}
				t.Executions[roleFor(t.Stage)] = ex
			}
		}
		out.Task = t
		return out, nil
	}
	if in.Phase == "stop" || in.Phase == "stopping" {
		n, e := a.node(ctx, t)
		if e != nil {
			return out, e
		}
		if e := n.CancelTask(ctx, t.ID); e != nil {
			return out, e
		}
		if t.Workspace.Root != "" {
			if e := n.StopIntegration(ctx, t.Workspace, t.Project.Integration, a.integrationEnv(t)); e != nil {
				return out, e
			}
		}
		if e := a.Store.ReleaseLeases(ctx, t.ID); e != nil {
			return out, e
		}
		out.Task = t
		return out, nil
	}
	c, e := a.client(t.Project, false)
	if e != nil {
		return out, e
	}
	switch in.Phase {
	case "waiting_dependencies":
		deps, e := c.Dependencies(ctx, t.Issue)
		if e != nil {
			return out, e
		}
		deps = append(deps, t.Dependencies...)
		for _, d := range deps {
			if d.ProjectID == t.Issue.ProjectID && d.IID == t.Issue.IID {
				return out, temporal.NewNonRetryableApplicationError("Issue depends on itself", "dependency", nil)
			}
			issue, e := c.GetIssue(ctx, d.ProjectID, d.IID)
			if e != nil {
				return out, e
			}
			if issue.State != "closed" {
				out.Ready = false
				t.Message = fmt.Sprintf("等待依赖 %d#%d", d.ProjectID, d.IID)
			}
		}
	case "preparing":
		for _, r := range t.Project.Repositories {
			if _, e = c.EnsureTarget(ctx, r); e != nil {
				return out, e
			}
		}
		env, e := a.gitEnv(t)
		if e != nil {
			return out, e
		}
		n, e := a.node(ctx, t)
		if e != nil {
			return out, e
		}
		w, e := n.PrepareWorkspace(ctx, t.ID, t.Project, env)
		if e != nil {
			return out, e
		}
		t.Workspace = w
	case "developing", "publishing", "reviewing":
		if in.Phase == "publishing" {
			changed := false
			for _, r := range t.Workspace.Revisions {
				changed = changed || r.Changed
			}
			if !changed {
				break
			}
		}
		ex, e := a.execute(ctx, in, t)
		if e != nil {
			return out, e
		}
		t.Executions[roleFor(in.Phase)] = ex
		if ex.Status != "succeeded" {
			message := "Agent execution " + ex.Status + ": " + ex.ID
			if ex.FailureReason != "" {
				message += ": " + ex.FailureReason
			}
			return StepResult{Task: t}, temporal.NewNonRetryableApplicationError(message, "agent_execution", nil)
		}
		if in.Phase == "publishing" {
			before := domain.Digest(t.Workspace.Revisions)
			n, e := a.node(ctx, t)
			if e != nil {
				return out, e
			}
			w, e := n.InspectWorkspace(ctx, t.Workspace, t.Project, nil)
			if e != nil {
				return out, e
			}
			if domain.Digest(w.Revisions) != before {
				t.Workspace = w
				t.Message = "发布阶段提交变化，重新验证"
				out.Task = t
				out.Rework = true
				return out, nil
			}
			for i, r := range t.Workspace.Revisions {
				if !r.Changed {
					continue
				}
				mr, e := c.FindMR(ctx, r.ProjectID, r.Branch, r.TargetBranch)
				if e != nil {
					return out, e
				}
				if mr == nil || mr.SHA != r.HeadSHA {
					return out, fmt.Errorf("missing or stale MR for %s", r.RepositoryID)
				}
				t.Workspace.Revisions[i].MRIID = mr.IID
				t.Workspace.Revisions[i].MRURL = mr.URL
			}
		}
		if in.Phase == "reviewing" {
			n, e := a.node(ctx, t)
			if e != nil {
				return out, e
			}
			w, e := n.InspectWorkspace(ctx, t.Workspace, t.Project, nil)
			if e != nil {
				return out, e
			}
			if domain.Digest(w.Revisions) != domain.Digest(t.Workspace.Revisions) {
				t.Workspace = w
				t.Message = "审查期间代码发生变化，重新验证"
				out.Rework = true
			}
		}
	case "verifying":
		resourceCtx, release, e := a.resourceSlot(ctx, in.ExecutionID)
		if e != nil {
			return out, e
		}
		defer release()
		ctx = resourceCtx
		env, e := a.gitEnv(t)
		if e != nil {
			return out, e
		}
		n, e := a.node(ctx, t)
		if e != nil {
			return out, e
		}
		w, e := n.RefreshTargets(ctx, t.Workspace, t.Project, env)
		if errors.Is(e, workspace.ErrNeedsSync) {
			t.Message = "目标分支已前进，先同步目标分支再验证"
			out.Task = t
			out.Rework = true
			return out, nil
		}
		if e != nil {
			return out, e
		}
		w, e = n.InspectWorkspace(ctx, w, t.Project, env)
		if e != nil {
			return out, e
		}
		t.Workspace = w
		if e = n.StartIntegration(ctx, w, t.Project.Integration, a.integrationEnv(t)); e != nil {
			return out, e
		}
		checks, e := n.VerifyIntegration(ctx, w, t.Project.Integration, a.integrationEnv(t))
		if e != nil {
			return out, e
		}
		t.Verification = checks
		required := 0
		for i := range t.Verification {
			t.Verification[i].RevisionDigest = domain.Digest(w.Revisions)
			if t.Verification[i].Required {
				required++
				out.Ready = out.Ready && t.Verification[i].Passed
			}
		}
		if required == 0 {
			return out, temporal.NewNonRetryableApplicationError("no required verification executed", "verification", nil)
		}
		// Release running services while review or a human decision is pending; reports persist.
		if e = n.StopIntegration(ctx, w, t.Project.Integration, a.integrationEnv(t)); e != nil {
			return out, e
		}
	case "acquiring_merge":
		fresh, e := c.GetIssue(ctx, t.Issue.ProjectID, t.Issue.IID)
		if e != nil {
			return out, e
		}
		t.Issue = fresh
		if requiresApproval(t) && t.ApprovalDigest != t.ReviewDigest {
			t.Stage = "waiting_approval"
			out.Ready = false
			break
		}
		for _, id := range domain.MergeOrder(t.Project.Repositories) {
			for _, r := range t.Workspace.Revisions {
				if r.RepositoryID != id || !r.Changed || r.Merged {
					continue
				}
				ok, e := a.Store.Lease(ctx, mergeLock(t, r), t.ID, 3*time.Minute)
				if e != nil {
					return out, e
				}
				if !ok {
					_ = a.Store.ReleaseLeases(ctx, t.ID)
					out.Ready = false
					break
				}
			}
			if !out.Ready {
				break
			}
		}
		if out.Ready {
			var reason string
			out.Ready, reason, e = a.gates(ctx, c, &t)
			if e != nil {
				_ = a.Store.ReleaseLeases(ctx, t.ID)
				return out, e
			}
			if reason != "" {
				t.Message = reason
				if strings.HasPrefix(reason, "STALE:") {
					out.Rework = true
				}
			}
			if !out.Ready {
				_ = a.Store.ReleaseLeases(ctx, t.ID)
			}
		}
	case "merging", "partial_merge":
		// Reconcile completed remote merges before retrying any agent action.
		for i, r := range t.Workspace.Revisions {
			if !r.Changed {
				continue
			}
			mr, e := c.GetMR(ctx, r.ProjectID, r.MRIID)
			if e != nil {
				return out, e
			}
			if !sameMR(mr, r) {
				return out, temporal.NewNonRetryableApplicationError("MR identity changed after review", "stale_review", nil)
			}
			if mr.State == "merged" {
				t.Workspace.Revisions[i].Merged = true
			}
		}
		ok, reason, e := a.gates(ctx, c, &t)
		if e != nil {
			return out, e
		}
		if !ok {
			t.Message = reason
			if reason == "manual approval required" {
				t.Stage = "waiting_approval"
			}
			out.Task = t
			out.Ready = false
			out.Rework = strings.HasPrefix(reason, "STALE:")
			return out, nil
		}
		for _, id := range domain.MergeOrder(t.Project.Repositories) {
			for i, r := range t.Workspace.Revisions {
				if r.RepositoryID != id || !r.Changed || r.Merged {
					continue
				}
				// One merge operation per execution keeps partial completion reviewable.
				ok, reason, e := a.gates(ctx, c, &t)
				if e != nil {
					return out, e
				}
				if !ok {
					out.Ready = false
					out.Rework = strings.HasPrefix(reason, "STALE:")
					t.Message = reason
					break
				}
				mi := in
				mi.ExecutionID = in.ExecutionID + "-" + id
				mi.Task = t
				ex, e := a.executeMerge(ctx, mi, t, r)
				if e != nil {
					return out, e
				}
				t.Executions["merge"] = ex
				mr, e := c.GetMR(ctx, r.ProjectID, r.MRIID)
				if e != nil {
					return out, e
				}
				if !sameMR(mr, r) {
					return out, temporal.NewNonRetryableApplicationError("merged MR differs from reviewed revision", "stale_review", nil)
				}
				if mr.State != "merged" {
					out.Ready = false
					t.Message = "部分合并未完成；核实原因后重试剩余 MR"
					break
				}
				t.Workspace.Revisions[i].Merged = true
				if e = a.Store.SaveTask(ctx, t); e != nil {
					return out, e
				}
			}
			if !out.Ready {
				break
			}
		}
		_ = a.Store.ReleaseLeases(ctx, t.ID)
	case "finalizing":
		fresh, e := c.GetIssue(ctx, t.Issue.ProjectID, t.Issue.IID)
		if e != nil {
			return out, e
		}
		t.Issue = fresh
		if requiresApproval(t) && t.ApprovalDigest != t.ReviewDigest {
			t.Stage = "waiting_approval"
			out.Task = t
			out.Ready = false
			return out, nil
		}
		for _, r := range t.Workspace.Revisions {
			if !r.Changed {
				continue
			}
			mr, e := c.GetMR(ctx, r.ProjectID, r.MRIID)
			if e != nil {
				return out, e
			}
			if !sameMR(mr, r) || mr.State != "merged" {
				return out, fmt.Errorf("cannot close Issue before MR %s merged", r.MRURL)
			}
		}
		// Cleanup is idempotent on the owning node so a failed close API call can
		// be retried without needing the deleted files.
		n, e := a.node(ctx, t)
		if e != nil {
			return out, e
		}
		if _, e = n.Cleanup(ctx, t.ID, t.Workspace, t.Project.Integration, a.integrationEnv(t)); e != nil {
			return out, e
		}
		if e = c.CloseIssue(ctx, t.Issue); e != nil {
			return out, e
		}
		_ = a.Store.ReleaseLeases(ctx, t.ID)
	default:
		return out, temporal.NewNonRetryableApplicationError("unknown Activity phase", "configuration", nil)
	}
	out.Task = t
	// GitLab progress failure is retried independently of expensive execution by the Sync activity.
	return out, nil
}
func roleFor(phase string) string {
	switch phase {
	case "reviewing":
		return "review"
	case "merging", "partial_merge":
		return "merge"
	default:
		return "dev"
	}
}
func requiresApproval(t domain.Task) bool {
	for _, r := range t.Workspace.Revisions {
		if r.Sensitive {
			return true
		}
	}
	for _, l := range t.Issue.Labels {
		if l == "no-auto-close" {
			return true
		}
	}
	return false
}
func mergeLock(t domain.Task, r domain.Revision) string {
	return fmt.Sprintf("merge:%s:%d:%s", domain.GitLabIdentity(t.Project.GitLabURL), r.ProjectID, r.TargetBranch)
}
func (a *Activities) gates(ctx context.Context, c *gitlab.Client, t *domain.Task) (bool, string, error) {
	if t.Issue.ProjectID > 0 && t.Issue.IID > 0 {
		fresh, e := c.GetIssue(ctx, t.Issue.ProjectID, t.Issue.IID)
		if e != nil {
			return false, "", e
		}
		t.Issue = fresh
	}
	if t.ReviewDigest == "" || t.ReviewDigest != domain.Digest(t.Workspace.Revisions) {
		return false, "STALE: review digest no longer matches", nil
	}
	if requiresApproval(*t) && t.ApprovalDigest != t.ReviewDigest {
		return false, "manual approval required", nil
	}
	required := 0
	for _, v := range t.Verification {
		if v.Required {
			required++
		}
		if v.Required && (!v.Passed || v.RevisionDigest != t.ReviewDigest) {
			return false, "verification not passed for reviewed revisions", nil
		}
	}
	if required == 0 {
		return false, "required verification is missing", nil
	}
	for i, r := range t.Workspace.Revisions {
		if !r.Changed {
			continue
		}
		mr, e := c.GetMR(ctx, r.ProjectID, r.MRIID)
		if e != nil {
			return false, "", e
		}
		if !sameMR(mr, r) {
			return false, "STALE: MR changed after review", nil
		}
		if mr.State == "merged" {
			t.Workspace.Revisions[i].Merged = true
			continue
		}
		closes, e := c.ClosingIssues(ctx, r.ProjectID, r.MRIID)
		if e != nil {
			return false, "", e
		}
		if len(closes) > 0 {
			return false, "STALE: remove closing issue references from MR and commits; the workflow closes issues after the full set", nil
		}
		if mr.SHA != r.HeadSHA || mr.TargetBranch != r.TargetBranch || mr.SourceBranch != r.Branch {
			return false, "STALE: MR changed after review", nil
		}
		target, e := c.GetBranch(ctx, r.ProjectID, r.TargetBranch)
		if e != nil {
			return false, "", e
		}
		if target.Commit.ID != r.BaseSHA {
			return false, "STALE: target branch advanced; development must synchronize and reverify", nil
		}
		if mr.HasConflicts {
			return false, "STALE: merge conflict", nil
		}
		if t.Project.RequireCI && mr.HeadPipeline != nil && (mr.HeadPipeline.Status == "failed" || mr.HeadPipeline.Status == "canceled") {
			return false, "STALE: required CI failed; inspect the pipeline and repair", nil
		}
		if mr.DetailedMergeStatus != "mergeable" && mr.DetailedMergeStatus != "ci_still_running" && mr.DetailedMergeStatus != "checking" && mr.DetailedMergeStatus != "unchecked" {
			return false, "GitLab merge condition: " + mr.DetailedMergeStatus, nil
		}
		ok, e := c.RequiredCIPassed(ctx, r.ProjectID, r.MRIID, r.HeadSHA, t.Project.RequireCI)
		if e != nil {
			return false, "", e
		}
		if !ok || mr.DetailedMergeStatus != "mergeable" {
			return false, "等待 GitLab CI 或合并检查", nil
		}
	}
	return true, "", nil
}
func (a *Activities) execute(ctx context.Context, in StepInput, t domain.Task) (domain.Execution, error) {
	return a.runAgent(ctx, in, t, nil)
}
func (a *Activities) executeMerge(ctx context.Context, in StepInput, t domain.Task, r domain.Revision) (domain.Execution, error) {
	return a.runAgent(ctx, in, t, &r)
}
func (a *Activities) runAgent(ctx context.Context, in StepInput, t domain.Task, merge *domain.Revision) (domain.Execution, error) {
	n, e := a.node(ctx, t)
	if e != nil {
		return domain.Execution{}, e
	}
	role := roleFor(in.Phase)
	cfg := t.Project.Development
	if role != "dev" {
		cfg = t.Project.Review
	}
	env := map[string]string{"GIT_AUTHOR_NAME": "swarmjet", "GIT_AUTHOR_EMAIL": "swarmjet@localhost", "GIT_COMMITTER_NAME": "swarmjet", "GIT_COMMITTER_EMAIL": "swarmjet@localhost"}
	for k, v := range a.AgentEnv {
		env[k] = v
	}
	// Review has no merge credential. Only the approved merge phase receives it.
	if role == "dev" || role == "merge" {
		ref := t.Project.CredentialRef
		if role == "merge" {
			ref = t.Project.MergeCredentialRef
		}
		token, e := a.Credential(ref)
		if e != nil {
			return domain.Execution{}, e
		}
		env["GITLAB_TOKEN"] = token
		env["GITLAB_URI"] = t.Project.GitLabURL
	}
	prior := t.Executions[role]
	if role == "merge" {
		prior = t.Executions["review"]
	}
	req := domain.AgentRequest{ExecutionID: in.ExecutionID, TaskID: t.ID, Role: role, Workspace: t.Workspace.Root, Prompt: prompt(t, in.Phase, merge), SessionID: prior.SessionID, Config: cfg, Env: env}
	// Durable execution IDs adopt an already-running container after Activity retries.
	slot := ""
	limit := a.Concurrency
	if limit < 1 {
		limit = 2
	}
	for slot == "" {
		if role == "merge" {
			if e := a.renewMerge(ctx, t); e != nil {
				return domain.Execution{}, e
			}
		}
		for i := 0; i < limit; i++ {
			name := fmt.Sprintf("agent-slot:%d", i)
			ok, e := a.Store.Lease(ctx, name, in.ExecutionID, 3*time.Minute)
			if e != nil {
				return domain.Execution{}, e
			}
			if ok {
				slot = name
				break
			}
		}
		if slot == "" {
			select {
			case <-ctx.Done():
				return domain.Execution{}, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	defer a.Store.ReleaseLeases(context.WithoutCancel(ctx), in.ExecutionID)
	priorRuns, e := n.TaskExecutions(ctx, t.ID)
	if e != nil {
		return domain.Execution{}, e
	}
	for _, old := range priorRuns {
		if old.ID != req.ExecutionID && (old.Status == "running" || old.Status == "starting") {
			if e = n.Cancel(ctx, old.ID); e != nil {
				return old, fmt.Errorf("cannot supersede live execution: %w", e)
			}
		}
	}
	if role == "merge" {
		if e := a.renewMerge(ctx, t); e != nil {
			return domain.Execution{}, e
		}
		c, e := a.client(t.Project, false)
		if e != nil {
			return domain.Execution{}, e
		}
		ok, reason, e := a.gates(ctx, c, &t)
		if e != nil {
			return domain.Execution{}, e
		}
		if !ok {
			return domain.Execution{}, fmt.Errorf("merge no longer ready: %s", reason)
		}
	}
	ex, e := n.Start(ctx, req)
	if e != nil {
		return ex, e
	}
	// On any lost supervision/error, keep the durable record and stop its container.
	defer func() {
		if ex.Status == "running" || ex.Status == "starting" {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = n.Cancel(cleanup, req.ExecutionID)
		}
	}()
	if e = a.Store.Execution(ctx, t.ID, role, ex); e != nil {
		return ex, e
	}
	for ex.Status == "starting" || ex.Status == "running" {
		select {
		case <-ctx.Done():
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = n.Cancel(cleanup, req.ExecutionID)
			cancel()
			return ex, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if ok, err := a.Store.Lease(ctx, slot, in.ExecutionID, 3*time.Minute); err != nil || !ok {
			_ = n.Cancel(context.WithoutCancel(ctx), req.ExecutionID)
			return ex, errors.New("agent slot lease lost")
		}
		if role == "merge" {
			for _, r := range t.Workspace.Revisions {
				if r.Changed && !r.Merged {
					ok, e := a.Store.Lease(ctx, mergeLock(t, r), t.ID, 3*time.Minute)
					if e != nil || !ok {
						_ = n.Cancel(context.WithoutCancel(ctx), req.ExecutionID)
						return ex, errors.New("merge lease lost")
					}
				}
			}
		}
		ex, e = n.Inspect(ctx, req.ExecutionID)
		if e != nil {
			return ex, e
		}
		if e = a.Store.Execution(ctx, t.ID, role, ex); e != nil {
			return ex, e
		}
		activity.RecordHeartbeat(ctx, ex.ID, ex.SessionID)
	}
	return ex, nil
}
func prompt(t domain.Task, phase string, merge *domain.Revision) string {
	b, _ := json.MarshalIndent(struct {
		Issue     domain.Issue
		Workspace domain.Workspace
		Notes     []string
		Checks    []domain.Verification
	}{t.Issue, t.Workspace, t.Notes, t.Verification}, "", "  ")
	base := `You are swarmjet's agent. Read applicable repository AGENTS.md and the Meta-Repo plan.md / architecture / integration instructions. Stay within this task's registered repositories and task branches. Preserve Git hooks. Record assumptions and continue despite ambiguous requirements; only return blocked when a concrete obstacle remains after reasonable attempts. Never include secrets in output. Changes to migrations, CI or deployment config may be prepared but require separate human approval before merging. Never execute production deployment. All task MRs and commit messages must reference the source issue WITHOUT closing keywords (Closes/Fixes/Resolves); the workflow closes it after every repository completes.\n`
	switch phase {
	case "developing":
		base += `Implement the issue across all necessary repositories using the same task workspace. Write meaningful tests. Fix validation failures. Synchronize with each configured origin target if it has advanced, preserving task changes; report conflicts honestly. Commit all changes. Do not push or create MRs yet; verification runs next. Use changed/no_change/blocked outcome.\n`
	case "publishing":
		base += `Code was verified. Push each changed task branch and create or update one GitLab MR per source/target pair using glab. Use the provided exact target per repository. Include verification and assumptions in MR descriptions. Do not modify code or merge. Use changed/blocked outcome.\n`
	case "reviewing":
		base += `Independently review the entire repository change set and actual verification reports. Check task coverage, cross-repository contracts and tests. Do not modify code, push, approve on GitLab or merge. Return approved, changes_requested with precise findings, or blocked. A no-change result must be independently justified.\n`
	case "merging", "partial_merge":
		m, _ := json.Marshal(merge)
		base += `Review, tests and any required human approval passed. Merge ONLY this MR using GitLab API via glab, with exact sha and target verification. If already merged report success; never force push or bypass GitLab checks. Do not change code. Return merged/blocked. MR: ` + string(m) + "\n"
	}
	return base + "\nTask snapshot:\n" + string(b)
}
func (a *Activities) Sync(ctx context.Context, t domain.Task) error {
	c, e := a.client(t.Project, false)
	if e != nil {
		return e
	}
	add := []string{"swarmjet:" + t.Stage}
	if len(t.Assumptions) > 0 {
		add = append(add, "needs-clarification")
	}
	fresh, e := c.GetIssue(ctx, t.Issue.ProjectID, t.Issue.IID)
	if e != nil {
		return e
	}
	remove := []string{}
	for _, l := range fresh.Labels {
		if strings.HasPrefix(l, "swarmjet:") && l != "swarmjet:"+t.Stage {
			remove = append(remove, l)
		}
	}
	if e = c.UpdateLabels(ctx, t.Issue, add, remove); e != nil {
		return e
	}
	body := fmt.Sprintf("**%s** · %s\n\n%s", t.Stage, t.ID, t.Message)
	for _, r := range t.Workspace.Revisions {
		if r.MRURL != "" {
			body += "\n- " + r.RepositoryID + ": " + r.MRURL
		}
	}
	for _, v := range t.Assumptions {
		body += "\n- 假设：" + v
	}
	for _, v := range t.Notes {
		body += "\n- 补充：" + v
	}
	return c.UpsertProgress(ctx, t.Issue, t.ID, body)
}

// ArtifactPath is useful to CLI diagnostics without exposing arbitrary server paths.
func ArtifactPath(dataDir, taskID string) string { return filepath.Join(dataDir, "runs", taskID) }
func EnvironmentCredential(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("missing credential reference %s", name)
	}
	return v, nil
}

func sameMR(m gitlab.MergeRequest, r domain.Revision) bool {
	return m.SHA == r.HeadSHA && m.SourceBranch == r.Branch && m.TargetBranch == r.TargetBranch
}
func (a *Activities) renewMerge(ctx context.Context, t domain.Task) error {
	for _, r := range t.Workspace.Revisions {
		if !r.Changed {
			continue
		}
		ok, e := a.Store.Lease(ctx, mergeLock(t, r), t.ID, 3*time.Minute)
		if e != nil {
			return e
		}
		if !ok {
			return fmt.Errorf("merge lease held by another task: %s", r.RepositoryID)
		}
	}
	return nil
}

func (a *Activities) resourceSlot(ctx context.Context, owner string) (context.Context, func(), error) {
	limit := a.Concurrency
	if limit < 1 {
		limit = 2
	}
	name := ""
	for name == "" {
		for i := 0; i < limit; i++ {
			candidate := fmt.Sprintf("agent-slot:%d", i)
			ok, e := a.Store.Lease(ctx, candidate, owner, 3*time.Minute)
			if e != nil {
				return ctx, nil, e
			}
			if ok {
				name = candidate
				break
			}
		}
		if name == "" {
			select {
			case <-ctx.Done():
				return ctx, nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	child, cancel := context.WithCancel(ctx)
	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-tick.C:
				ok, e := a.Store.Lease(child, name, owner, 3*time.Minute)
				if e != nil || !ok {
					cancel()
					return
				}
			}
		}
	}()
	return child, func() {
		cancel()
		cleanup, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = a.Store.ReleaseLeases(cleanup, owner)
	}, nil
}

func (a *Activities) workspaceManager(p domain.Project) (*workspace.Manager, error) {
	token, e := a.Credential(p.CredentialRef)
	if e != nil {
		return nil, e
	}
	w := *a.Workspace
	w.Env = map[string]string{}
	for k, v := range a.Workspace.Env {
		w.Env[k] = v
	}
	w.Env["GITLAB_TOKEN"] = token
	w.Env["GIT_TERMINAL_PROMPT"] = "0"
	if w.Env["GIT_ASKPASS"] == "" {
		w.Env["GIT_ASKPASS"] = "/usr/local/bin/swarmjet-askpass"
	}
	return &w, nil
}
