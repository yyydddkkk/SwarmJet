package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
	"testing"
)

type commandAcceptanceNode struct {
	execution.Node
	checks []domain.Verification
}

func (n commandAcceptanceNode) StartIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) error {
	return nil
}
func (n commandAcceptanceNode) VerifyIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) ([]domain.Verification, error) {
	return n.checks, nil
}
func (n commandAcceptanceNode) StopIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) error {
	return nil
}

func TestRepositoryAcceptanceKeepsEnvironmentFailureOutOfCodeRework(t *testing.T) {
	for _, broken := range []bool{false, true} {
		task := domain.Task{Project: domain.Project{AcceptanceStrategy: "repository", Development: domain.AgentConfig{Image: "toolchain:test"}, Repositories: []domain.Repository{{Path: "repos/app"}}}, Workspace: domain.Workspace{Revisions: []domain.Revision{{RepositoryID: "app", HeadSHA: "head", Changed: true}}}, AcceptanceCommands: []domain.AcceptanceCommand{{Name: "lint", Path: "repos/app", Argv: []string{"pnpm", "lint"}, Evidence: "package.json"}}}
		check := domain.Verification{Name: "lint", Required: true, ExitCode: 2, EnvironmentFailure: broken}
		ready, waiting, err := (&Activities{}).strategyVerification(context.Background(), nil, commandAcceptanceNode{checks: []domain.Verification{check}}, &task)
		if err != nil || waiting || ready != broken || task.AcceptanceIncomplete != broken {
			t.Fatalf("broken=%v ready=%v waiting=%v incomplete=%v err=%v", broken, ready, waiting, task.AcceptanceIncomplete, err)
		}
		if broken && (len(task.Verification) != 2 || !strings.Contains(task.Message, "验收环境")) {
			t.Fatalf("missing diagnostic verification: %+v %q", task.Verification, task.Message)
		}
	}
}

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
func TestIssueReviewGuideIsInformationalAndRevisionBound(t *testing.T) {
	task := domain.Task{ID: "run-2", Round: 2, HumanAcceptance: &domain.ManualAcceptance{Prerequisites: []string{"测试环境"}, Steps: []domain.ManualStep{{Action: "创建项目", Expected: "项目出现在列表"}}}, Workspace: domain.Workspace{Revisions: []domain.Revision{{RepositoryID: "api", MRURL: "https://gitlab.test/mr/1", HeadSHA: "abc", TargetBranch: "main", Changed: true}}}}
	body, err := issueReviewGuide(task)
	if err != nil || !strings.Contains(body, "第 2 轮") || !strings.Contains(body, "https://gitlab.test/mr/1") || !strings.Contains(body, "abc") || !strings.Contains(body, "/swarmjet run") || strings.Contains(body, "- [ ]") {
		t.Fatalf("invalid guide: %s %v", body, err)
	}
}
func TestPublishIssueReviewGuideLinksMR(t *testing.T) {
	var noteBody, mrBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/2/notes") && r.Method == "GET":
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/issues/2/notes") && r.Method == "POST":
			var input struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			noteBody = input.Body
			_, _ = w.Write([]byte(`{"id":10}`))
		case strings.HasSuffix(r.URL.Path, "/merge_requests/3") && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(gitlab.MergeRequest{IID: 3, Description: "MR summary", SHA: "head", SourceBranch: "branch", TargetBranch: "main"})
		case strings.HasSuffix(r.URL.Path, "/merge_requests/3") && r.Method == "PUT":
			var input struct {
				Description string `json:"description"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			mrBody = input.Description
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	task := domain.Task{ID: "round", Round: 1, Issue: domain.Issue{ProjectID: 1, IID: 2, URL: server.URL + "/issues/2"}, HumanAcceptance: &domain.ManualAcceptance{Prerequisites: []string{"test env"}, Steps: []domain.ManualStep{{Action: "create", Expected: "visible"}}}, Workspace: domain.Workspace{Revisions: []domain.Revision{{RepositoryID: "api", ProjectID: 1, MRIID: 3, MRURL: server.URL + "/mr/3", HeadSHA: "head", Branch: "branch", TargetBranch: "main", Changed: true}}}}
	if err := publishIssueReviewGuide(context.Background(), gitlab.New(server.URL, "token"), task); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(noteBody, "人工复核指引") || !strings.Contains(mrBody, "#note_10") || !strings.Contains(mrBody, "MR summary") || strings.Contains(mrBody, "- [ ]") {
		t.Fatalf("note=%q MR=%q", noteBody, mrBody)
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
