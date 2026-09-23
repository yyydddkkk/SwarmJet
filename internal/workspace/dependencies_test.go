package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"swarmjet/internal/domain"
	"testing"
)

func TestManifestDependencyAliasesPathsCapabilitiesAndCanonicalSets(t *testing.T) {
	data := []byte(`projects:
  database:
    repo: https://git.invalid/db.git
    path: libs/storage
    provides: [storage-api]
  frontend:
    repo: https://git.invalid/ui.git
    path: apps/ui
    depends_on: [storage-api, database, libs/storage]
`)
	repos := []domain.Repository{{ID: "db-id", Path: "libs/storage"}, {ID: "ui-id", Path: "apps/ui", DependsOn: []string{"db-id", "db-id"}}, {ID: "root", Path: ".", DependsOn: []string{"ui-id"}}}
	if e := validateManifestDependencies(data, repos); e != nil {
		t.Fatal(e)
	}
	repos[1].DependsOn = nil
	if e := validateManifestDependencies(data, repos); e == nil || !strings.Contains(e.Error(), "expected IDs [db-id]") {
		t.Fatalf("expected actionable ID mismatch: %v", e)
	}
}
func TestManifestDependencyMissingEnabledAndAmbiguousProviders(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, want string
		repos                []domain.Repository
	}{{"disabled required alias", "projects:\n  db: db.git\n  ui:\n    repo: ui.git\n    depends_on: [db]\n", "missing enabled dependencies: db", []domain.Repository{{ID: "ui-id", Path: "ui"}}}, {"disabled capability provider", "projects:\n  db:\n    repo: db.git\n    provides: [storage]\n  ui:\n    repo: ui.git\n    depends_on: [storage]\n", "missing enabled dependencies: db", []domain.Repository{{ID: "ui-id", Path: "ui"}}}, {"unknown", "projects:\n  ui:\n    repo: ui.git\n    depends_on: [absent]\n", "unresolved", []domain.Repository{{ID: "ui-id", Path: "ui"}}}, {"ambiguous", "projects:\n  a:\n    repo: a.git\n    provides: [storage]\n  b:\n    repo: b.git\n    provides: [storage]\n  ui:\n    repo: ui.git\n    depends_on: [storage]\n", "ambiguous meta providers", []domain.Repository{{ID: "a-id", Path: "a"}, {ID: "b-id", Path: "b"}, {ID: "ui-id", Path: "ui", DependsOn: []string{"a-id"}}}}} {
		t.Run(tc.name, func(t *testing.T) {
			e := validateManifestDependencies([]byte(tc.manifest), tc.repos)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("got %v, want %s", e, tc.want)
			}
		})
	}
}
func TestSimpleManifestEmptyDependenciesAndDisabledIndependentProjects(t *testing.T) {
	data := []byte(`{"projects":{"simple":"simple.git","unused":{"repo":"unused.git","depends_on":["missing"]}}}`)
	if e := validateManifestDependencies(data, []domain.Repository{{ID: "one", Path: "simple"}}); e != nil {
		t.Fatal(e)
	}
	if e := validateManifestDependencies([]byte("projects: {}\n"), []domain.Repository{{ID: "meta", Path: "."}}); e != nil {
		t.Fatal(e)
	}
}
func TestPreparePinsVerifiedDependencySnapshot(t *testing.T) {
	m, p := fixture(t)
	data := fmt.Sprintf("projects:\n  a:\n    repo: %s\n    provides: [backend-contract]\n  b:\n    repo: %s\n    path: libs/b\n    depends_on: [backend-contract]\n", p.Repositories[0].URL, p.Repositories[1].URL)
	os.WriteFile(filepath.Join(p.MetaURL, ".meta.yaml"), []byte(data), 0600)
	gitTest(t, m, p.MetaURL, "add", ".meta.yaml")
	gitTest(t, m, p.MetaURL, "commit", "-m", "manifest dependencies")
	ctx := context.Background()
	if _, e := m.Prepare(ctx, "bad-task", p); e == nil || !strings.Contains(e.Error(), "expected IDs [a]") {
		t.Fatalf("Prepare accepted stale dependency config: %v", e)
	}
	if _, e := os.Stat(m.statePath("bad-task")); !os.IsNotExist(e) {
		t.Fatal("invalid dependencies persisted a task snapshot")
	}
	p.Repositories[1].DependsOn = []string{"a"}
	ws, e := m.Prepare(ctx, "good-task", p)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := m.load(ws.TaskID)
	if e != nil || len(saved.Project.Repositories[1].DependsOn) != 1 || saved.Project.Repositories[1].DependsOn[0] != "a" {
		t.Fatalf("dependency snapshot not preserved: %v", e)
	}
	p.Repositories[1].DependsOn = nil
	if _, e = m.Prepare(ctx, "good-task", p); e != nil {
		t.Fatalf("restart ignored already verified snapshot: %v", e)
	}
}
