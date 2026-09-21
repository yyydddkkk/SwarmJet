package runtimeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeDocker struct {
	mu                       sync.Mutex
	name, identity, image    string
	exists, running, unowned bool
	calls                    [][]string
	envs                     [][]string
	catalog                  string
	stopped                  chan struct{}
	stopOnce                 sync.Once
}

func (f *fakeDocker) run(ctx context.Context, args, env []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{}, args...))
	f.envs = append(f.envs, append([]string{}, env...))
	if args[0] == "image" {
		return []byte("sha256:test"), nil
	}
	if args[0] == "exec" {
		if args[len(args)-1] == "models" {
			return []byte(f.catalog), nil
		}
		return []byte("opencode\ncodex\n"), nil
	}
	if args[0] != "container" {
		return nil, errors.New("unexpected Docker command")
	}
	switch args[1] {
	case "ls":
		if f.exists {
			return []byte(f.name), nil
		}
		return nil, nil
	case "inspect":
		label := f.identity
		if f.unowned {
			label = "another-root"
		}
		b, _ := json.Marshal([]map[string]any{{"Name": "/" + f.name, "Image": "sha256:test", "Config": map[string]any{"Image": f.image, "User": runtimeUser(), "Labels": map[string]string{"swarmjet.runtime-profile": "default", "swarmjet.runtime-root": label}}, "State": map[string]any{"Running": f.running, "Status": "running"}}})
		return b, nil
	case "create":
		f.exists = true
		return []byte("container-id"), nil
	case "start":
		f.running = true
		return nil, nil
	case "stop":
		f.running = false
		if f.stopped != nil {
			f.stopOnce.Do(func() { close(f.stopped) })
		}
		return nil, nil
	case "rm":
		f.exists = false
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %v", args)
}
func setup(t *testing.T) (*server, *fakeDocker) {
	t.Helper()
	s := New(Config{DataDir: t.TempDir(), Image: "runtime:test", Secret: "worker-secret", Env: map[string]string{"OPENAI_API_KEY": "provider-secret", "GITLAB_MERGE_TOKEN": "must-not-pass", "ADMIN_PASSWORD": "must-not-pass"}}).(*server)
	f := &fakeDocker{name: s.name, identity: s.identity, image: s.cfg.Image, catalog: "openai/gpt-test\nanthropic/other\nprovider-secret\n", stopped: make(chan struct{})}
	s.cfg.DockerCommand = f.run
	return s, f
}
func request(t *testing.T, s http.Handler, path, secret string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if secret != "" {
		r.Header.Set("X-SwarmJet-Runtime-Secret", secret)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func writeProfile(t *testing.T, s *server, rel, body string) {
	t.Helper()
	p := filepath.Join(s.dirs.Home, rel)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestAuthenticationRequiredForAllRoutes(t *testing.T) {
	s, f := setup(t)
	for _, path := range []string{"/status", "/models", "/terminal", "/unknown"} {
		for _, secret := range []string{"", "wrong"} {
			w := request(t, s, path, secret)
			if w.Code != 401 {
				t.Fatalf("%s secret=%q status=%d", path, secret, w.Code)
			}
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("unauthorized request invoked Docker")
	}
	empty := New(Config{DataDir: t.TempDir(), Image: "runtime:test"})
	if request(t, empty, "/status", "").Code != 401 {
		t.Fatal("empty configured secret accepted")
	}
}
func TestStatusIsReadonlyAndNeverReturnsCredentialValues(t *testing.T) {
	s, f := setup(t)
	writeProfile(t, s, ".local/share/opencode/auth.json", `{"anthropic":{"type":"api","key":"very-secret-auth-value"}}`)
	writeProfile(t, s, ".config/opencode/opencode.jsonc", "{ // local provider\n \"provider\": {\"custom\": {\"options\": {\"apiKey\":\"very-secret-config-value\",},},},}\n")
	writeProfile(t, s, ".codex/auth.json", `{"tokens":{"access_token":"very-secret-codex-value"}}`)
	w := request(t, s, "/status", "worker-secret")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, secret := range []string{"very-secret", "provider-secret", "must-not-pass"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("credential leaked: %s", secret)
		}
	}
	var status statusResponse
	if e := json.Unmarshal(w.Body.Bytes(), &status); e != nil {
		t.Fatal(e)
	}
	if !status.Available || !status.OpenCode.Configured || !status.OpenCode.ConfigExists || !status.Codex.Configured || status.Codex.ExecutionSupported {
		t.Fatalf("wrong status %+v", status)
	}
	if strings.Join(status.OpenCode.Providers, ",") != "anthropic,custom,openai" {
		t.Fatal(status.OpenCode.Providers)
	}
	for _, args := range f.calls {
		if len(args) > 1 && (args[1] == "create" || args[1] == "start") {
			t.Fatal("status eagerly launched configuration container")
		}
	}
}
func TestSetupMountsOnlyProfileAndWhitelistedEnvironment(t *testing.T) {
	s, f := setup(t)
	t.Setenv("GITLAB_MERGE_TOKEN", "host-merge-secret")
	if e := s.ensure(context.Background()); e != nil {
		t.Fatal(e)
	}
	var create []string
	for i, args := range f.calls {
		if len(args) > 1 && args[1] == "create" {
			create = args
			if strings.Contains(strings.Join(f.envs[i], "\n"), "host-merge-secret") || strings.Contains(strings.Join(f.envs[i], "\n"), "must-not-pass") {
				t.Fatal("worker credentials inherited")
			}
		}
	}
	if create == nil {
		t.Fatal("container not created")
	}
	mounts := []string{}
	for i, arg := range create {
		if arg == "--mount" {
			mounts = append(mounts, create[i+1])
		}
	}
	if len(mounts) != 1 || mounts[0] != "type=bind,src="+s.dirs.Home+",dst=/runtime-home" {
		t.Fatalf("unsafe mounts %v", mounts)
	}
	joined := strings.Join(create, " ")
	for _, want := range []string{"swarmjet.runtime-profile=default", "swarmjet.runtime-root=" + s.identity, "HOME=/runtime-home", "CODEX_HOME=/runtime-home/.codex", "HISTFILE=/dev/null", "HISTSIZE=0", "HISTFILESIZE=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(joined, "provider-secret") || strings.Contains(joined, "docker.sock") {
		t.Fatal("credential argv/socket leak")
	}
	if e := s.stop(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(s.dirs.Home); e != nil {
		t.Fatal("profile lost on stop")
	}
}
func TestOwnershipMismatchNeverAdoptedOrStopped(t *testing.T) {
	s, f := setup(t)
	f.exists = true
	f.running = true
	f.unowned = true
	if e := s.ensure(context.Background()); e == nil {
		t.Fatal("unowned container adopted")
	}
	if e := s.stop(context.Background()); e == nil {
		t.Fatal("unowned container stopped")
	}
	for _, args := range f.calls {
		if len(args) > 1 && (args[1] == "stop" || args[1] == "rm" || args[1] == "start") {
			t.Fatal("unowned container mutated")
		}
	}
}
func TestModelsFilteredToConfiguredProvidersWithoutQueries(t *testing.T) {
	s, f := setup(t)
	writeProfile(t, s, ".config/opencode/opencode.json", `{"provider":{"custom":{"options":{"apiKey":"never-return"}}}}`)
	f.catalog = "openai/gpt-test\nanthropic/not-configured\ncustom/local-model\nopenai/gpt-test\nlog: never-return\n"
	w := request(t, s, "/models", "worker-secret")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Models    []string
		Providers []string
		Warnings  []string
	}
	if e := json.Unmarshal(w.Body.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	if strings.Join(response.Models, ",") != "custom/local-model,openai/gpt-test" {
		t.Fatal(response.Models)
	}
	if strings.Contains(w.Body.String(), "never-return") {
		t.Fatal("secret output leaked")
	}
	for _, args := range f.calls {
		if args[0] == "exec" && args[len(args)-1] != "models" {
			t.Fatalf("unexpected model execution %v", args)
		}
	}
}
func TestProfileFileSymlinksAreNotFollowed(t *testing.T) {
	s, _ := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.json")
	os.WriteFile(outside, []byte(`{"external-provider":{"key":"outside-secret"}}`), 0600)
	os.MkdirAll(filepath.Join(s.dirs.Home, ".local", "share", "opencode"), 0700)
	os.Symlink(outside, filepath.Join(s.dirs.OpenCodeData, "auth.json"))
	metadata := profileInfo(s.dirs, nil)
	if len(metadata.Providers) != 0 || len(metadata.Warnings) == 0 {
		t.Fatal("credential symlink followed")
	}
	if _, e := readProfileFile(s.dirs.Home, "../secret.json"); e == nil {
		t.Fatal("path traversal accepted")
	}
	os.MkdirAll(filepath.Join(s.dirs.Home, ".config"), 0700)
	os.Symlink(filepath.Dir(outside), filepath.Join(s.dirs.Home, ".config", "opencode"))
	if _, e := readProfileFile(s.dirs.Home, ".config/opencode/secret.json"); e == nil {
		t.Fatal("directory symlink followed")
	}
}
func TestWebsocketPTYAuthSingleSessionAndShutdown(t *testing.T) {
	s, f := setup(t)
	var terminalArgs []string
	s.cfg.TerminalCommand = func(ctx context.Context, args, env []string) *exec.Cmd {
		terminalArgs = append([]string{}, args...)
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'terminal-ready\\n'; exec cat")
		cmd.Env = env
		return cmd
	}
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/terminal"
	if conn, response, e := websocket.DefaultDialer.Dial(url, nil); e == nil || response.StatusCode != 401 {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("unauthenticated websocket accepted")
	}
	headers := http.Header{"X-SwarmJet-Runtime-Secret": []string{"worker-secret"}}
	conn, _, e := websocket.DefaultDialer.Dial(url, headers)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, b, e := conn.ReadMessage()
	if e != nil || kind != websocket.BinaryMessage || !strings.Contains(string(b), "terminal-ready") {
		t.Fatalf("PTY output missing %d %s %v", kind, b, e)
	}
	if second, response, e := websocket.DefaultDialer.Dial(url, headers); e == nil || response.StatusCode != 409 {
		if second != nil {
			second.Close()
		}
		t.Fatal("concurrent terminal accepted")
	}
	other := httptest.NewServer(New(s.cfg))
	defer other.Close()
	otherURL := "ws" + strings.TrimPrefix(other.URL, "http") + "/terminal"
	if second, response, e := websocket.DefaultDialer.Dial(otherURL, headers); e == nil || response.StatusCode != 409 {
		if second != nil {
			second.Close()
		}
		t.Fatal("another server instance bypassed profile terminal lock")
	}
	if e = conn.WriteJSON(map[string]any{"type": "resize", "cols": 120, "rows": 40}); e != nil {
		t.Fatal(e)
	}
	if e = conn.WriteJSON(map[string]any{"type": "input", "data": "hello-terminal\n"}); e != nil {
		t.Fatal(e)
	}
	for {
		_, b, e = conn.ReadMessage()
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(b), "hello-terminal") {
			break
		}
	}
	if !strings.Contains(strings.Join(terminalArgs, " "), "HISTFILE=/dev/null") {
		t.Fatal("terminal history not disabled")
	}
	conn.Close()
	select {
	case <-f.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal disconnect did not stop setup container")
	}
}
func TestModelOutputLimitAndMalformedAuthRemainSafe(t *testing.T) {
	s, f := setup(t)
	f.catalog = strings.Repeat("openai/model\n", maxOutput/8)
	w := request(t, s, "/models", "worker-secret")
	if w.Code != 502 || strings.Contains(w.Body.String(), "openai/model") {
		t.Fatal("oversized catalog accepted or echoed")
	}
	writeProfile(t, s, ".local/share/opencode/auth.json", `{"secret-value-with-malformed-json"`)
	metadata := profileInfo(s.dirs, nil)
	if len(metadata.Providers) != 0 || len(metadata.Warnings) == 0 {
		t.Fatal("malformed credentials accepted")
	}
}
func TestProfileFIFOIsRejectedWithoutBlocking(t *testing.T) {
	s, _ := setup(t)
	os.MkdirAll(s.dirs.OpenCodeData, 0700)
	if e := syscall.Mkfifo(filepath.Join(s.dirs.OpenCodeData, "auth.json"), 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := readProfileFile(s.dirs.Home, ".local/share/opencode/auth.json"); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("credential FIFO blocked metadata endpoint")
	}
}
func TestProfileParentSymlinkCannotExposeOtherCredentialKeys(t *testing.T) {
	s, _ := setup(t)
	outside := t.TempDir()
	path := filepath.Join(outside, "home", ".local", "share", "opencode")
	os.MkdirAll(path, 0700)
	os.WriteFile(filepath.Join(path, "auth.json"), []byte(`{"outside-provider":{"key":"outside-secret"}}`), 0600)
	parent := filepath.Dir(filepath.Dir(s.dirs.Home))
	os.MkdirAll(parent, 0700)
	if e := os.Symlink(outside, filepath.Dir(s.dirs.Home)); e != nil {
		t.Fatal(e)
	}
	metadata := profileInfo(s.dirs, nil)
	if len(metadata.Providers) != 0 || len(metadata.Warnings) == 0 {
		t.Fatal("profile parent symlink followed")
	}
}
func TestDeploymentInlineConfigIsWhitelistedBoundedAndNeverExposed(t *testing.T) {
	s, f := setup(t)
	s = New(Config{DataDir: s.cfg.DataDir, Image: s.cfg.Image, Secret: s.cfg.Secret, DockerCommand: f.run, Env: map[string]string{"OPENCODE_CONFIG_CONTENT": "{ // configured provider\n \"provider\": {\"inline-custom\": {\"options\":{\"apiKey\":\"inline-secret-never-return\"}},},}", "DATABASE_PASSWORD": "database-secret-never-pass"}}).(*server)
	f.catalog = "inline-custom/local\nopenai/unconfigured\n"
	w := request(t, s, "/models", "worker-secret")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "inline-custom/local") || strings.Contains(w.Body.String(), "inline-secret") {
		t.Fatal(w.Code, w.Body.String())
	}
	seen := false
	for i, args := range f.calls {
		if len(args) > 1 && args[1] == "create" {
			if strings.Contains(strings.Join(args, " "), "inline-secret") {
				t.Fatal("inline config leaked into arguments")
			}
			env := strings.Join(f.envs[i], "\n")
			seen = strings.Contains(env, "OPENCODE_CONFIG_CONTENT=")
			if strings.Contains(env, "database-secret") {
				t.Fatal("nonprovider secret passed")
			}
		}
	}
	if !seen {
		t.Fatal("inline config was not passed to setup environment")
	}
	metadata := profileInfo(s.dirs, map[string]string{"OPENCODE_CONFIG_CONTENT": strings.Repeat("x", maxInlineConfig+1)})
	if len(metadata.Providers) != 0 || len(metadata.Warnings) == 0 {
		t.Fatal("oversized inline config not bounded")
	}
}
func TestModelFailureHasAPIErrorAndStatusReportsToolDetection(t *testing.T) {
	s, f := setup(t)
	w := request(t, s, "/status", "worker-secret")
	var status statusResponse
	json.Unmarshal(w.Body.Bytes(), &status)
	if status.ToolsChecked {
		t.Fatal("absent container tools claimed checked")
	}
	f.exists = true
	f.running = true
	w = request(t, s, "/status", "worker-secret")
	json.Unmarshal(w.Body.Bytes(), &status)
	if !status.ToolsChecked || !status.OpenCode.Installed || !status.Codex.Installed {
		t.Fatal("installed tools not checked")
	}
	f.catalog = strings.Repeat("x", maxOutput+1)
	w = request(t, s, "/models", "worker-secret")
	var failed struct {
		Error string `json:"error"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &failed); e != nil || w.Code != 502 || failed.Error == "" {
		t.Fatalf("missing API error: %d %s", w.Code, w.Body.String())
	}
}
