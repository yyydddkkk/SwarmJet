package initflow

import (
	"context"
	"os"
	"testing"
)

func TestCleanupVerifyContainersRemovesRecordedNames(t *testing.T) {
	f := newFixture(t)
	var calls [][]string
	f.m.Docker = func(_ context.Context, args []string) ([]byte, error) {
		calls = append(calls, append([]string{}, args...))
		return nil, nil
	}
	name := verifyContainerName("p1", 0)
	f.m.recordVerifyContainer("p1", name)
	if _, err := os.Stat(f.m.verifyRegistryPath("p1")); err != nil {
		t.Fatal("container registry was not written:", err)
	}
	f.m.cleanupVerifyContainers("p1")
	removed := false
	for _, call := range calls {
		if len(call) == 3 && call[0] == "rm" && call[1] == "-f" && call[2] == name {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("recorded container was not force-removed: %v", calls)
	}
	if _, err := os.Stat(f.m.verifyRegistryPath("p1")); !os.IsNotExist(err) {
		t.Fatal("container registry was not cleared")
	}
}

func TestDockerRunTracksNamedContainer(t *testing.T) {
	f := newFixture(t)
	f.m.VerifyImage = "verify:test"
	var runArgs []string
	f.m.Docker = func(_ context.Context, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			runArgs = append([]string{}, args...)
		}
		return []byte("ok"), nil
	}
	code, _, err := f.m.dockerRun(context.Background(), "p1", 0, t.TempDir(), []string{"true"})
	if err != nil || code != 0 {
		t.Fatalf("dockerRun failed: code=%d err=%v", code, err)
	}
	name := verifyContainerName("p1", 0)
	named := false
	for i, arg := range runArgs {
		if arg == "--name" && i+1 < len(runArgs) && runArgs[i+1] == name {
			named = true
		}
	}
	if !named {
		t.Fatalf("verification container was not named deterministically: %v", runArgs)
	}
	if _, err := os.Stat(f.m.verifyRegistryPath("p1")); !os.IsNotExist(err) {
		t.Fatal("container registry was not cleared after the run")
	}
}
