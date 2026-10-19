package runtimeadmin

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCodeAuthReadsOnlyValidLoginState(t *testing.T) {
	dataDir := t.TempDir()
	if LoggedIn(dataDir) {
		t.Fatal("empty profile reported as logged in")
	}
	path := filepath.Join(GetProfileDirs(dataDir).OpenCodeData, "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"anthropic":{"type":"oauth","access":"private-value"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := OpenCodeAuth(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if string(auth) != body || !LoggedIn(dataDir) {
		t.Fatalf("unexpected auth state %q", auth)
	}
	if err = os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenCodeAuth(dataDir); err == nil || LoggedIn(dataDir) {
		t.Fatal("malformed auth accepted")
	}
	if err = os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if LoggedIn(dataDir) {
		t.Fatal("empty auth object reported as logged in")
	}
}

func TestOpenCodeAuthRejectsSymlink(t *testing.T) {
	dataDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(outside, []byte(`{"anthropic":{"type":"api"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(GetProfileDirs(dataDir).OpenCodeData, "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if LoggedIn(dataDir) {
		t.Fatal("symlinked login state accepted")
	}
}

func TestInProcessHandlerDoesNotNeedRuntimeSecret(t *testing.T) {
	dataDir := t.TempDir()
	handler := NewInProcess(Config{DataDir: dataDir, Image: "runtime:test"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	// The standalone constructor keeps requiring the private header.
	w = httptest.NewRecorder()
	New(Config{DataDir: dataDir, Image: "runtime:test", Secret: "private"}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("standalone handler = %d", w.Code)
	}
}

func TestDockerAvailableHonorsUnixDockerHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/swarmjet.sock")
	if DockerAvailable() {
		t.Fatal("missing socket reported available")
	}
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("DOCKER_HOST", "unix://"+socket)
	if !DockerAvailable() {
		t.Fatal("socket not detected")
	}
}
