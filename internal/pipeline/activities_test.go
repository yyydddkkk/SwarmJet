package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
	"github.com/yyydddkkk/SwarmJet/internal/workspace"
)

type failingPublishNode struct {
	execution.Node
	prepared domain.Workspace
}

func (n *failingPublishNode) PrepareWorkspace(context.Context, string, domain.Project, map[string]string) (domain.Workspace, error) {
	return n.prepared, nil
}
func (n *failingPublishNode) InspectWorkspace(_ context.Context, ws domain.Workspace, _ domain.Project, _ map[string]string) (domain.Workspace, error) {
	return ws, nil
}
func (n *failingPublishNode) RefreshTargets(context.Context, domain.Workspace, domain.Project, map[string]string) (domain.Workspace, error) {
	return domain.Workspace{}, fmt.Errorf("%w: repository client target advanced", workspace.ErrNeedsSync)
}

func TestPublishingKeepsWorkspaceWhenRemoteSyncFails(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint("missing=", missing), func(t *testing.T) {
			ws := domain.Workspace{TaskID: "task", Root: "/node/task", Revisions: []domain.Revision{{RepositoryID: "client", Changed: true}}}
			original := ws
			if missing {
				original = domain.Workspace{}
			}
			a := Activities{Nodes: fixedNodeResolver{node: &failingPublishNode{prepared: ws}}, ProjectCredential: func(domain.Project, bool) (string, error) { return "token", nil }}
			task := domain.Task{ID: "task", NodeID: "node", Project: domain.Project{ID: "project", GitLabURL: "http://gitlab.invalid"}, Workspace: original}
			result, err := a.Step(context.Background(), StepInput{Phase: domain.StagePublishing, Task: task, ExecutionID: "publish"})
			if err != nil || !result.Rework || result.Task.Workspace.Root != ws.Root || !strings.Contains(result.Task.Message, "client") {
				t.Fatalf("remote sync lost workspace: %+v %v", result, err)
			}
		})
	}
}

func TestResultRecoveryRunsOnceInFreshReadOnlyExecution(t *testing.T) {
	task := domain.Task{ID: "task", Executions: map[string]domain.Execution{}}
	in := StepInput{Phase: domain.StageDeveloping, ExecutionID: "task-developing-3"}
	calls := 0
	run := func(_ context.Context, step StepInput, got domain.Task, repair bool) (domain.Execution, error) {
		calls++
		if calls == 1 {
			if repair || step.ExecutionID != in.ExecutionID {
				t.Fatalf("first execution: %+v repair=%v", step, repair)
			}
			return domain.Execution{ID: step.ExecutionID, Status: "failed", FailureCode: "invalid_result"}, nil
		}
		if calls != 2 || !repair || step.ExecutionID != in.ExecutionID+"-result" || got.Executions["dev"].ID != in.ExecutionID {
			t.Fatalf("repair execution: %+v repair=%v prior=%+v", step, repair, got.Executions["dev"])
		}
		return domain.Execution{ID: step.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "changed", Summary: "committed"}}, nil
	}
	ex, err := withResultRecovery(context.Background(), in, task, "dev", run)
	if err != nil || ex.Status != "succeeded" || calls != 2 {
		t.Fatalf("result recovery: %+v calls=%d err=%v", ex, calls, err)
	}
}

func TestResultRecoveryResumesBlockedHandoffWithoutRepeatingWork(t *testing.T) {
	for _, previous := range []domain.Execution{
		{ID: "previous", Status: "failed", FailureCode: "invalid_result"},
		{ID: "legacy", Status: "failed", FailureReason: "invalid result.json: open result.json: no such file or directory"},
		{ID: "paused-result", Status: "cancelled", ResultRepair: true},
		{ID: "legacy-result", Status: "cancelled"},
	} {
		task := domain.Task{Executions: map[string]domain.Execution{"dev": previous}}
		calls := 0
		ex, err := withResultRecovery(context.Background(), StepInput{Phase: domain.StageDeveloping, ExecutionID: "retry"}, task, "dev", func(_ context.Context, step StepInput, _ domain.Task, repair bool) (domain.Execution, error) {
			calls++
			if !repair || step.ExecutionID != "retry-result" {
				t.Fatalf("repeated development: %+v repair=%v", step, repair)
			}
			return domain.Execution{ID: step.ExecutionID, Status: "failed", FailureCode: "invalid_result"}, nil
		})
		if err != nil || calls != 1 || ex.Status != "failed" {
			t.Fatalf("unbounded recovery: %+v calls=%d err=%v", ex, calls, err)
		}
	}
}

func TestResultRecoveryIgnoresOtherExecutionFailures(t *testing.T) {
	calls := 0
	ex, err := withResultRecovery(context.Background(), StepInput{ExecutionID: "run"}, domain.Task{Executions: map[string]domain.Execution{}}, "dev", func(_ context.Context, step StepInput, _ domain.Task, repair bool) (domain.Execution, error) {
		calls++
		if repair {
			t.Fatal("ordinary Agent failure triggered result recovery")
		}
		return domain.Execution{ID: step.ExecutionID, Status: "failed", ExitCode: 1}, nil
	})
	if err != nil || calls != 1 || ex.Status != "failed" {
		t.Fatalf("ordinary failure changed: %+v calls=%d err=%v", ex, calls, err)
	}
}

func TestResultRecoveryPromptOnlyDescribesExistingWork(t *testing.T) {
	task := domain.Task{Issue: domain.Issue{Title: "show two decimal places"}}
	prompt := resultRecoveryPrompt(task, domain.StageDeveloping, nil, domain.Execution{ID: "first"})
	for _, want := range []string{"read-only", "Do not edit files", "acceptanceCommands", "show two decimal places", "first", `"steps":[{"action":`, "When outcome is blocked, omit humanAcceptance"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %q from repair prompt", want)
		}
	}
}

func TestMergeWithoutReviewReusesDevelopmentSession(t *testing.T) {
	task := domain.Task{Project: domain.Project{
		ReviewMode:  "none",
		Development: domain.AgentConfig{Adapter: "dst", Image: "dev-image"},
		Review:      domain.AgentConfig{Adapter: "codex", Image: "review-image"},
	}, Executions: map[string]domain.Execution{"dev": {SessionID: "dev-session"}}}
	cfg, session, scope := agentSettings(task, "merge")
	if cfg.Adapter != "dst" || session != "dev-session" || scope != "dev" {
		t.Fatalf("merge opened another Agent context: adapter=%s session=%s scope=%s", cfg.Adapter, session, scope)
	}
	task.Project.ReviewMode = "independent"
	task.Executions["review"] = domain.Execution{SessionID: "review-session"}
	cfg, session, scope = agentSettings(task, "merge")
	if cfg.Adapter != "codex" || session != "review-session" || scope != "" {
		t.Fatalf("independent review context changed: adapter=%s session=%s scope=%s", cfg.Adapter, session, scope)
	}
}

func TestResultRecoveryIDStaysWithinRunnerLimit(t *testing.T) {
	first := resultRecoveryID(strings.Repeat("a", 128))
	second := resultRecoveryID(strings.Repeat("a", 127) + "b")
	if len(first) != 128 || first == second {
		t.Fatalf("result execution IDs collide or exceed limit: %q %q", first, second)
	}
}

func TestWorkspaceManagerPreservesNativeAskpass(t *testing.T) {
	manager := workspace.New(t.TempDir(), "meta")
	manager.Env = map[string]string{"GIT_ASKPASS": "/native/scripts/askpass.sh"}
	a := Activities{Workspace: manager, Credential: func(string) (string, error) { return "test-token", nil }}
	private, err := a.workspaceManager(domain.Project{CredentialRef: "GITLAB_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if private.Env["GIT_ASKPASS"] != manager.Env["GIT_ASKPASS"] {
		t.Fatal("native askpass replaced by container-only path")
	}
	if private.Env["GITLAB_TOKEN"] != "test-token" || manager.Env["GITLAB_TOKEN"] != "" {
		t.Fatal("per-project credentials must only reach the private manager")
	}
	manager.Env = nil
	private, err = a.workspaceManager(domain.Project{})
	if err != nil || private.Env["GIT_ASKPASS"] != "/usr/local/bin/swarmjet-askpass" {
		t.Fatal("container default must remain available", err)
	}
}

func reviewedGateTask() domain.Task {
	revisions := []domain.Revision{{RepositoryID: "api", ProjectID: 12, BaseSHA: "base-sha", HeadSHA: "reviewed-sha", TargetBranch: "release", Branch: "swarmjet/task", Changed: true, MRIID: 42}}
	task := domain.Task{ID: "task", Stage: domain.StageMerging, Project: domain.Project{ID: "project", RequireCI: true, Repositories: []domain.Repository{{ID: "api", ProjectID: 12, TargetBranch: "release"}}}, Workspace: domain.Workspace{Revisions: revisions}}
	task.ReviewDigest = domain.Digest(revisions)
	task.Verification = []domain.Verification{{Name: "integration", Required: true, Passed: true, RevisionDigest: task.ReviewDigest}}
	return task
}
func reviewedMR() gitlab.MergeRequest {
	return gitlab.MergeRequest{IID: 42, SHA: "reviewed-sha", SourceBranch: "swarmjet/task", TargetBranch: "release", State: "opened", DetailedMergeStatus: "mergeable", HeadPipeline: &gitlab.Pipeline{SHA: "reviewed-sha", Status: "success"}}
}
func gateServer(t *testing.T, mr func(int) gitlab.MergeRequest, target string) (*gitlab.Client, string, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("gate attempted mutation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/12/merge_requests/42/closes_issues":
			json.NewEncoder(w).Encode([]any{})
		case "/api/v4/projects/12/merge_requests/42":
			requests++
			json.NewEncoder(w).Encode(mr(requests))
		case "/api/v4/projects/12/repository/branches/release":
			json.NewEncoder(w).Encode(map[string]any{"name": "release", "commit": map[string]string{"id": target}})
		default:
			t.Errorf("unexpected API path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return gitlab.New(server.URL, "fake-token"), server.URL, &requests
}
func TestGatesRejectChangedMRIdentityEvenWhenMerged(t *testing.T) {
	for _, state := range []string{"opened", "merged"} {
		for _, field := range []string{"sha", "source", "target"} {
			t.Run(state+"/"+field, func(t *testing.T) {
				task := reviewedGateTask()
				mr := reviewedMR()
				mr.State = state
				switch field {
				case "sha":
					mr.SHA = "other-sha"
				case "source":
					mr.SourceBranch = "other-source"
				case "target":
					mr.TargetBranch = "main"
				}
				client, _, _ := gateServer(t, func(int) gitlab.MergeRequest { return mr }, "base-sha")
				ready, reason, err := (&Activities{}).gates(context.Background(), client, &task)
				if err != nil || ready || !strings.HasPrefix(reason, "STALE:") {
					t.Fatalf("ready=%v reason=%q err=%v", ready, reason, err)
				}
				if task.Workspace.Revisions[0].Merged {
					t.Fatal("unreviewed remote merge marked as completed")
				}
			})
		}
	}
}
func TestGatesReconcileExactMergedRevisionWithoutRecheckingOldBase(t *testing.T) {
	task := reviewedGateTask()
	mr := reviewedMR()
	mr.State = "merged"
	mr.HeadPipeline = nil
	client, _, requests := gateServer(t, func(int) gitlab.MergeRequest { return mr }, "advanced-after-merge")
	ready, reason, err := (&Activities{}).gates(context.Background(), client, &task)
	if err != nil || !ready || reason != "" || !task.Workspace.Revisions[0].Merged || *requests != 1 {
		t.Fatalf("ready=%v reason=%q err=%v merged=%v requests=%d", ready, reason, err, task.Workspace.Revisions[0].Merged, *requests)
	}
}
func TestGatesRequireCurrentSuccessfulMandatoryVerification(t *testing.T) {
	for _, scenario := range []string{"missing", "optional-only", "failed", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			task := reviewedGateTask()
			switch scenario {
			case "missing":
				task.Verification = nil
			case "optional-only":
				task.Verification[0].Required = false
			case "failed":
				task.Verification[0].Passed = false
			case "stale":
				task.Verification[0].RevisionDigest = "previous-digest"
			}
			client, _, requests := gateServer(t, func(int) gitlab.MergeRequest { return reviewedMR() }, "base-sha")
			ready, reason, err := (&Activities{}).gates(context.Background(), client, &task)
			if err != nil || ready || !strings.Contains(reason, "verification") || *requests != 0 {
				t.Fatalf("ready=%v reason=%q err=%v requests=%d", ready, reason, err, *requests)
			}
		})
	}
}

func TestGatesAllowOptedOutAcceptanceAfterIndependentReview(t *testing.T) {
	task := reviewedGateTask()
	task.Project.AcceptanceStrategy = "none"
	task.Project.RequireCI = false
	task.Verification = nil
	client, _, _ := gateServer(t, func(int) gitlab.MergeRequest { mr := reviewedMR(); mr.HeadPipeline = nil; return mr }, "base-sha")
	ready, reason, err := (&Activities{}).gates(context.Background(), client, &task)
	if err != nil || !ready || reason != "" {
		t.Fatalf("explicitly disabled acceptance blocked reviewed MR: ready=%v reason=%q err=%v", ready, reason, err)
	}
	task.ReviewDigest = "stale"
	ready, reason, err = (&Activities{}).gates(context.Background(), client, &task)
	if err != nil || ready || !strings.HasPrefix(reason, "STALE:") {
		t.Fatalf("disabled acceptance bypassed review digest: ready=%v reason=%q err=%v", ready, reason, err)
	}
}

func TestMergeActivityStopsBeforeAgentWhenTaskAutoMergeIsOff(t *testing.T) {
	for _, phase := range []string{domain.StageAcquiringMerge, domain.StageMerging} {
		t.Run(phase, func(t *testing.T) {
			task := reviewedGateTask()
			task.Stage = phase
			off := false
			task.AutoMergeOverride = &off
			result, err := (&Activities{}).Step(context.Background(), StepInput{Phase: phase, Task: task, ExecutionID: "merge"})
			if err != nil || result.Ready || result.Task.Stage != domain.StageWaitingManualMerge || result.Task.AutoMergeEnabled() {
				t.Fatalf("automatic merge continued after task switch: %+v err=%v", result, err)
			}
		})
	}
}
func TestMergeActivityReturnsReworkWhenTargetAdvanced(t *testing.T) {
	task := reviewedGateTask()
	_, url, _ := gateServer(t, func(int) gitlab.MergeRequest { return reviewedMR() }, "concurrent-target-sha")
	task.Project.GitLabURL = url
	activities := &Activities{Credential: func(string) (string, error) { return "fake-token", nil }}
	result, err := activities.Step(context.Background(), StepInput{Phase: domain.StageMerging, Task: task, ExecutionID: "execution"})
	if err != nil || result.Ready || !result.Rework || !strings.Contains(result.Task.Message, "target branch advanced") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestGatesOptionalCIStillRequiresReviewedHeadAndSource(t *testing.T) {
	for _, scenario := range []string{"exact-no-pipeline", "wrong-initial-head", "wrong-initial-source", "head-changes-during-check"} {
		t.Run(scenario, func(t *testing.T) {
			task := reviewedGateTask()
			task.Project.RequireCI = false
			client, _, _ := gateServer(t, func(call int) gitlab.MergeRequest {
				mr := reviewedMR()
				mr.HeadPipeline = nil
				switch scenario {
				case "wrong-initial-head":
					mr.SHA = "other"
				case "wrong-initial-source":
					mr.SourceBranch = "other"
				case "head-changes-during-check":
					if call > 1 {
						mr.SHA = "other"
					}
				}
				return mr
			}, "base-sha")
			ready, reason, err := (&Activities{}).gates(context.Background(), client, &task)
			if err != nil {
				t.Fatal(err)
			}
			if ready != (scenario == "exact-no-pipeline") {
				t.Fatalf("ready=%v reason=%q", ready, reason)
			}
		})
	}
}
func TestMergeActivityRejectsUnreviewedAlreadyMergedMR(t *testing.T) {
	task := reviewedGateTask()
	mr := reviewedMR()
	mr.State = "merged"
	mr.SourceBranch = "other-task"
	_, url, _ := gateServer(t, func(int) gitlab.MergeRequest { return mr }, "base-sha")
	task.Project.GitLabURL = url
	a := &Activities{Credential: func(string) (string, error) { return "fake-token", nil }}
	if _, err := a.Step(context.Background(), StepInput{Phase: domain.StagePartialMerge, Task: task, ExecutionID: "execution"}); err == nil {
		t.Fatal("unreviewed merged MR accepted")
	}
}

func TestGitEnvKeepsAskpassOnTheOwningHost(t *testing.T) {
	manager := workspace.New(t.TempDir(), "meta")
	manager.Env = map[string]string{"GIT_ASKPASS": "/native/scripts/askpass.sh"}
	a := Activities{Workspace: manager, Credential: func(string) (string, error) { return "test-token", nil }}
	local, err := a.gitEnv(domain.Task{ID: "t", Project: domain.Project{CredentialRef: "GITLAB_TOKEN"}})
	if err != nil || local["GIT_ASKPASS"] != "/native/scripts/askpass.sh" || local["GITLAB_TOKEN"] != "test-token" {
		t.Fatalf("local env=%v err=%v", local, err)
	}
	remote, err := a.gitEnv(domain.Task{ID: "t", NodeID: "node-a", Project: domain.Project{CredentialRef: "GITLAB_TOKEN"}})
	if err != nil || remote["GITLAB_TOKEN"] != "test-token" {
		t.Fatalf("remote env=%v err=%v", remote, err)
	}
	if _, ok := remote["GIT_ASKPASS"]; ok {
		t.Fatal("control plane must not send its askpass path to a remote node")
	}
}

func TestIntegrationEnvStaysControlPlaneLocal(t *testing.T) {
	a := Activities{IntegrationEnv: map[string]string{"INTEGRATION_TEST_TOKEN": "value"}}
	if got := a.integrationEnv(domain.Task{ID: "t"}); got["INTEGRATION_TEST_TOKEN"] != "value" {
		t.Fatalf("local integration env=%v", got)
	}
	if got := a.integrationEnv(domain.Task{ID: "t", NodeID: "node-a"}); got != nil {
		t.Fatalf("remote integration env=%v", got)
	}
}
