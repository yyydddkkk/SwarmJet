package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/workspace"
)

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
	task := domain.Task{ID: "task", Stage: "merging", Project: domain.Project{ID: "project", RequireCI: true, Repositories: []domain.Repository{{ID: "api", ProjectID: 12, TargetBranch: "release"}}}, Workspace: domain.Workspace{Revisions: revisions}}
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
func TestMergeActivityReturnsReworkWhenTargetAdvanced(t *testing.T) {
	task := reviewedGateTask()
	_, url, _ := gateServer(t, func(int) gitlab.MergeRequest { return reviewedMR() }, "concurrent-target-sha")
	task.Project.GitLabURL = url
	activities := &Activities{Credential: func(string) (string, error) { return "fake-token", nil }}
	result, err := activities.Step(context.Background(), StepInput{Phase: "merging", Task: task, ExecutionID: "execution"})
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
	if _, err := a.Step(context.Background(), StepInput{Phase: "partial_merge", Task: task, ExecutionID: "execution"}); err == nil {
		t.Fatal("unreviewed merged MR accepted")
	}
}
