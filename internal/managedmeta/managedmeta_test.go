package managedmeta

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"swarmjet/internal/domain"
)

func sampleRepos() []domain.Repository {
	return []domain.Repository{
		{ID: "web", URL: "https://gitlab.example/team/web.git", Path: "apps/web", TargetBranch: "dev", DependsOn: []string{"core"}},
		{ID: "core", URL: "https://gitlab.example/team/core.git", Path: "libs/core", TargetBranch: "dev"},
	}
}

func TestManifestDeterministicAndOrdered(t *testing.T) {
	first, err := Manifest(sampleRepos())
	if err != nil {
		t.Fatal(err)
	}
	reversed := sampleRepos()
	reversed[0], reversed[1] = reversed[1], reversed[0]
	second, err := Manifest(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("manifest is not deterministic: %s / %s", first, second)
	}
	if strings.Index(string(first), "core:") > strings.Index(string(first), "web:") {
		t.Fatalf("projects are not sorted: %s", first)
	}
	if !strings.Contains(string(first), "depends_on:") {
		t.Fatalf("dependency missing: %s", first)
	}
}

func TestManifestRejectsIncompleteRepository(t *testing.T) {
	for _, r := range []domain.Repository{
		{ID: "ok", URL: "", Path: "x"},
		{ID: "ok", URL: "https://git/x.git", Path: ""},
		{ID: "bad id", URL: "https://git/x.git", Path: "x"},
	} {
		if _, err := Manifest([]domain.Repository{r}); err == nil {
			t.Fatalf("accepted incomplete repository %+v", r)
		}
	}
}

func TestWriteRejectsUnsafeNames(t *testing.T) {
	m := New(t.TempDir())
	dir := t.TempDir()
	backslash := "a" + string(rune(92)) + "b"
	for _, name := range []string{"../escape", "/abs", ".git/config", "a/../b", backslash, "a:b", "a/./b"} {
		if err := m.write(dir, map[string]string{name: "x"}); err == nil {
			t.Fatalf("accepted unsafe file name %q", name)
		}
	}
	if err := m.write(dir, map[string]string{"docs/readme.md": "ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestRootRejectsUnsafeProject(t *testing.T) {
	m := New(t.TempDir())
	for _, id := range []string{"../x", "", "a/b", "-x"} {
		if _, err := m.Root(id); err == nil {
			t.Fatalf("accepted unsafe project id %q", id)
		}
	}
}

func TestEnsureCommitIdempotentAndPreservesManual(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	m := New(t.TempDir())
	files, err := Files("Demo", sampleRepos())
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Ensure(ctx, "demo", "main", files)
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.Ensure(ctx, "demo", "main", files)
	if err != nil || again != first {
		t.Fatalf("Ensure not idempotent: %v %q %q", err, first, again)
	}
	same, err := m.Commit(ctx, "demo", "main", "regenerate", files)
	if err != nil || same != first {
		t.Fatalf("no-op commit changed history: %v %q %q", err, first, same)
	}
	if _, err = m.ReadFile(ctx, "demo", "main", ".meta.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.ReadFile(ctx, "demo", "main", "../escape"); err == nil {
		t.Fatal("read escaped root")
	}
	changed := map[string]string{}
	for k, v := range files {
		changed[k] = v
	}
	nl := string(rune(10))
	changed["README.md"] += nl + "manual revision" + nl
	manual, err := m.Commit(ctx, "demo", "main", "manual revision", changed)
	if err != nil || manual == first {
		t.Fatalf("manual commit not created: %v %q", err, manual)
	}
	// Re-running initialization must not overwrite the manual revision.
	preserved, err := m.Ensure(ctx, "demo", "main", files)
	if err != nil || preserved != manual {
		t.Fatalf("Ensure reset manual revision: %v %q %q", err, preserved, manual)
	}
	body, err := m.ReadFile(ctx, "demo", "main", "README.md")
	if err != nil || !strings.Contains(string(body), "manual revision") {
		t.Fatalf("manual revision lost: %v %s", err, body)
	}
}
