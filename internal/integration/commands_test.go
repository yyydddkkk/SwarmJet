package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"swarmjet/internal/domain"
	"testing"
)

func TestCommandVerificationIsolationAndExitStatus(t *testing.T) {
	m, ws, _ := fixture(t)
	var seen []string
	m.Execute = func(_ context.Context, _ string, args, env []string) ([]byte, int, error) {
		if args[0] == "run" {
			seen = args
			return []byte("failed assertion"), 7, errors.New("exit 7")
		}
		return nil, 0, nil
	}
	cfg := domain.IntegrationConfig{Mode: "commands", Image: "toolchain:test", Commands: []domain.AcceptanceCommand{{Name: "unit", Path: ".", Argv: []string{"node", "-e", "console.log('a b')"}}}}
	got, err := m.Verify(context.Background(), ws, cfg)
	if err != nil || len(got) != 1 || got[0].Passed || got[0].ExitCode != 7 {
		t.Fatalf("lost failure: %+v %v", got, err)
	}
	args := strings.Join(seen, " ")
	if !strings.Contains(args, "--entrypoint node toolchain:test -e") || strings.Contains(args, "docker.sock") {
		t.Fatal(args)
	}
	if _, err = os.Stat(got[0].Report); err != nil {
		t.Fatal("report not recorded", err)
	}
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(ws.Root, "escape"))
	cfg.Commands[0].Path = "escape"
	if _, err = m.Verify(context.Background(), ws, cfg); err == nil {
		t.Fatal("accepted symlink escape")
	}
}
