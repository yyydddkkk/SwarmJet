package store

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestWorkbenchIdentityAssociationAndManualMergeEvidence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	project, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGitLabIdentity(ctx, project.GitLabURL, "sealed-only", "administrator"); err != nil {
		t.Fatal(err)
	}
	secret, username, err := s.GitLabIdentity(ctx, project.GitLabURL+"/api/v4")
	if err != nil || secret != "sealed-only" || username != "administrator" {
		t.Fatalf("identity: %q %q %v", secret, username, err)
	}
	task := testTask("workbench-task", project, 12)
	if err = s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	task.Stage = "waiting_manual_merge"
	task.Project.AutoMerge = func() *bool { v := false; return &v }()
	task.Workspace.Revisions = []domain.Revision{{RepositoryID: "service", ProjectID: 1, MRIID: 7, BaseSHA: "base", HeadSHA: "head", TargetBranch: "main", Branch: "task-branch", Changed: true}}
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	task.HumanAcceptance = &domain.ManualAcceptance{Prerequisites: []string{"start service"}, Steps: []domain.ManualStep{{Action: "open page", Expected: "healthy"}}}
	if err = s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	linked, err := s.TaskForMR(ctx, project.GitLabURL, 1, 7)
	if err != nil || linked.ID != task.ID {
		t.Fatalf("MR association: %+v %v", linked, err)
	}
	if _, err = s.Pool.Exec(ctx, `DELETE FROM task_mrs WHERE task_id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE tasks SET body=jsonb_set(body,'{project,gitlabUrl}',to_jsonb($2::text)) WHERE id=$1`, task.ID, project.GitLabURL+"/api/v4"); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if linked, err = s.TaskForMR(ctx, project.GitLabURL, 1, 7); err != nil || linked.ID != task.ID {
		t.Fatalf("legacy MR backfill: %+v %v", linked, err)
	}
	if err = s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	issueTasks, err := s.TasksForIssue(ctx, project.GitLabURL, task.Issue)
	if err != nil || len(issueTasks) != 1 || issueTasks[0].ID != task.ID {
		t.Fatalf("Issue history: %+v %v", issueTasks, err)
	}
	if scoped, err := s.TasksForIssueInProject(ctx, "another-project", project.GitLabURL, task.Issue); err != nil || len(scoped) != 0 {
		t.Fatalf("cross-project Issue association leaked: %+v %v", scoped, err)
	}
	if _, err := s.TaskForMRInProject(ctx, "another-project", project.GitLabURL, 1, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project MR association leaked: %v", err)
	}
	record := ManualAcceptanceRecord{Digest: domain.ManualMergeEvidenceDigest(task), Checks: []ManualAcceptanceCheck{{Index: 0, Confirmed: true}, {Index: 1, Confirmed: true}}, Note: "verified by administrator"}
	if err = s.SaveManualAcceptance(ctx, task.ID, record); err != nil {
		t.Fatal(err)
	}
	task.Executions["review"] = domain.Execution{Result: &domain.AgentResult{Summary: "review changed"}}
	if err = s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err = s.EnqueueManualMerge(ctx, task.ID, domain.Command{ID: "stale-manual-merge", Kind: "merge_round"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed review accepted stale evidence: %v", err)
	}
	delete(task.Executions, "review")
	if err = s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	command := domain.Command{ID: "merge-workbench-task", Kind: "merge_round"}
	if err = s.EnqueueManualMerge(ctx, task.ID, command); err != nil {
		t.Fatal(err)
	}
	if err = s.EnqueueManualMerge(ctx, task.ID, command); err != nil {
		t.Fatal("retry must be idempotent:", err)
	}
	task.Stage = "manual_merging"
	if err = s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err = s.EnqueueManualMerge(ctx, task.ID, command); err != nil {
		t.Fatal("delayed command retry must be idempotent:", err)
	}
	if err = s.EnqueueManualMerge(ctx, task.ID, domain.Command{ID: "second-manual-merge", Kind: "merge_round"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate merge accepted: %v", err)
	}
	if err = s.SaveManualAcceptance(ctx, task.ID, record); !errors.Is(err, ErrConflict) {
		t.Fatalf("evidence mutated after command: %v", err)
	}
	on := true
	if err = s.EnqueueAutoMerge(ctx, task.ID, domain.Command{ID: "enable-after-manual", Kind: "set_auto_merge", AutoMerge: &on}); !errors.Is(err, ErrConflict) {
		t.Fatalf("auto merge raced manual merge: %v", err)
	}
}
