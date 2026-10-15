package repochange

import (
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestAddManifestEntryPreservesOtherContentAndUsesManifestAliases(t *testing.T) {
	body := []byte("# team note\nversion: 1\nprojects:\n  backend:\n    repo: https://git.example/team/backend.git\n    path: apps/backend\nother: keep\n")
	registered := []domain.Repository{{ID: "api", Path: "apps/backend"}}
	repo := domain.Repository{ID: "frontend", Path: "apps/frontend", URL: "https://git.example/team/frontend.git", DependsOn: []string{"api"}}
	updated, changed, err := addManifestEntry(body, repo, registered)
	if err != nil || !changed {
		t.Fatalf("add manifest: changed=%v err=%v", changed, err)
	}
	for _, wanted := range []string{"# team note", "other: keep", "frontend:", "path: apps/frontend", "depends_on: [backend]"} {
		if !strings.Contains(string(updated), wanted) {
			t.Fatalf("updated manifest missing %q:\n%s", wanted, updated)
		}
	}
	if again, changed, err := addManifestEntry(updated, repo, registered); err != nil || changed || string(again) != string(updated) {
		t.Fatalf("second add should be idempotent: changed=%v err=%v", changed, err)
	}
}

func TestAddManifestEntryRejectsConflictingPath(t *testing.T) {
	body := []byte("projects:\n  backend:\n    repo: https://git.example/team/backend.git\n    path: apps/backend\n")
	repo := domain.Repository{ID: "other", Path: "apps/backend", URL: "https://git.example/team/other.git"}
	if _, _, err := addManifestEntry(body, repo, nil); err == nil {
		t.Fatal("accepted an occupied Meta path")
	}
}

func TestAlreadyDeclaredRepositoryAcceptsResolvedDependency(t *testing.T) {
	body := []byte("projects:\n  backend:\n    repo: https://git.example/team/backend.git\n    path: apps/backend\n    provides: [storage]\n  frontend:\n    repo: https://git.example/team/frontend.git\n    path: apps/frontend\n    depends_on: [storage]\n")
	registered := []domain.Repository{{ID: "api", Path: "apps/backend"}}
	repo := domain.Repository{ID: "frontend", Path: "apps/frontend", URL: "https://git.example/team/frontend.git", DependsOn: []string{"api"}}
	if _, changed, err := addManifestEntry(body, repo, registered); err != nil || changed {
		t.Fatalf("already declared repository should not need a Meta edit: changed=%v err=%v", changed, err)
	}
}
