package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"testing"
)

func TestHumanAcceptanceSectionAndRevisionReset(t *testing.T) {
	task := domain.Task{HumanAcceptance: &domain.ManualAcceptance{Prerequisites: []string{"测试账号；启动本地服务"}, Steps: []domain.ManualStep{{Action: "登录后创建项目", Expected: "项目列表出现新项目"}}, Notes: []string{"完成后删除测试项目"}}}
	r := domain.Revision{HeadSHA: "abc"}
	body, err := manualDescription(task, r, "## Changes\nDetails")
	if err != nil || !strings.HasPrefix(body, manualStart) || !strings.Contains(body, "- [ ] **操作：**") || !strings.Contains(body, "**预期：**") {
		t.Fatalf("invalid manual section: %s %v", body, err)
	}
	checked := strings.Replace(body, "- [ ]", "- [x]", 1)
	retry, err := manualDescription(task, r, checked)
	if err != nil || retry != checked {
		t.Fatal("retry discarded human sign-off")
	}
	r.HeadSHA = "def"
	updated, err := manualDescription(task, r, checked)
	if err != nil || strings.Contains(updated, "- [x]") || strings.Count(updated, manualStart) != 1 || !strings.Contains(updated, "## Changes") {
		t.Fatal("new revision did not reset checklist while preserving description")
	}
	task.HumanAcceptance = nil
	if _, err = manualDescription(task, r, body); err == nil {
		t.Fatal("missing human instructions accepted")
	}
}
func TestStrategyCIRequiresCurrentCommit(t *testing.T) {
	for _, status := range []string{"success", "running", "failed", "skipped", "old-sha"} {
		t.Run(status, func(t *testing.T) {
			sha := "head"
			pipelineStatus := status
			if status == "old-sha" {
				sha = "old"
				pipelineStatus = "success"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(gitlab.MergeRequest{IID: 1, SHA: "head", SourceBranch: "bot/task", TargetBranch: "main", HeadPipeline: &gitlab.Pipeline{SHA: sha, Status: pipelineStatus}})
			}))
			defer srv.Close()
			task := domain.Task{Checkpoint: &domain.WorkflowCheckpoint{CIWaits: 4}, Project: domain.Project{AcceptanceStrategy: "gitlab_ci"}, Workspace: domain.Workspace{Revisions: []domain.Revision{{ProjectID: 1, MRIID: 1, HeadSHA: "head", Branch: "bot/task", TargetBranch: "main", Changed: true}}}}
			ready, waiting, err := (&Activities{}).strategyVerification(context.Background(), gitlab.New(srv.URL, "test"), nil, &task)
			if err != nil {
				t.Fatal(err)
			}
			switch status {
			case "success":
				if !ready || waiting || task.AcceptanceIncomplete {
					t.Fatal("success rejected")
				}
			case "running":
				if !waiting {
					t.Fatal("running CI not awaited")
				}
			case "failed":
				if ready || task.AcceptanceIncomplete {
					t.Fatal("failed CI must require rework")
				}
			default:
				if !task.AcceptanceIncomplete {
					t.Fatal("missing evidence accepted")
				}
			}
		})
	}
}
func TestNewStrategyPublishesBeforeVerification(t *testing.T) {
	h := newWorkflowHarness(t)
	task := workflowTask()
	task.Project.AcceptanceStrategy = "repository"
	task.Project.AutoMerge = autoMerge(true)
	h.step = func(_ context.Context, in StepInput, _ int) (StepResult, error) {
		out := successfulStep(in)
		if in.Phase == "verifying" {
			out.Task.AcceptanceIncomplete = true
		}
		return out, nil
	}
	final := h.run(t, task)
	if final.Stage != "completed" {
		t.Fatal(final.Stage)
	}
	pub, verify := -1, -1
	for i, p := range h.phases {
		if p == "publishing" && pub < 0 {
			pub = i
		}
		if p == "verifying" && verify < 0 {
			verify = i
		}
		if p == "merging" || p == "acquiring_merge" {
			t.Fatal("incomplete verification allowed automatic merge")
		}
	}
	if pub < 0 || verify < pub || h.counts["waiting_manual_merge"] == 0 {
		t.Fatalf("wrong strategy phases: %v", h.phases)
	}
}
