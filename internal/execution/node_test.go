package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestReportPathConfinement(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs", "task")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "log.txt"), []byte("log"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"../secret", "/etc/passwd", "escape", "", "."} {
		if _, err := reportPath(dir, "task", relative); err == nil {
			t.Fatalf("accepted %q", relative)
		}
	}
	if p, err := reportPath(dir, "task", "log.txt"); err != nil || p == "" {
		t.Fatalf("valid report rejected: %v", err)
	}
}

func TestCleanupMarkerIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runs", "task"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "task", "cleanup-complete"), []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	n := &LocalNode{DataDir: dir}
	done, err := n.Cleanup(context.Background(), "task", domain.Workspace{}, domain.IntegrationConfig{}, nil)
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
}
