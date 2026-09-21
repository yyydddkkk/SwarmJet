package runtimeadmin

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in with SWARMJET_RUNTIME_IMAGE. Only a new temporary profile and its
// uniquely owned setup container are touched; no real authentication is used.
func TestActualDockerTerminal(t *testing.T) {
	image := os.Getenv("SWARMJET_RUNTIME_IMAGE")
	if image == "" {
		t.Skip("set SWARMJET_RUNTIME_IMAGE for isolated actual Docker/PTY smoke")
	}
	s := New(Config{DataDir: t.TempDir(), Image: image, Secret: "runtime-smoke-only"}).(*server)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, e := s.image(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, exists, e := s.inspect(cleanup)
		if e != nil {
			t.Errorf("inspect owned smoke container: %v", e)
			return
		}
		if exists {
			_ = s.stop(cleanup)
			if _, e = s.docker(cleanup, nil, "container", "rm", s.name); e != nil {
				t.Errorf("remove own smoke container: %v", e)
			}
		}
	})
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/terminal"
	headers := http.Header{"X-SwarmJet-Runtime-Secret": []string{s.cfg.Secret}}
	connect := func() *websocket.Conn {
		t.Helper()
		conn, response, e := websocket.DefaultDialer.DialContext(ctx, url, headers)
		if e != nil {
			if response != nil {
				t.Fatalf("terminal HTTP %d: %v", response.StatusCode, e)
			}
			t.Fatal(e)
		}
		conn.SetReadDeadline(time.Now().Add(25 * time.Second))
		return conn
	}
	command := func(conn *websocket.Conn, text, marker string) string {
		t.Helper()
		if e := conn.WriteJSON(map[string]any{"type": "input", "data": text + "\n"}); e != nil {
			t.Fatal(e)
		}
		output := ""
		for !strings.Contains(output, marker) {
			kind, b, e := conn.ReadMessage()
			if e != nil {
				t.Fatalf("terminal did not produce %s: %v", marker, e)
			}
			if kind == websocket.BinaryMessage {
				output += string(b)
			} else {
				var event struct{ Type, Message string }
				json.Unmarshal(b, &event)
				if event.Type == "error" {
					t.Fatalf("terminal error: %s", event.Message)
				}
			}
			if len(output) > maxOutput {
				t.Fatal("unexpected excessive terminal output")
			}
		}
		return strings.ReplaceAll(output, "\r", "")
	}
	awaitStopped := func() {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			c, exists, e := s.inspect(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if exists && !c.State.Running {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("owned setup container still running after disconnect")
	}
	first := connect()
	defer first.Close()
	output := command(first, `stty -echo; printf '\nSJ_RUNTIME_%s\n' READY; command -v opencode; command -v codex; opencode --version >/dev/null && printf 'SJ_OPENCODE_%s\n' OK; codex --version >/dev/null && printf 'SJ_CODEX_%s\n' OK; printf 'SJ_HISTORY:%s:%s:%s\n' "$HISTFILE" "$HISTSIZE" "$HISTFILESIZE"; printf '\nSJ_TOOLS_%s\n' DONE`, "SJ_TOOLS_DONE")
	for _, want := range []string{"SJ_RUNTIME_READY", "/opencode\n", "/codex\n", "SJ_OPENCODE_OK", "SJ_CODEX_OK", "SJ_HISTORY:/dev/null:0:0"} {
		if !strings.Contains(output, want) {
			t.Fatalf("terminal check missing %q", want)
		}
	}
	if e := first.WriteJSON(map[string]any{"type": "resize", "cols": 120, "rows": 40}); e != nil {
		t.Fatal(e)
	}
	// Docker relays SIGWINCH to its exec resize API asynchronously.
	time.Sleep(200 * time.Millisecond)
	output = command(first, `stty size; printf '\nSJ_RESIZE_%s\n' DONE`, "SJ_RESIZE_DONE")
	if !strings.Contains(output, "40 120\n") {
		t.Fatalf("PTY resize not applied; output=%q", output)
	}
	command(first, `printf profile-ok > /runtime-home/.swarmjet-terminal-smoke; printf '\nSJ_PERSIST_%s\n' DONE`, "SJ_PERSIST_DONE")
	first.Close()
	awaitStopped()
	saved, e := os.ReadFile(filepath.Join(s.dirs.Home, ".swarmjet-terminal-smoke"))
	if e != nil || string(saved) != "profile-ok" {
		t.Fatal("temporary profile file did not persist")
	}
	second := connect()
	defer second.Close()
	command(second, `stty -echo; test "$(cat /runtime-home/.swarmjet-terminal-smoke)" = profile-ok && printf '\nSJ_RECONNECT_%s\n' OK`, "SJ_RECONNECT_OK")
	second.Close()
	awaitStopped()
}
