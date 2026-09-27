package pipeline

import (
	"context"
	"testing"

	"swarmjet/internal/gitlab"
)

func TestManualMergeEvidenceKeepsIncompleteAcceptanceForHumanDecision(t *testing.T) {
	task := reviewedGateTask()
	task.AcceptanceIncomplete = true
	task.Verification = nil
	mr := reviewedMR()
	mr.State = "merged"
	client, _, _ := gateServer(t, func(int) gitlab.MergeRequest { return mr }, "advanced-after-merge")
	evidence := mergeEvidence{client: client, task: &task}
	if !evidence.reviewCurrent() {
		t.Fatal("reviewed revision set was rejected")
	}
	if current, missing := evidence.manualVerificationCurrent(); !current || missing {
		t.Fatalf("incomplete acceptance should remain for human decision: current=%v missing=%v", current, missing)
	}
	merged, err := evidence.reconcileMergedRevisions(context.Background())
	if err != nil || !merged || !task.Workspace.Revisions[0].Merged {
		t.Fatalf("exact merged MR not reconciled: merged=%v err=%v", merged, err)
	}
	if ready, _, err := evidence.automaticGate(context.Background()); err != nil || ready {
		t.Fatalf("incomplete acceptance allowed automatic merge: ready=%v err=%v", ready, err)
	}
}

func TestManualMergeEvidenceRequiresVerificationWhenNotIncomplete(t *testing.T) {
	task := reviewedGateTask()
	evidence := mergeEvidence{task: &task}
	task.Verification = nil
	if current, missing := evidence.manualVerificationCurrent(); !current || !missing {
		t.Fatalf("missing verification: current=%v missing=%v", current, missing)
	}
	task.Verification = reviewedGateTask().Verification
	task.Verification[0].RevisionDigest = "old-review"
	if current, missing := evidence.manualVerificationCurrent(); current || missing {
		t.Fatalf("stale verification: current=%v missing=%v", current, missing)
	}
}
