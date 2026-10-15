package api

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/managedmeta"
)

func TestCheckManagedMeta(t *testing.T) {
	dir := t.TempDir()
	project := domain.Project{
		ID: "managed1", MetaMode: domain.MetaModeManaged, MetaRef: "main", Name: "Demo",
		Repositories: []domain.Repository{{ID: "svc", URL: "https://gitlab.test/team/svc.git", Path: "svc"}},
		Integration:  domain.IntegrationConfig{ComposeFile: "integration/compose.yaml", Checks: []domain.Check{{Name: "unit", Service: "runner"}}},
	}
	m := managedmeta.New(dir)
	files, err := managedmeta.Files("Demo", project.Repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Ensure(context.Background(), project.ID, "main", files); err != nil {
		t.Fatal(err)
	}
	compose := `services:
  runner:
    image: alpine
`
	if _, err = m.Commit(context.Background(), project.ID, "main", "add compose", map[string]string{"integration/compose.yaml": compose}); err != nil {
		t.Fatal(err)
	}
	a := &server{cfg: Config{DataDir: dir}}
	if err = a.checkManagedMeta(project); err != nil {
		t.Fatal(err)
	}
	bad := project
	bad.Repositories = []domain.Repository{{ID: "svc", URL: "https://gitlab.test/team/other.git", Path: "svc"}}
	if a.checkManagedMeta(bad) == nil {
		t.Fatal("accepted a manifest URL mismatch")
	}
	missing := project
	missing.Integration.ComposeFile = "nope/compose.yaml"
	if a.checkManagedMeta(missing) == nil {
		t.Fatal("accepted a missing compose file")
	}
	unknown := project
	unknown.Integration.Checks = []domain.Check{{Name: "x", Service: "missing"}}
	if a.checkManagedMeta(unknown) == nil {
		t.Fatal("accepted a missing compose service")
	}
}
