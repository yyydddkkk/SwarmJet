package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishInspectedCommitsRetryAndRejectChangedHead(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, err := m.Prepare(ctx, "publish", p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ws.Root, "a")
	if err := os.WriteFile(filepath.Join(dir, "change"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, m, dir, "add", ".")
	gitTest(t, m, dir, "commit", "-m", "change")
	ws, err = m.Inspect(ctx, ws, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := m.Push(ctx, ws, p); err != nil {
			t.Fatal(err)
		}
	}
	if head := gitTest(t, m, p.Repositories[0].URL, "rev-parse", "refs/heads/"+ws.Revisions[0].Branch); head != ws.Revisions[0].HeadSHA {
		t.Fatal("wrong published commit")
	}
	if _, err := m.git(ctx, p.Repositories[1].URL, "rev-parse", "--verify", "refs/heads/"+ws.Revisions[1].Branch); err == nil {
		t.Fatal("unchanged repository published")
	}
	gitTest(t, m, dir, "commit", "--allow-empty", "-m", "later")
	if err := m.Push(ctx, ws, p); err == nil || !strings.Contains(err.Error(), "changed since inspection") {
		t.Fatalf("stale inspection accepted: %v", err)
	}
}

func TestPublishRejectsPushRewriteAndNeverForcesRemote(t *testing.T) {
	m, p := fixture(t)
	ctx := context.Background()
	ws, err := m.Prepare(ctx, "publish", p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ws.Root, "a")
	os.WriteFile(filepath.Join(dir, "change"), []byte("one"), 0600)
	gitTest(t, m, dir, "add", ".")
	gitTest(t, m, dir, "commit", "-m", "change")
	ws, err = m.Inspect(ctx, ws, p)
	if err != nil {
		t.Fatal(err)
	}
	key := "url." + t.TempDir() + ".pushInsteadOf"
	gitTest(t, m, dir, "config", key, p.Repositories[0].URL)
	if err := m.Push(ctx, ws, p); err == nil || !strings.Contains(err.Error(), "rewrites forbidden") {
		t.Fatalf("rewrite accepted: %v", err)
	}
	gitTest(t, m, dir, "config", "--unset", key)
	remote := p.Repositories[0].URL
	gitTest(t, m, remote, "checkout", "-b", ws.Revisions[0].Branch)
	gitTest(t, m, remote, "commit", "--allow-empty", "-m", "remote concurrent edit")
	before := gitTest(t, m, remote, "rev-parse", "HEAD")
	gitTest(t, m, remote, "checkout", p.Repositories[0].TargetBranch)
	if err := m.Push(ctx, ws, p); err == nil {
		t.Fatal("divergent remote overwritten")
	}
	if after := gitTest(t, m, remote, "rev-parse", ws.Revisions[0].Branch); after != before {
		t.Fatal("remote commit changed")
	}
}
