package initflow

import (
	"context"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"testing"
)

func TestRemoteAnalysisPinsNodeAndDoesNotPrepareControlPlaneWorkspace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	in := domain.Initialization{ID: InitID(f.project.ID), ProjectID: f.project.ID, Status: domain.InitRunning, Stage: "analyze", Version: 1}
	f.repo.inits[in.ID] = in
	called := false
	f.m.RemoteAnalysis = func(_ context.Context, in *domain.Initialization, p domain.Project) (execution.AnalysisResult, error) {
		called = true
		in.NodeID = "remote"
		in.Image = "registry/agent@digest"
		return execution.AnalysisResult{Execution: domain.Execution{ID: in.ExecutionID, Status: "running", SessionID: "session"}}, nil
	}
	out, e := f.m.analyze(ctx, in, f.project)
	if e != nil {
		t.Fatal(e)
	}
	if !called || out.NodeID != "remote" || out.SessionID != "session" || len(f.clones) != 0 || len(f.agents.requests) != 0 {
		t.Fatalf("unexpected routing: %+v", out)
	}
	cancelled := false
	f.m.CancelAnalysis = func(_ context.Context, in domain.Initialization) error { cancelled = in.NodeID == "remote"; return nil }
	if e = f.m.Cancel(ctx, f.project.ID); e != nil {
		t.Fatal(e)
	}
	if !cancelled {
		t.Fatal("cancel did not reach remote node")
	}
}
func TestRemoteGeneratedFilesRejectPathEscape(t *testing.T) {
	for _, name := range []string{"../x", ".git/config", "/tmp/x", "foo/../x", "a\\b"} {
		if e := validateGeneratedFiles(map[string]string{name: "text"}); e == nil {
			t.Fatal(name)
		}
	}
	if e := validateGeneratedFiles(map[string]string{"PROJECT.md": "# project"}); e != nil {
		t.Fatal(e)
	}
}
