package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"swarmjet/internal/domain"
	"testing"
)

func fixture(t *testing.T) (*Manager, domain.Project) {
	t.Helper()
	m := New(t.TempDir(), "")
	m.Env = map[string]string{"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.invalid", "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.invalid"}
	base := t.TempDir()
	repo := func(name string) string {
		d := filepath.Join(base, name)
		os.MkdirAll(d, 0700)
		gitTest(t, m, d, "init", "-b", "main")
		os.WriteFile(filepath.Join(d, "README"), []byte(name), 0600)
		gitTest(t, m, d, "add", ".")
		gitTest(t, m, d, "commit", "-m", "initial")
		return d
	}
	a := repo("a")
	b := repo("b")
	gitTest(t, m, a, "checkout", "-b", "alpha")
	os.WriteFile(filepath.Join(a, "alpha"), []byte("a"), 0600)
	gitTest(t, m, a, "add", ".")
	gitTest(t, m, a, "commit", "-m", "alpha")
	gitTest(t, m, b, "checkout", "-b", "beta")
	os.WriteFile(filepath.Join(b, "beta"), []byte("b"), 0600)
	gitTest(t, m, b, "add", ".")
	gitTest(t, m, b, "commit", "-m", "beta")
	root := repo("meta")
	os.WriteFile(filepath.Join(root, ".meta.yaml"), []byte("projects:\n  a: "+a+"\n  b:\n    repo: "+b+"\n    path: libs/b\n"), 0600)
	gitTest(t, m, root, "add", ".")
	gitTest(t, m, root, "commit", "-m", "manifest")
	return m, domain.Project{ID: "project", MetaURL: root, MetaRef: "main", Repositories: []domain.Repository{{ID: "a", Path: "a", URL: a, TargetBranch: "alpha"}, {ID: "b", Path: "libs/b", URL: b, TargetBranch: "beta"}}}
}
func gitTest(t *testing.T, m *Manager, dir string, args ...string) string {
	t.Helper()
	s, e := m.git(context.Background(), dir, args...)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPrepareRestartInspect(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	for i, r := range p.Repositories {
		expected := gitTest(t, m, r.URL, "rev-parse", r.TargetBranch)
		if ws.Revisions[i].BaseSHA != expected {
			t.Fatalf("wrong base %s", r.ID)
		}
	}
	if _, e = m.Inspect(ctx, ws, p); e != nil {
		t.Fatal(e)
	}
	a := filepath.Join(ws.Root, "a")
	os.MkdirAll(filepath.Join(a, "migrations"), 0700)
	os.WriteFile(filepath.Join(a, "migrations", "001.sql"), []byte("select 1"), 0600)
	if _, e = m.Inspect(ctx, ws, p); e == nil {
		t.Fatal("dirty accepted")
	}
	gitTest(t, m, a, "add", ".")
	gitTest(t, m, a, "commit", "-m", "change")
	newHead := gitTest(t, m, a, "rev-parse", "HEAD")
	childPrimary := filepath.Join(m.taskSource(ws.TaskID), "libs/b")
	gitTest(t, m, childPrimary, "worktree", "remove", filepath.Join(ws.Root, "libs/b"))
	p.Repositories[0].TargetBranch = "nonexistent"
	resumed, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	if got := gitTest(t, m, a, "rev-parse", "HEAD"); got != newHead {
		t.Fatal("task was reset")
	}
	inspected, e := m.Inspect(ctx, resumed, p)
	if e != nil {
		t.Fatal(e)
	}
	if !inspected.Revisions[0].Changed || !inspected.Revisions[0].Sensitive {
		t.Fatal("missing change flags")
	}
	if inspected.Revisions[1].Changed {
		t.Fatal("unchanged child marked changed")
	}
}
func TestUnsafePaths(t *testing.T) {
	m, p := fixture(t)
	for _, id := range []string{"../escape", "a/b", "..", "-flag"} {
		if _, e := m.Prepare(context.Background(), id, p); e == nil {
			t.Fatalf("accepted %s", id)
		}
	}
	p.Repositories[0].Path = "../escape"
	if _, e := m.Prepare(context.Background(), "safe", p); e == nil {
		t.Fatal("accepted traversal")
	}
}
func TestManifestMismatch(t *testing.T) {
	m, p := fixture(t)
	p.Repositories[0].URL = p.Repositories[1].URL
	if _, e := m.Prepare(context.Background(), "task", p); e == nil {
		t.Fatal("accepted URL mismatch")
	}
}
func TestUnregisteredRootChange(t *testing.T) {
	m, p := fixture(t)
	ws, e := m.Prepare(context.Background(), "task", p)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(ws.Root, "README"), []byte("edit"), 0600)
	if _, e = m.Inspect(context.Background(), ws, p); e == nil {
		t.Fatal("accepted root edits")
	}
}
func TestReleasePreservesDirtyWorktree(t *testing.T) {
	m, p := fixture(t)
	ws, e := m.Prepare(context.Background(), "task", p)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(ws.Root, "a", "README")
	os.WriteFile(file, []byte("unsaved"), 0600)
	if e = m.Release(context.Background(), ws); e == nil {
		t.Fatal("released dirty task")
	}
	if b, e := os.ReadFile(file); e != nil || string(b) != "unsaved" {
		t.Fatal("lost dirty task changes")
	}
}
func TestMetaReceivesAliasAndRecoversFailure(t *testing.T) {
	m, p := fixture(t)
	script := filepath.Join(t.TempDir(), "meta")
	log := filepath.Join(t.TempDir(), "args")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$META_TEST_LOG\"\ngit worktree add .worktrees/task bot/task\nexit 1\n"), 0700)
	m.MetaCommand = script
	m.Env["META_TEST_LOG"] = log
	ws, e := m.Prepare(context.Background(), "task", p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Inspect(context.Background(), ws, p); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(log)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "b:bot/task") || strings.Contains(string(b), "libs/b:bot/task") {
		t.Fatalf("incorrect args %s", b)
	}
}
func TestInspectKeepsPublishedMetadataAndEmptyCommits(t *testing.T) {
	m, p := fixture(t)
	ws, e := m.Prepare(context.Background(), "task", p)
	if e != nil {
		t.Fatal(e)
	}
	gitTest(t, m, filepath.Join(ws.Root, "a"), "commit", "--allow-empty", "-m", "no tree change")
	ws, e = m.Inspect(context.Background(), ws, p)
	if e != nil {
		t.Fatal(e)
	}
	ws.Revisions[0].MRIID = 42
	ws.Revisions[0].MRURL = "https://git.invalid/mr/42"
	ws.Revisions[0].Merged = true
	got, e := m.Inspect(context.Background(), ws, p)
	if e != nil {
		t.Fatal(e)
	}
	r := got.Revisions[0]
	if r.Changed || r.MRIID != 42 || r.MRURL != ws.Revisions[0].MRURL || !r.Merged {
		t.Fatalf("incorrect inspection %#v", r)
	}
}
func TestRefreshTargetsIgnoresAdvancedUnchangedRepository(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	// This task changes b while a's target advances independently.
	taskB := filepath.Join(ws.Root, "libs/b")
	os.WriteFile(filepath.Join(taskB, "task-change"), []byte("task change"), 0600)
	gitTest(t, m, taskB, "add", ".")
	gitTest(t, m, taskB, "commit", "-m", "task change")
	ws, e = m.Inspect(ctx, ws, p)
	if e != nil || ws.Revisions[0].Changed || !ws.Revisions[1].Changed {
		t.Fatalf("unexpected starting revisions: %+v %v", ws.Revisions, e)
	}
	initial := ws.Revisions[0].BaseSHA
	remote := p.Repositories[0].URL
	os.WriteFile(filepath.Join(remote, "advanced"), []byte("target advanced"), 0600)
	gitTest(t, m, remote, "add", ".")
	gitTest(t, m, remote, "commit", "-m", "advance target")
	got, e := m.RefreshTargets(ctx, ws, p)
	if e != nil {
		t.Fatal(e)
	}
	if got.Revisions[0].BaseSHA != initial || got.Revisions[0].InitialSHA != initial || got.Revisions[0].Changed {
		t.Fatalf("unchanged repository baseline moved: %#v", got.Revisions[0])
	}
	got, e = m.Inspect(ctx, got, p)
	if e != nil {
		t.Fatal(e)
	}
	if got.Revisions[0].Changed || !got.Revisions[1].Changed {
		t.Fatal("unrelated target advance changed task scope")
	}
}
func TestRefreshTargetsRequiresSyncForChangedRepository(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	taskRepo := filepath.Join(ws.Root, "a")
	os.WriteFile(filepath.Join(taskRepo, "task-change"), []byte("task change"), 0600)
	gitTest(t, m, taskRepo, "add", ".")
	gitTest(t, m, taskRepo, "commit", "-m", "task change")
	ws, e = m.Inspect(ctx, ws, p)
	if e != nil || !ws.Revisions[0].Changed {
		t.Fatal("task change missing", e)
	}
	initial := ws.Revisions[0].BaseSHA
	remote := p.Repositories[0].URL
	os.WriteFile(filepath.Join(remote, "advanced"), []byte("target advanced"), 0600)
	gitTest(t, m, remote, "add", ".")
	gitTest(t, m, remote, "commit", "-m", "advance target")
	target := gitTest(t, m, remote, "rev-parse", "HEAD")
	if _, e = m.RefreshTargets(ctx, ws, p); !errors.Is(e, ErrNeedsSync) {
		t.Fatalf("changed repository accepted stale target: %v", e)
	}
	gitTest(t, m, taskRepo, "merge", "--no-edit", target)
	got, e := m.RefreshTargets(ctx, ws, p)
	if e != nil {
		t.Fatal(e)
	}
	if got.Revisions[0].BaseSHA != target || got.Revisions[0].InitialSHA != initial {
		t.Fatalf("wrong changed repository baseline %#v", got.Revisions[0])
	}
	got, e = m.Inspect(ctx, got, p)
	if e != nil || !got.Revisions[0].Changed {
		t.Fatal("task change lost after synchronization", e)
	}
}
func TestRefreshTargetsNormalizesUpstreamOnlyFastForward(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	initial := ws.Revisions[0].BaseSHA
	remote := p.Repositories[0].URL
	os.WriteFile(filepath.Join(remote, "advanced"), []byte("upstream only"), 0600)
	gitTest(t, m, remote, "add", ".")
	gitTest(t, m, remote, "commit", "-m", "advance target")
	target := gitTest(t, m, remote, "rev-parse", "HEAD")
	if _, e = m.RefreshTargets(ctx, ws, p); e != nil {
		t.Fatal(e)
	}
	gitTest(t, m, filepath.Join(ws.Root, "a"), "merge", "--ff-only", target)
	ws, e = m.Inspect(ctx, ws, p)
	if e != nil || !ws.Revisions[0].Changed {
		t.Fatal("upstream fast-forward did not appear as a change before normalization", e)
	}
	ws, e = m.RefreshTargets(ctx, ws, p)
	if e != nil {
		t.Fatal(e)
	}
	ws, e = m.Inspect(ctx, ws, p)
	if e != nil || ws.Revisions[0].Changed || ws.Revisions[0].BaseSHA != target || ws.Revisions[0].InitialSHA != initial {
		t.Fatalf("upstream-only commit still appears as task work: %+v %v", ws.Revisions[0], e)
	}
}
func TestReleaseRetainsUnmergedBranchAndIsIdempotent(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(ws.Root, "a")
	os.WriteFile(filepath.Join(dir, "unmerged"), []byte("preserve"), 0600)
	gitTest(t, m, dir, "add", ".")
	gitTest(t, m, dir, "commit", "-m", "unmerged")
	head := gitTest(t, m, dir, "rev-parse", "HEAD")
	for i := 0; i < 2; i++ {
		if e = m.Release(ctx, ws); e != nil {
			t.Fatal(e)
		}
	}
	primary := filepath.Join(m.taskSource(ws.TaskID), "a")
	if got := gitTest(t, m, primary, "rev-parse", "bot/task"); got != head {
		t.Fatal("unmerged branch lost")
	}
	if _, e = os.Stat(ws.Root); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("worktree remains: %v", e)
	}
}
func TestTaskSourcesAreIndependent(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	a, e := m.Prepare(ctx, "task-a", p)
	if e != nil {
		t.Fatal(e)
	}
	b, e := m.Prepare(ctx, "task-b", p)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(a.Root, m.taskSource("task-a")+"/") || !strings.HasPrefix(b.Root, m.taskSource("task-b")+"/") {
		t.Fatal("not private task roots")
	}
	gitTest(t, m, filepath.Join(a.Root, "a"), "config", "swarmjet.private", "task-a")
	other, e := os.ReadFile(filepath.Join(m.taskSource("task-b"), "a", ".git", "config"))
	if e != nil || strings.Contains(string(other), "private") {
		t.Fatal("shared git config")
	}
	sha := a.Revisions[0].BaseSHA
	obj := filepath.Join(".git", "objects", sha[:2], sha[2:])
	first, e := os.Stat(filepath.Join(m.taskSource("task-a"), "a", obj))
	if e != nil {
		t.Fatal(e)
	}
	for _, root := range []string{m.taskSource("task-b"), filepath.Join(m.DataDir, "projects", p.ID, "meta")} {
		other, e := os.Stat(filepath.Join(root, "a", obj))
		if e != nil {
			t.Fatal(e)
		}
		if os.SameFile(first, other) {
			t.Fatal("task git object hardlinked to another repository")
		}
	}
	for _, task := range []string{"task-a", "task-b"} {
		if _, e = os.Stat(filepath.Join(m.taskSource(task), "a", ".git", "objects", "info", "alternates")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("task has object alternates")
		}
	}
}
func TestHostGitIgnoresExecutableRepositoryConfiguration(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(ws.Root, "a")
	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(t.TempDir(), "monitor")
	os.WriteFile(script, []byte("#!/bin/sh\necho executed > "+marker+"\n"), 0700)
	gitTest(t, m, dir, "config", "core.fsmonitor", script)
	gitTest(t, m, dir, "config", "filter.inject.smudge", script)
	gitTest(t, m, dir, "config", "filter.inject.clean", script)
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("README filter=inject\n"), 0600)
	gitTest(t, m, dir, "add", ".gitattributes")
	gitTest(t, m, dir, "commit", "-m", "attributes")
	os.WriteFile(filepath.Join(dir, "README"), []byte("replace"), 0600)
	gitTest(t, m, dir, "checkout", "--", "README")
	if _, e = m.Inspect(ctx, ws, p); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(marker); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("host ran repository helper")
	}
	t.Setenv("SWARMJET_MERGE_SECRET", "must-not-leak")
	output, e := m.run(ctx, "", "sh", "-c", `printf '%s' "${SWARMJET_MERGE_SECRET-unset}"`)
	if e != nil || output != "unset" {
		t.Fatalf("worker environment leaked: %q %v", output, e)
	}
}
func TestOriginAndMetadataTamperingRejected(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(ws.Root, "a")
	gitTest(t, m, dir, "remote", "set-url", "origin", "ext::sh malicious")
	if _, e = m.RefreshTargets(ctx, ws, p); e == nil {
		t.Fatal("changed origin accepted")
	}
	gitTest(t, m, dir, "remote", "set-url", "origin", p.Repositories[0].URL)
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(m.DataDir, "projects", p.ID, "meta", "a", ".git")+"\n"), 0600)
	if _, e = m.Inspect(ctx, ws, p); e == nil {
		t.Fatal("external metadata accepted")
	}
}
func TestMergedRepositoryRemainsFrozenAcrossRework(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	ws.Revisions[0].Merged = true
	ws.Revisions[0].MRIID = 3
	initial := ws.Revisions[0].BaseSHA
	remote := p.Repositories[0].URL
	os.WriteFile(filepath.Join(remote, "upstream"), []byte("merged target moved"), 0600)
	gitTest(t, m, remote, "add", ".")
	gitTest(t, m, remote, "commit", "-m", "upstream")
	got, e := m.RefreshTargets(ctx, ws, p)
	if e != nil {
		t.Fatal(e)
	}
	if got.Revisions[0].BaseSHA != initial || !got.Revisions[0].Merged || got.Revisions[0].MRIID != 3 {
		t.Fatal("merged revision baseline or metadata changed")
	}
	gitTest(t, m, filepath.Join(ws.Root, "a"), "commit", "--allow-empty", "-m", "unauthorized merged repo edit")
	if _, e = m.Inspect(ctx, got, p); e == nil {
		t.Fatal("merged repository edit accepted")
	}
}
func TestHostFetchRejectsMetadataSymlink(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, e := m.Prepare(ctx, "task", p)
	if e != nil {
		t.Fatal(e)
	}
	victim := filepath.Join(t.TempDir(), "control.json")
	os.WriteFile(victim, []byte("keep"), 0600)
	fetchHead := filepath.Join(m.taskSource("task"), "a", ".git", "FETCH_HEAD")
	os.Remove(fetchHead)
	if e = os.Symlink(victim, fetchHead); e != nil {
		t.Fatal(e)
	}
	if _, e = m.RefreshTargets(ctx, ws, p); e == nil {
		t.Fatal("metadata symlink accepted")
	}
	b, e := os.ReadFile(victim)
	if e != nil || string(b) != "keep" {
		t.Fatal("host fetch overwrote external file")
	}
}
func TestUnsupportedMetaCommandNeverSilentlyFallsBack(t *testing.T) {
	m, p := fixture(t)
	script := filepath.Join(t.TempDir(), "meta")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'unsupported command' >&2\nexit 2\n"), 0700)
	m.MetaCommand = script
	for i := 0; i < 2; i++ {
		if _, e := m.Prepare(context.Background(), "task", p); e == nil {
			t.Fatal("invalid meta command silently accepted")
		}
		if _, e := os.Stat(filepath.Join(m.taskSource("task"), ".worktrees", "task", ".git")); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("native fallback created task despite unsupported CLI")
		}
	}
}

func TestNestedDeploymentAndCIFilesRequireApproval(t *testing.T) {
	for _, p := range []string{"compose.yaml", "integration/http-check/compose.yaml", "apps/api/Dockerfile.prod", "ci/check.sh", ".auto-devops.yml", "infra/main.tf", "backend/alembic/versions/001.py"} {
		if !sensitive(p, nil) {
			t.Fatalf("deployment/CI change not marked sensitive: %s", p)
		}
	}
	for _, p := range []string{"backend/service.py", "web/compose-message.ts", "package-lock.json", "tests/test_api.py"} {
		if sensitive(p, nil) {
			t.Fatalf("ordinary development gated: %s", p)
		}
	}
}
