package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run with SWARMJET_META_BIN=/absolute/path/meta and its sibling plugins on PATH.
// All Git remotes and mutations are temporary local repositories.
func TestRealMetaPrepareInspectResumeRelease(t *testing.T) {
	binary := os.Getenv("SWARMJET_META_BIN")
	if binary == "" {
		t.Skip("set SWARMJET_META_BIN to test installed meta CLI")
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(absolute); err != nil || info.IsDir() {
		t.Fatalf("meta executable unavailable: %v", err)
	}
	m, project := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// The manager deliberately recovers partial CLI failures with Git. Record the
	// real CLI exit separately so this integration test cannot pass on fallback alone.
	wrapperDir := t.TempDir()
	wrapper := filepath.Join(wrapperDir, "meta-record")
	script := "#!/bin/sh\n\"$SWARMJET_TEST_META\" \"$@\" >\"$SWARMJET_TEST_META_OUTPUT\" 2>&1\nstatus=$?\nprintf '%s\\n' \"$status\" >>\"$SWARMJET_TEST_META_STATUS\"\nexit \"$status\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(wrapperDir, "output")
	status := filepath.Join(wrapperDir, "status")
	m.MetaCommand = wrapper
	m.Env["SWARMJET_TEST_META"] = absolute
	m.Env["SWARMJET_TEST_META_OUTPUT"] = output
	m.Env["SWARMJET_TEST_META_STATUS"] = status
	workspace, err := m.Prepare(ctx, "real-meta", project)
	if err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(status)
	if err != nil {
		t.Fatalf("real meta was not invoked: %v", err)
	}
	diagnostic, _ := os.ReadFile(output)
	if strings.TrimSpace(string(result)) != "0" {
		t.Fatalf("real meta failed (manager recovered via Git): exit=%s output=%s", result, diagnostic)
	}
	t.Logf("real meta output: %s", diagnostic)
	for i, repo := range project.Repositories {
		want := gitTest(t, m, repo.URL, "rev-parse", repo.TargetBranch)
		if workspace.Revisions[i].BaseSHA != want || workspace.Revisions[i].HeadSHA != want {
			t.Fatalf("repository %s wrong independent baseline: %+v want=%s", repo.ID, workspace.Revisions[i], want)
		}
		if got := gitTest(t, m, filepath.Join(workspace.Root, repo.Path), "rev-parse", "HEAD"); got != want {
			t.Fatalf("repository %s wrong worktree HEAD: %s want %s", repo.ID, got, want)
		}
	}
	workspace, err = m.Inspect(ctx, workspace, project)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(workspace.Root, "a")
	if err = os.WriteFile(filepath.Join(child, "task-change"), []byte("preserve across restart\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, m, child, "add", "task-change")
	gitTest(t, m, child, "commit", "-m", "task change")
	taskHead := gitTest(t, m, child, "rev-parse", "HEAD")
	restarted := New(m.DataDir, wrapper)
	restarted.Env = m.Env
	resumed, err := restarted.Prepare(ctx, "real-meta", project)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Root != workspace.Root || gitTest(t, restarted, child, "rev-parse", "HEAD") != taskHead {
		t.Fatal("resume reset or replaced task workspace")
	}
	result, err = os.ReadFile(status)
	if err != nil || strings.TrimSpace(string(result)) != "0" {
		t.Fatal("resume unexpectedly reinvoked meta creation")
	}
	inspected, err := restarted.Inspect(ctx, resumed, project)
	if err != nil {
		t.Fatal(err)
	}
	if !inspected.Revisions[0].Changed || inspected.Revisions[1].Changed {
		t.Fatalf("wrong change detection: %+v", inspected.Revisions)
	}
	if err = restarted.Release(ctx, inspected); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Release(ctx, inspected); err != nil {
		t.Fatalf("release not idempotent: %v", err)
	}
	if _, err = os.Stat(workspace.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released worktree remains: %v", err)
	}
	if got := gitTest(t, restarted, filepath.Join(restarted.taskSource("real-meta"), "a"), "rev-parse", "bot/real-meta"); got != taskHead {
		t.Fatal("release discarded unmerged task commits")
	}
	if _, err = restarted.Prepare(ctx, "real-meta", project); err == nil {
		t.Fatal("released workspace silently recreated")
	}
}
