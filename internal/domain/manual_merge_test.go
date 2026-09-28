package domain

import "testing"

func TestManualMergeEvidenceDigestBindsReviewAndSHAs(t *testing.T) {
	task := Task{ReviewDigest: "review", Workspace: Workspace{Revisions: []Revision{{RepositoryID: "repo", BaseSHA: "base", HeadSHA: "head", TargetBranch: "main"}}}, Executions: map[string]Execution{"review": {Result: &AgentResult{Summary: "looks good"}}}, HumanAcceptance: &ManualAcceptance{Steps: []ManualStep{{Action: "open", Expected: "works"}}}}
	first := ManualMergeEvidenceDigest(task)
	task.Workspace.Revisions[0].Merged = true
	if ManualMergeEvidenceDigest(task) != first {
		t.Fatal("partial merge invalidated acceptance")
	}
	task.Workspace.Revisions[0].HeadSHA = "new-head"
	if ManualMergeEvidenceDigest(task) == first {
		t.Fatal("changed commit retained acceptance")
	}
	task.Workspace.Revisions[0].HeadSHA = "head"
	review := task.Executions["review"]
	review.Result.Summary = "new review"
	task.Executions["review"] = review
	if ManualMergeEvidenceDigest(task) == first {
		t.Fatal("changed review retained acceptance")
	}
}
