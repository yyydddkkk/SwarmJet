package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"swarmjet/internal/managedmeta"
	"swarmjet/internal/store"
	"swarmjet/internal/workspace"
)

type IntegrationRunner interface {
	Start(context.Context, domain.Workspace, domain.IntegrationConfig) error
	Verify(context.Context, domain.Workspace, domain.IntegrationConfig) ([]domain.Verification, error)
	Stop(context.Context, domain.Workspace, domain.IntegrationConfig) error
}
type Activities struct {
	resolvedIntegrationNetwork map[string]string
	NetworkEnv                 func(context.Context, domain.NetworkVersion) (map[string]string, error)
	Store                      *store.Store
	Workspace                  *workspace.Manager
	Agents                     execution.AgentBackend
	Integration                IntegrationRunner
	Nodes                      execution.NodeResolver
	IntegrationEnv             map[string]string
	DataDir                    string
	Concurrency                int
	Credential                 func(string) (string, error)
	// ProjectCredential resolves a project token with its GitLab address binding.
	ProjectCredential func(domain.Project, bool) (string, error)
	AgentEnv          map[string]string
}

// credentialFor resolves a project token. The bound resolver refuses to send a
// saved connection token to a different GitLab address.
func (a *Activities) credentialFor(p domain.Project, merge bool) (string, error) {
	if a.ProjectCredential != nil {
		return a.ProjectCredential(p, merge)
	}
	name := p.CredentialReference()
	if merge {
		name = p.MergeCredentialReference()
	}
	return a.Credential(name)
}

func (a *Activities) client(p domain.Project, merge bool) (*gitlab.Client, error) {
	name := p.CredentialReference()
	if merge {
		name = p.MergeCredentialReference()
	}
	token, e := a.credentialFor(p, merge)
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
	token, e := a.credentialFor(t.Project, false)
	if e != nil {
		return nil, temporal.NewNonRetryableApplicationError("credential unavailable: "+t.Project.CredentialReference(), "configuration", e)
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
	if t.NodeConfigVersion > 0 && a.NetworkEnv != nil {
		v, e := a.NetworkEnv(context.Background(), t.Network)
		if e != nil {
			return nil, e
		}
		for k, value := range v {
			env[k] = value
		}
	}
	return env, nil
}

func (a *Activities) integrationEnv(t domain.Task) map[string]string {
	var out map[string]string
	if t.NodeID == "" && len(a.IntegrationEnv) > 0 {
		out = map[string]string{}
		for k, v := range a.IntegrationEnv {
			out[k] = v
		}
	}
	if a.resolvedIntegrationNetwork != nil {
		if out == nil {
			out = map[string]string{}
		}
		for k, v := range a.resolvedIntegrationNetwork {
			out[k] = v
		}
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
	if in.Phase == "acquiring_merge" || in.Phase == "merging" || in.Phase == "partial_merge" {
		allowed, err := a.taskAutoMergeAllowed(ctx, t)
		if err != nil {
			return out, err
		}
		if !allowed {
			disabled := false
			t.AutoMergeOverride = &disabled
			t.Stage = "waiting_manual_merge"
			t.Message = "此任务已关闭自动合并，等待人工合并 MR"
			out.Task = t
			out.Ready = false
			return out, nil
		}
	}
	if t.NodeConfigVersion > 0 && a.NetworkEnv != nil && in.Phase != "cancel" && in.Phase != "recover" && in.Phase != "finalize" {
		env, e := a.NetworkEnv(ctx, t.Network)
		if e != nil {
			return out, e
		}
		copy := *a
		copy.resolvedIntegrationNetwork = env
		a = &copy
	}
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
			if e := n.StopIntegration(ctx, t.Workspace, taskIntegration(t), a.integrationEnv(t)); e != nil {
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
			key := fmt.Sprintf("%d/%d", d.ProjectID, d.IID)
			upstreamID := t.DependencyTasks[key]
			if upstreamID == "" && t.Round > 0 {
				upstream, err := a.Store.LatestTaskForIssue(ctx, t.Project.GitLabURL, domain.Issue{ProjectID: d.ProjectID, IID: d.IID})
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return out, err
				}
				if err == nil && upstream.Stage != "cancelled" {
					if t.DependencyTasks == nil {
						t.DependencyTasks = map[string]string{}
					}
					t.DependencyTasks[key] = upstream.ID
					upstreamID = upstream.ID
				}
			}
			if upstreamID != "" {
				upstream, err := a.Store.Task(ctx, upstreamID)
				if err != nil {
					return out, err
				}
				if upstream.Stage == "completed" {
					continue
				}
			} else {
				issue, err := c.GetIssue(ctx, d.ProjectID, d.IID)
				if err != nil {
					return out, err
				}
				if issue.State == "closed" {
					continue
				}
			}
			out.Ready = false
			t.Message = fmt.Sprintf("等待依赖 %d#%d", d.ProjectID, d.IID)
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
		if t.Project.ManagedMeta() {
			init, readErr := a.Store.Initialization(ctx, "init-"+t.Project.ID)
			if readErr != nil {
				return out, readErr
			}
			if pub := init.Publication; pub != nil && pub.Status == "published" {
				if pub.GitLabURL != t.Project.GitLabURL || pub.CredentialRef != t.Project.CredentialReference() || pub.Ref != t.Project.MetaRef {
					return out, temporal.NewNonRetryableApplicationError("published Meta connection differs from task snapshot", "managed_meta", nil)
				}
				// The control-plane host fetches the remote source of truth;
				// never use an execution node's askpass path on this host.
				fetchEnv := map[string]string{"GITLAB_TOKEN": env["GITLAB_TOKEN"], "GIT_ASKPASS": "/usr/local/bin/swarmjet-askpass"}
				if a.Workspace != nil && a.Workspace.Env["GIT_ASKPASS"] != "" {
					fetchEnv["GIT_ASKPASS"] = a.Workspace.Env["GIT_ASKPASS"]
				}
				if e := managedmeta.New(a.DataDir).SyncRemote(ctx, t.Project.ID, pub.Ref, pub.CloneURL, fetchEnv); e != nil {
					return out, e
				}
			}
		}
		// A managed Meta-Repo is owned by the control plane. Push it to the
		// assigned execution node before its workspace is prepared.
		if t.Project.ManagedMeta() && t.NodeID != "" {
			if e := a.syncManagedMeta(ctx, n, t.Project); e != nil {
				return out, e
			}
		}
		w, e := n.PrepareWorkspace(ctx, t.ID, t.Project, env)
		if e != nil {
			return out, e
		}
		t.Workspace = w
	case "developing", "publishing", "reviewing":
		if t.Workspace.Root == "" {
			// A prior remote call can lose its response while the Node still
			// retains the private worktree. Resume that durable Node state.
			n, err := a.node(ctx, t)
			if err != nil {
				return out, err
			}
			env, err := a.gitEnv(t)
			if err != nil {
				return out, err
			}
			t.Workspace, err = n.PrepareWorkspace(ctx, t.ID, t.Project, env)
			if err != nil {
				return out, err
			}
		}
		if in.Phase == "publishing" {
			// An Agent may fast-forward a repository it did not otherwise change
			// while resolving a stale target. Normalize baselines before deciding
			// which branches need publishing, or upstream-only commits look like
			// task changes and create unrelated MRs.
			n, err := a.node(ctx, t)
			if err != nil {
				return out, err
			}
			env, err := a.gitEnv(t)
			if err != nil {
				return out, err
			}
			current, err := n.InspectWorkspace(ctx, t.Workspace, t.Project, env)
			if err != nil {
				return out, err
			}
			normalized, err := n.RefreshTargets(ctx, current, t.Project, env)
			if errors.Is(err, workspace.ErrNeedsSync) {
				t.Workspace = current
				t.Message = "目标分支已前进：" + err.Error()
				out.Task = t
				out.Rework = true
				return out, nil
			}
			if err != nil {
				return out, err
			}
			current = normalized
			current, err = n.InspectWorkspace(ctx, current, t.Project, env)
			if err != nil {
				return out, err
			}
			t.Workspace = current
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
			return StepResult{Task: t}, agentExecutionError(ex, in.ExecutionID)
		}
		if ex.Result != nil {
			if in.Phase == "developing" {
				if t.Project.AcceptanceStrategy == "none" {
					t.AcceptanceCommands = nil
				} else {
					t.AcceptanceCommands = ex.Result.AcceptanceCommands
				}
				t.AcceptanceIncomplete = false
			}
			if ex.Result.HumanAcceptance != nil {
				t.HumanAcceptance = ex.Result.HumanAcceptance
			}
		}
		if in.Phase == "publishing" && ex.Result != nil && ex.Result.Outcome == "blocked" {
			t.Message = ex.Result.NeedsHelp
			out.Task = t
			return out, nil
		}
		if in.Phase == "developing" && t.Project.AcceptanceStrategy != "" && t.Project.AcceptanceStrategy != "none" && ex.Result != nil && ex.Result.Outcome != "blocked" {
			// New strategies publish before verification: capture the developer's
			// committed revisions now, rather than relying on the later verify step.
			node, err := a.node(ctx, t)
			if err != nil {
				return out, err
			}
			current, err := node.InspectWorkspace(ctx, t.Workspace, t.Project, nil)
			if err != nil {
				return out, err
			}
			t.Workspace = current
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
				if t.Round == 0 && ((t.Project.AcceptanceStrategy != "" && t.Project.AcceptanceStrategy != "none") || t.HumanAcceptance != nil) {
					description, e := manualDescription(t, r, mr.Description)
					if e != nil {
						return StepResult{Task: t}, temporal.NewNonRetryableApplicationError(e.Error(), "human_acceptance", e)
					}
					if description != mr.Description {
						if e = c.UpdateMRDescription(ctx, r.ProjectID, mr.IID, description); e != nil {
							return out, e
						}
					}
				}
				t.Workspace.Revisions[i].MRIID = mr.IID
				t.Workspace.Revisions[i].MRURL = mr.URL
			}
			if t.Round > 0 && !t.Project.IndependentReview() {
				if e := publishIssueReviewGuide(ctx, c, t); e != nil {
					return out, e
				}
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
			if t.Round > 0 && !out.Rework && ex.Result != nil && ex.Result.Outcome == "approved" {
				if e := publishIssueReviewGuide(ctx, c, t); e != nil {
					return out, e
				}
			}
		}
	case "verifying":
		resourceCtx, release, e := a.resourceSlot(ctx, in.ExecutionID, t.NodeID)
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
		if t.Project.AcceptanceStrategy == "none" {
			t.Verification = nil
			t.AcceptanceIncomplete = false
			break
		}
		if t.Project.AcceptanceStrategy != "" {
			out.Ready, out.Waiting, e = a.strategyVerification(ctx, c, n, &t)
			if e != nil {
				return out, e
			}
			after, inspectErr := n.InspectWorkspace(ctx, w, t.Project, env)
			if inspectErr != nil {
				return out, inspectErr
			}
			if domain.Digest(after.Revisions) != domain.Digest(w.Revisions) {
				t.Workspace = after
				out.Rework = true
				t.Message = "验收期间提交变化，必须重新验证"
			}
			if !out.Waiting && !out.Rework {
				if err := refreshMRVerification(ctx, c, t); err != nil {
					return out, err
				}
			}
			break
		}
		if e = n.StartIntegration(ctx, w, taskIntegration(t), a.integrationEnv(t)); e != nil {
			return out, e
		}
		checks, e := n.VerifyIntegration(ctx, w, taskIntegration(t), a.integrationEnv(t))
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
		if e = n.StopIntegration(ctx, w, taskIntegration(t), a.integrationEnv(t)); e != nil {
			return out, e
		}
	case "acquiring_merge":
		fresh, e := c.GetIssue(ctx, t.Issue.ProjectID, t.Issue.IID)
		if e != nil {
			return out, e
		}
		t.Issue = fresh
		if t.Round == 0 && t.AutoMergeEnabled() && requiresApproval(t) && t.ApprovalDigest != t.ReviewDigest {
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
	case "waiting_manual_merge":
		// Auto-merge is off. Required verification must still be current for the
		// reviewed revision set, then the task waits until every changed MR is
		// actually merged before the workflow closes the source issue.
		if t.ReviewDigest == "" || t.ReviewDigest != domain.Digest(t.Workspace.Revisions) {
			t.Message = "审查摘要已变化，需要重新验证"
			out.Task = t
			out.Rework = true
			return out, nil
		}
		if t.Project.AcceptanceStrategy != "none" {
			required := 0
			for _, v := range t.Verification {
				if !v.Required {
					continue
				}
				required++
				if !t.AcceptanceIncomplete && (!v.Passed || v.RevisionDigest != t.ReviewDigest) {
					t.Message = "必需验收未通过或已过期，需要重新验证"
					out.Task = t
					out.Rework = true
					return out, nil
				}
			}
			if required == 0 && !t.AcceptanceIncomplete {
				return out, temporal.NewNonRetryableApplicationError("no required verification executed", "verification", nil)
			}
		}
		allMerged := true
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
				continue
			}
			allMerged = false
		}
		if allMerged {
			out.Ready = true
		} else {
			out.Ready = false
			t.Message = "等待真人验收并人工合并全部 MR"
			if t.AcceptanceIncomplete {
				t.Message = "验收不完整，禁止自动合并；请查看 MR 真人验收清单"
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
				if ex.Status != "succeeded" {
					return StepResult{Task: t}, agentExecutionError(ex, mi.ExecutionID)
				}
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
		if t.Round == 0 && t.AutoMergeEnabled() && requiresApproval(t) && t.ApprovalDigest != t.ReviewDigest {
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
		if _, e = n.Cleanup(ctx, t.ID, t.Workspace, taskIntegration(t), a.integrationEnv(t)); e != nil {
			return out, e
		}
		if t.Round == 0 {
			if e = c.CloseIssue(ctx, t.Issue); e != nil {
				return out, e
			}
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
func (a *Activities) taskAutoMergeAllowed(ctx context.Context, t domain.Task) (bool, error) {
	if !t.AutoMergeEnabled() {
		return false, nil
	}
	if a.Store == nil {
		return true, nil
	}
	latest, err := a.Store.LatestAutoMergeRequest(ctx, t.ID)
	if err != nil {
		return false, err
	}
	return latest == nil || *latest, nil
}
func (a *Activities) gates(ctx context.Context, c *gitlab.Client, t *domain.Task) (bool, string, error) {
	if !t.AutoMergeEnabled() {
		return false, "此任务已关闭自动合并", nil
	}
	if t.AcceptanceIncomplete {
		return false, "验收不完整，只能人工处理，禁止自动合并", nil
	}

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
	if t.Round == 0 && requiresApproval(*t) && t.ApprovalDigest != t.ReviewDigest {
		return false, "manual approval required", nil
	}
	if t.Project.AcceptanceStrategy != "none" {
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
		if (t.Project.RequireCI || t.Project.AcceptanceStrategy == "gitlab_ci") && mr.HeadPipeline != nil && (mr.HeadPipeline.Status == "failed" || mr.HeadPipeline.Status == "canceled") {
			return false, "STALE: required CI failed; inspect the pipeline and repair", nil
		}
		if mr.DetailedMergeStatus != "mergeable" && mr.DetailedMergeStatus != "ci_still_running" && mr.DetailedMergeStatus != "checking" && mr.DetailedMergeStatus != "unchecked" {
			return false, "GitLab merge condition: " + mr.DetailedMergeStatus, nil
		}
		ok, e := c.RequiredCIPassed(ctx, r.ProjectID, r.MRIID, r.HeadSHA, t.Project.RequireCI || t.Project.AcceptanceStrategy == "gitlab_ci")
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
	return withResultRecovery(ctx, in, t, roleFor(in.Phase), func(ctx context.Context, in StepInput, t domain.Task, repair bool) (domain.Execution, error) {
		return a.runAgent(ctx, in, t, nil, repair)
	})
}
func agentExecutionError(ex domain.Execution, originalID string) error {
	message := "Agent execution " + ex.Status + ": " + ex.ID
	if ex.ID != originalID {
		message = "Agent 结果补写失败: " + ex.ID
	}
	if ex.FailureReason != "" {
		message += ": " + ex.FailureReason
	}
	return temporal.NewNonRetryableApplicationError(message, "agent_execution", nil)
}
func (a *Activities) executeMerge(ctx context.Context, in StepInput, t domain.Task, r domain.Revision) (domain.Execution, error) {
	return withResultRecovery(ctx, in, t, "merge", func(ctx context.Context, in StepInput, t domain.Task, repair bool) (domain.Execution, error) {
		return a.runAgent(ctx, in, t, &r, repair)
	})
}

// A successful CLI process without a valid result is an incomplete handoff.
// Give it one new, isolated result-only execution. A blocked task with the same
// failure resumes at that handoff rather than repeating already committed work.
func withResultRecovery(ctx context.Context, in StepInput, t domain.Task, role string, run func(context.Context, StepInput, domain.Task, bool) (domain.Execution, error)) (domain.Execution, error) {
	if t.Executions == nil {
		t.Executions = map[string]domain.Execution{}
	}
	if old := t.Executions[role]; invalidAgentResult(old) || interruptedResultRepair(old) {
		in.ExecutionID = resultRecoveryID(in.ExecutionID)
		return run(ctx, in, t, true)
	}
	first, err := run(ctx, in, t, false)
	if err != nil || !invalidAgentResult(first) {
		return first, err
	}
	t.Executions[role] = first
	in.ExecutionID = resultRecoveryID(in.ExecutionID)
	return run(ctx, in, t, true)
}

func resultRecoveryID(id string) string {
	if len(id)+len("-result") <= 128 {
		return id + "-result"
	}
	hash := sha256.Sum256([]byte(id))
	return id[:128-17] + "-" + hex.EncodeToString(hash[:8])
}

func invalidAgentResult(ex domain.Execution) bool {
	return ex.Status == "failed" && ex.ExitCode == 0 && (ex.FailureCode == "invalid_result" || strings.HasPrefix(ex.FailureReason, "invalid result.json:"))
}

func agentSettings(t domain.Task, role string) (domain.AgentConfig, string, string) {
	if role == "review" {
		return t.Project.Review, t.Executions["review"].SessionID, ""
	}
	if role == "merge" {
		if t.Project.IndependentReview() {
			return t.Project.Review, t.Executions["review"].SessionID, ""
		}
		prior := t.Executions["merge"]
		if prior.SessionID == "" {
			prior = t.Executions["dev"]
		}
		return t.Project.Development, prior.SessionID, "dev"
	}
	return t.Project.Development, t.Executions[role].SessionID, ""
}

func interruptedResultRepair(ex domain.Execution) bool {
	return ex.Status == "cancelled" && (ex.ResultRepair || strings.HasSuffix(ex.ID, "-result"))
}

func (a *Activities) runAgent(ctx context.Context, in StepInput, t domain.Task, merge *domain.Revision, repair bool) (domain.Execution, error) {
	n, e := a.node(ctx, t)
	if e != nil {
		return domain.Execution{}, e
	}
	role := roleFor(in.Phase)
	cfg, sessionID, profileScope := agentSettings(t, role)
	env := map[string]string{"GIT_AUTHOR_NAME": "swarmjet", "GIT_AUTHOR_EMAIL": "swarmjet@localhost", "GIT_COMMITTER_NAME": "swarmjet", "GIT_COMMITTER_EMAIL": "swarmjet@localhost"}
	if t.NodeConfigVersion > 0 {
		env = map[string]string{}
	}
	if t.NodeConfigVersion == 0 {
		for k, v := range a.AgentEnv {
			env[k] = v
		}
	}
	// Review has no merge credential. Only the approved merge phase receives it.
	if role == "dev" || role == "merge" {
		token, e := a.credentialFor(t.Project, role == "merge")
		if e != nil {
			return domain.Execution{}, e
		}
		env["GITLAB_TOKEN"] = token
		env["GITLAB_URI"] = t.Project.GitLabURL
	}
	if t.NodeConfigVersion > 0 && a.NetworkEnv != nil {
		v, e := a.NetworkEnv(ctx, t.Network)
		if e != nil {
			return domain.Execution{}, e
		}
		for k, value := range v {
			env[k] = value
		}
	}
	req := domain.AgentRequest{ExecutionID: in.ExecutionID, TaskID: t.ID, Role: role, Workspace: t.Workspace.Root, Prompt: prompt(t, in.Phase, merge), SessionID: sessionID, ProfileScope: profileScope, Config: cfg, Env: env}
	if repair {
		req.SessionID = "" // start with a small, fresh context
		req.Prompt = resultRecoveryPrompt(t, in.Phase, merge, t.Executions[role])
		req.ReadOnlyWorkspace = true
		if req.Config.TimeoutSeconds == 0 || req.Config.TimeoutSeconds > 900 {
			req.Config.TimeoutSeconds = 900
		}
	}
	// Durable execution IDs adopt an already-running container after Activity retries.
	slot := ""
	limit := a.Concurrency
	if t.NodeConfigVersion > 0 {
		cfg, e := a.Store.NodeConfig(ctx, t.NodeID)
		if e != nil {
			return domain.Execution{}, e
		}
		limit = cfg.Capacity
	}
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
			if t.NodeID != "" {
				name = fmt.Sprintf("agent-slot:%s:%d", t.NodeID, i)
			}
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
func resultRecoveryPrompt(t domain.Task, phase string, merge *domain.Revision, failed domain.Execution) string {
	context, _ := json.MarshalIndent(struct {
		Issue               domain.Issue
		Round               int
		TriggerText         string
		Project             domain.Project
		Workspace           domain.Workspace
		Verification        []domain.Verification
		AcceptanceCommands  []domain.AcceptanceCommand
		HumanAcceptance     *domain.ManualAcceptance
		Phase               string
		Merge               *domain.Revision
		PreviousExecutionID string
	}{t.Issue, t.Round, t.TriggerText, t.Project, t.Workspace, t.Verification, t.AcceptanceCommands, t.HumanAcceptance, phase, merge, failed.ID}, "", "  ")
	return `The preceding Agent CLI process exited successfully but did not provide a valid result.json. This is a result handoff recovery, not a new work attempt. The task source tree is mounted read-only. Inspect the existing task worktree, commits, relevant repository instructions, and verification reports to determine what actually happened. Do not edit files, run tests that write files, commit, push, create or change MRs, or merge. Never claim work was done without evidence; if the outcome cannot be determined, return blocked with a concrete needsHelp. Do not include secrets.

For developing or publishing, include humanAcceptance with prerequisites, concrete steps and expected results, and notes. For acceptanceStrategy=repository, statically identify acceptanceCommands from applicable repository files; the platform executes them and does not trust claims of passing checks. Use the allowed outcome for the role and phase: dev developing=changed/no_change/blocked; dev publishing=changed/blocked; review=approved/changes_requested/blocked; merge=merged/blocked. For publishing or merging, inspect GitLab state without mutation before reporting success.

` + gitLabOutputLanguage + `

Task context:
` + string(context)
}

const gitLabOutputLanguage = `Write GitLab MR titles, descriptions, and comments in natural Chinese by default, including review feedback. Write user-facing result fields (summary, assumptions, findings, needsHelp, and humanAcceptance) in Chinese so the platform's Issue comments also read naturally. Keep code identifiers, file paths, commands, API names, product names, and established technical terms in their original form where that reads better; do not translate them mechanically. Follow an explicit language requirement in the task or repository when present.`

func prompt(t domain.Task, phase string, merge *domain.Revision) string {
	b, _ := json.MarshalIndent(struct {
		Issue                domain.Issue
		Round                int
		PreviousTaskID       string
		TriggerText          string
		Workspace            domain.Workspace
		Notes                []string
		Checks               []domain.Verification
		AcceptanceCommands   []domain.AcceptanceCommand
		AcceptanceIncomplete bool
	}{t.Issue, t.Round, t.PreviousTaskID, t.TriggerText, t.Workspace, t.Notes, t.Verification, t.AcceptanceCommands, t.AcceptanceIncomplete}, "", "  ")
	base := `You are swarmjet's agent. Read applicable repository AGENTS.md and the Meta-Repo plan.md / architecture / integration instructions. Stay within this task's registered repositories and task branches. Preserve Git hooks. Record assumptions and continue despite ambiguous requirements; only return blocked when a concrete obstacle remains after reasonable attempts. Never include secrets in output. Changes to migrations, CI or deployment config may be prepared but require separate human approval before merging. Never execute production deployment. All task MRs and commit messages must reference the source issue WITHOUT closing keywords (Closes/Fixes/Resolves); the workflow closes it after every repository completes.\n`
	if t.Round > 0 {
		base = `You are swarmjet's agent. Read applicable repository AGENTS.md and the Meta-Repo plan.md / architecture / integration instructions. Stay within this task's registered repositories and task branches. Preserve Git hooks. Record assumptions and continue despite ambiguous requirements; only return blocked when a concrete obstacle remains after reasonable attempts. Never include secrets in output. Never execute production deployment. This is one round of a long-lived source Issue. The Issue stays open after merge. Address the current Issue description and the TriggerText for this round; use PreviousTaskID only as prior-delivery context. All task MRs and commit messages must reference the source Issue WITHOUT closing keywords (Closes/Fixes/Resolves). GitLab merge checks remain mandatory. Independent review and platform verification apply only when enabled in the project.\n`
	}
	base += "\n" + gitLabOutputLanguage + "\n"

	config, _ := json.Marshal(t.Project)
	base += "\nProject acceptance policy: " + string(config) + "\n"
	plan, _ := json.Marshal(t.HumanAcceptance)
	base += "\nHuman acceptance plan: " + string(plan) + "\n"
	if t.Round > 0 {
		base += `For developing and publishing, return humanAcceptance in the result: {"prerequisites":["test environment, account/role, setup data, start instructions or URLs; explicitly state unknowns"],"steps":[{"action":"specific user operation or command","expected":"observable expected result"}],"notes":["edge cases, unverified areas, risks and cleanup"]}. The platform publishes this as an informational post-merge review guide on the source Issue. Do not put a human sign-off checklist in MR descriptions or claim a person tested the change. Do not use production data or expose credentials. Update the guide when code changes.
`
	} else {
		base += `For developing and publishing, return humanAcceptance in the result: {"prerequisites":["test environment, account/role, setup data, start instructions or URLs; explicitly state unknowns"],"steps":[{"action":"specific user operation or command","expected":"observable expected result"}],"notes":["edge cases, unverified areas, risks and cleanup"]}. Include a prominent Chinese heading "真人测试验收" at the very top of every MR description, with unchecked checkboxes, concrete steps and expected results. Never claim human sign-off or tick human checkboxes. Do not use production data or expose credentials. Update the checklist when code changes.
`
	}
	base += `
For acceptanceStrategy=repository, statically read AGENTS.md, package scripts, CI and development docs and return acceptanceCommands:[{"name":"check name","path":"exact registered repository path","argv":["executable","argument"],"evidence":"repository file and rule explaining why this check covers the change"}]. The platform executes these commands; your claimed pass is not evidence. If no applicable checks can be identified return an empty list and explain what a human must verify. The verification image and toolchain are platform configuration, not something you may invent.
For gitlab_ci, publish branches and MRs first, never use ci.skip; the platform waits for the exact commit CI. When independent review is enabled, reviewers assess the adequacy of the checks and the human checklist, including task coverage and missing/weak tests. Missing infrastructure is not a passing check; describe it explicitly. The platform must prohibit automatic merge when enabled checks are incomplete.
`
	switch phase {
	case "developing":
		base += `Implement the issue across all necessary repositories using the same task workspace. Write meaningful tests. Fix validation failures. Synchronize with each configured origin target if it has advanced, preserving task changes; report conflicts honestly. Commit all changes. Do not push or create MRs yet; publishing and verification are orchestrated by the platform. Use changed/no_change/blocked outcome.\n`
	case "publishing":
		base += `Push each changed task branch and create or update one GitLab MR per source/target pair using glab. Use the provided exact target per repository. Include verification and assumptions in MR descriptions. Do not modify code or merge. Use changed/blocked outcome.\n`
	case "reviewing":
		if t.Project.AcceptanceStrategy == "none" {
			base += `Independently review the entire repository change set and development test evidence. Extra platform acceptance is disabled for this project. Check task coverage, cross-repository contracts and tests; request changes if evidence is inadequate. Do not modify code, push, approve on GitLab or merge. Return approved, changes_requested with precise findings, or blocked. A no-change result must be independently justified.\n`
		} else {
			base += `Independently review the entire repository change set and actual verification reports. Check task coverage, cross-repository contracts and tests. Do not modify code, push, approve on GitLab or merge. Return approved, changes_requested with precise findings, or blocked. A no-change result must be independently justified.\n`
		}
	case "merging", "partial_merge":
		m, _ := json.Marshal(merge)
		base += `Merge ONLY this MR using GitLab API via glab, with exact sha and target verification. If already merged report success; never force push or bypass GitLab checks. Do not change code. Return merged/blocked. MR: ` + string(m) + "\n"
	}
	return base + "\nTask snapshot:\n" + string(b)
}
func (a *Activities) Sync(ctx context.Context, t domain.Task) error {
	c, e := a.client(t.Project, false)
	if e != nil {
		return e
	}
	latest, e := a.Store.LatestTaskForIssue(ctx, t.Project.GitLabURL, t.Issue)
	if e != nil {
		return e
	}
	if latest.ID == t.ID {
		add := []string{"swarmjet:" + t.Stage}
		if len(t.Assumptions) > 0 {
			add = append(add, "needs-clarification")
		}
		fresh, err := c.GetIssue(ctx, t.Issue.ProjectID, t.Issue.IID)
		if err != nil {
			return err
		}
		remove := []string{}
		for _, l := range fresh.Labels {
			if strings.HasPrefix(l, "swarmjet:") && l != "swarmjet:"+t.Stage || l == "needs-clarification" && len(t.Assumptions) == 0 {
				remove = append(remove, l)
			}
		}
		if err = c.UpdateLabels(ctx, t.Issue, add, remove); err != nil {
			return err
		}
	}
	body := fmt.Sprintf("**%s** · %s\n\n%s", t.Stage, t.ID, t.Message)
	if t.Round > 0 {
		body = fmt.Sprintf("**%s** · %s · 第 %d 轮\n\n%s", t.Stage, t.ID, t.Round, t.Message)
	}
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

func (a *Activities) resourceSlot(ctx context.Context, owner string, nodeID string) (context.Context, func(), error) {
	limit := a.Concurrency
	if limit < 1 {
		limit = 2
	}
	name := ""
	for name == "" {
		for i := 0; i < limit; i++ {
			candidate := fmt.Sprintf("agent-slot:%d", i)
			if nodeID != "" {
				candidate = fmt.Sprintf("agent-slot:%s:%d", nodeID, i)
			}
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

// syncManagedMeta exports the control-plane owned managed Meta-Repo and
// installs it on the task's execution node. The transfer is bounded and
// idempotent, so a retried activity cannot lose or duplicate the repository.
func (a *Activities) syncManagedMeta(ctx context.Context, n execution.Node, p domain.Project) error {
	if a.DataDir == "" {
		return temporal.NewNonRetryableApplicationError("managed Meta-Repo transfer requires a data directory", "managed_meta", nil)
	}
	bundle, e := managedmeta.New(a.DataDir).Bundle(ctx, p.ID)
	if e != nil {
		return temporal.NewNonRetryableApplicationError("managed Meta-Repo bundle unavailable: "+e.Error(), "managed_meta", e)
	}
	return n.SyncManagedMeta(ctx, p.ID, p.MetaRef, bundle)
}

func (a *Activities) workspaceManager(p domain.Project) (*workspace.Manager, error) {
	token, e := a.credentialFor(p, false)
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
