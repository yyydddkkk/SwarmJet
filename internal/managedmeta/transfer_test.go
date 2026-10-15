package managedmeta

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestBundleInstallRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	source := New(t.TempDir())
	node := New(t.TempDir())
	repos := []domain.Repository{{ID: "svc", URL: "https://gitlab.test/team/svc.git", Path: "svc", TargetBranch: "dev"}}
	files, err := Files("Demo", repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Ensure(ctx, "p1", "main", files); err != nil {
		t.Fatal(err)
	}
	bundle, err := source.Bundle(ctx, "p1")
	if err != nil || len(bundle) == 0 {
		t.Fatalf("bundle: %v %d", err, len(bundle))
	}
	if err = node.Install(ctx, "p1", "main", bundle); err != nil {
		t.Fatal(err)
	}
	body, err := node.ReadFile(ctx, "p1", "main", ".meta.yaml")
	if err != nil || !strings.Contains(string(body), "svc") {
		t.Fatalf("node cannot read the installed manifest: %v %s", err, body)
	}
	// Idempotent re-install.
	if err = node.Install(ctx, "p1", "main", bundle); err != nil {
		t.Fatal(err)
	}
	// A later control-plane commit must propagate on the next install.
	if _, err = source.Commit(ctx, "p1", "main", "update", map[string]string{"docs/new.md": "content"}); err != nil {
		t.Fatal(err)
	}
	bundle, err = source.Bundle(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if err = node.Install(ctx, "p1", "main", bundle); err != nil {
		t.Fatal(err)
	}
	if _, err = node.ReadFile(ctx, "p1", "main", "docs/new.md"); err != nil {
		t.Fatalf("update not installed: %v", err)
	}
	// Identity and size are validated.
	if err = node.Install(ctx, "p1", "bad..ref", bundle); err == nil {
		t.Fatal("invalid ref accepted")
	}
	if err = node.Install(ctx, "p1", "main", nil); err == nil {
		t.Fatal("empty bundle accepted")
	}
}

// Concurrent installs for the same project on one node must serialize instead
// of racing over the shared directory.
func TestConcurrentInstallIsSerialized(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	ctx := context.Background()
	source := New(t.TempDir())
	node := New(t.TempDir())
	repos := []domain.Repository{{ID: "svc", URL: "https://gitlab.test/team/svc.git", Path: "svc", TargetBranch: "dev"}}
	files, err := Files("Demo", repos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Ensure(ctx, "p1", "main", files); err != nil {
		t.Fatal(err)
	}
	bundle, err := source.Bundle(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 6
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- node.Install(ctx, "p1", "main", bundle)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent install failed: %v", err)
		}
	}
	if _, err = node.ReadFile(ctx, "p1", "main", ".meta.yaml"); err != nil {
		t.Fatalf("repository unusable after concurrent install: %v", err)
	}
}
