package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
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
	if !strings.Contains(args, "--entrypoint node toolchain:test -e") || !strings.Contains(args, "--tmpfs /tmp:exec,mode=1777,size=256m") || strings.Contains(args, "docker.sock") {
		t.Fatal(args)
	}
	if !strings.Contains(args, "src="+ws.Root+",dst="+ws.Root) || !strings.Contains(args, "--workdir "+ws.Root) || strings.Contains(args, "dst=/workspace") {
		t.Fatal("acceptance moved the prepared workspace and broke absolute pnpm paths:", args)
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

func TestCommandVerificationDistinguishesToolchainFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		log    string
		code   int
		broken bool
	}{
		{"missing-eslint-parser", "Error: Failed to load parser 'vue-eslint-parser': Cannot find module 'vue-eslint-parser'", 2, true},
		{"docker-start", "docker: Error response from daemon", 125, true},
		{"lint-rule", "src/view.vue 12:1 error no-unused-vars", 1, false},
		{"test-assertion", "AssertionError: expected 1 to be 2", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ws, _ := fixture(t)
			m.Execute = func(_ context.Context, _ string, args, _ []string) ([]byte, int, error) {
				if args[0] == "run" {
					return []byte(tc.log), tc.code, errors.New("command failed")
				}
				return nil, 0, nil
			}
			cfg := domain.IntegrationConfig{Mode: "commands", Image: "toolchain:test", Commands: []domain.AcceptanceCommand{{Name: "check", Path: ".", Argv: []string{"pnpm", "run", "lint:check"}}}}
			got, err := m.Verify(context.Background(), ws, cfg)
			if err != nil || len(got) != 1 || got[0].EnvironmentFailure != tc.broken {
				t.Fatalf("environment classification: %+v err=%v", got, err)
			}
		})
	}
}
