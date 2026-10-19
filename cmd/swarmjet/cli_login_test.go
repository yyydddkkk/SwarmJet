package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/runtimeadmin"
)

func writeOpenCodeAuth(t *testing.T, dataDir, body string) {
	t.Helper()
	path := filepath.Join(runtimeadmin.GetProfileDirs(dataDir).OpenCodeData, "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCellCLIEnvPriorityLoginThenEnvironmentThenNone(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"env":{"type":"api","key":"env-secret"}}`)

	login := `{"anthropic":{"type":"api","key":"login-secret"}}`
	writeOpenCodeAuth(t, dataDir, login)
	env, err := cellCLIEnv(context.Background(), domain.Hive{})
	if err != nil {
		t.Fatal(err)
	}
	if env["OPENCODE_AUTH_CONTENT"] != login {
		t.Fatalf("login state did not win: %q", env["OPENCODE_AUTH_CONTENT"])
	}

	if err = os.Remove(filepath.Join(runtimeadmin.GetProfileDirs(dataDir).OpenCodeData, "auth.json")); err != nil {
		t.Fatal(err)
	}
	env, err = cellCLIEnv(context.Background(), domain.Hive{})
	if err != nil {
		t.Fatal(err)
	}
	if env["OPENCODE_AUTH_CONTENT"] != `{"env":{"type":"api","key":"env-secret"}}` {
		t.Fatalf("environment fallback missing: %q", env["OPENCODE_AUTH_CONTENT"])
	}

	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	env, err = cellCLIEnv(context.Background(), domain.Hive{})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Fatalf("want no credential, got %v", env)
	}
}

func TestCellCLIEnvDoesNotLogCredentialOnCorruptLoginState(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	writeOpenCodeAuth(t, dataDir, `{"anthropic":{"key":"leaked-secret-value"`)

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	env, err := cellCLIEnv(context.Background(), domain.Hive{})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 0 {
		t.Fatalf("corrupt login state accepted: %v", env)
	}
	if strings.Contains(logs.String(), "leaked-secret-value") {
		t.Fatalf("credential leaked into logs: %s", logs.String())
	}
}

func TestNewCLILoginHandlerDisabledWithoutDocker(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/swarmjet.sock")
	handler, reason := newCLILoginHandler(t.TempDir())
	if handler != nil || reason == "" {
		t.Fatalf("handler=%v reason=%q", handler, reason)
	}
	if strings.Contains(reason, "OPENCODE_AUTH_CONTENT") {
		t.Fatalf("reason leaked unrelated detail: %s", reason)
	}
}

func TestCLILoginStatusReflectsHandlerAndLoginState(t *testing.T) {
	dataDir := t.TempDir()
	if available, loggedIn := cliLoginStatus(nil, dataDir)(); available || loggedIn {
		t.Fatalf("nil handler = %v/%v", available, loggedIn)
	}
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if available, loggedIn := cliLoginStatus(handler, dataDir)(); !available || loggedIn {
		t.Fatalf("logged out = %v/%v", available, loggedIn)
	}
	writeOpenCodeAuth(t, dataDir, `{"anthropic":{"type":"api","key":"login-secret"}}`)
	if available, loggedIn := cliLoginStatus(handler, dataDir)(); !available || !loggedIn {
		t.Fatalf("logged in = %v/%v", available, loggedIn)
	}
}
