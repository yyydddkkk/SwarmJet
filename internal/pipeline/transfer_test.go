package pipeline

import (
	"context"
	"testing"

	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/managedmeta"
)

type captureNode struct {
	execution.Node
	projectID string
	ref       string
	bundle    []byte
}

func (c *captureNode) SyncManagedMeta(_ context.Context, projectID, ref string, bundle []byte) error {
	c.projectID = projectID
	c.ref = ref
	c.bundle = bundle
	return nil
}

// The preparing activity must push the control-plane managed Meta-Repo to the
// task's execution node before its workspace is prepared.
func TestActivitiesSyncManagedMeta(t *testing.T) {
	dir := t.TempDir()
	project := domain.Project{
		ID: "p1", Name: "Demo", MetaMode: domain.MetaModeManaged, MetaRef: "main",
		Repositories: []domain.Repository{{ID: "svc", URL: "https://gitlab.test/team/svc.git", Path: "svc", TargetBranch: "dev"}},
	}
	files, err := managedmeta.Files("Demo", project.Repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = managedmeta.New(dir).Ensure(context.Background(), project.ID, project.MetaRef, files); err != nil {
		t.Fatal(err)
	}
	a := &Activities{DataDir: dir}
	node := &captureNode{}
	if err = a.syncManagedMeta(context.Background(), node, project); err != nil {
		t.Fatal(err)
	}
	if node.projectID != "p1" || node.ref != "main" || len(node.bundle) == 0 {
		t.Fatalf("managed Meta-Repo not transferred: %+v", node)
	}
}
