package initflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestCleanupKeepsRecordWhenRemovalFails(t *testing.T) {
	f := newFixture(t)
	f.m.Docker = func(context.Context, []string) ([]byte, error) {
		return nil, errors.New("docker daemon unavailable")
	}
	name := verifyContainerName("p1", 0)
	if err := f.m.recordVerifyContainer("p1", name); err != nil {
		t.Fatal(err)
	}
	f.m.cleanupVerifyContainers("p1")
	names := readVerifyRegistry(f.m.verifyRegistryPath("p1"))
	if len(names) != 1 || names[0] != name {
		t.Fatalf("record was dropped after a failed cleanup: %v", names)
	}
}

func TestDockerRunKeepsRecordWhenCleanupFails(t *testing.T) {
	f := newFixture(t)
	f.m.VerifyImage = "verify:test"
	f.m.Docker = func(_ context.Context, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			return []byte("ok"), nil
		}
		return nil, errors.New("docker daemon unavailable")
	}
	dir := t.TempDir()
	if code, _, err := f.m.dockerRun(context.Background(), "p1", 0, dir, []string{"true"}); err != nil || code != 0 {
		t.Fatalf("dockerRun failed: code=%d err=%v", code, err)
	}
	names := readVerifyRegistry(f.m.verifyRegistryPath("p1"))
	if len(names) != 1 || names[0] != verifyContainerName("p1", 0) {
		t.Fatalf("record was dropped although the container may still run: %v", names)
	}
}

func TestDockerRunAbortsWhenRegistrationFails(t *testing.T) {
	f := newFixture(t)
	f.m.VerifyImage = "verify:test"
	var ran bool
	f.m.Docker = func(_ context.Context, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "run" {
			ran = true
		}
		return nil, nil
	}
	// Make the registry directory uncreatable by placing a regular file there.
	dir := f.m.initDir("p1")
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.m.dockerRun(context.Background(), "p1", 0, t.TempDir(), []string{"true"}); err == nil {
		t.Fatal("dockerRun started a container without a durable record")
	}
	if ran {
		t.Fatal("verification container was started even though registration failed")
	}
}
