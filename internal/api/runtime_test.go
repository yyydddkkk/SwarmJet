package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeProxyPrivateAuthenticationAndPath(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" || r.URL.RawQuery != "" {
			t.Errorf("unexpected proxy path %s", r.URL)
		}
		if r.Header.Get("X-SwarmJet-Runtime-Secret") != "private-worker-secret" {
			t.Error("missing internal auth")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("browser credentials forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"available":true}`))
	}))
	defer worker.Close()
	a := &server{cfg: Config{RuntimeURL: worker.URL, RuntimeSecret: "private-worker-secret"}}
	req := httptest.NewRequest("GET", "http://console/api/runtime/status?token=attacker", nil)
	req.Header.Set("Cookie", "swarmjet_session=browser-cookie")
	req.Header.Set("Authorization", "browser-token")
	req.Header.Set("X-SwarmJet-Runtime-Secret", "untrusted-browser-value")
	w := httptest.NewRecorder()
	a.runtime(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-worker-secret") {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
}
func TestRuntimeTerminalRejectsCrossOrigin(t *testing.T) {
	a := &server{cfg: Config{RuntimeURL: "http://worker:8091", RuntimeSecret: "secret", AllowedOrigin: "https://console.example"}}
	for _, origin := range []string{"", "https://attacker.example", "null"} {
		r := httptest.NewRequest("GET", "https://console.example/api/runtime/terminal", nil)
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		a.runtime(w, r)
		if w.Code != 403 {
			t.Fatalf("origin=%s code=%d", origin, w.Code)
		}
	}
}
func TestRuntimeDisabledAndUnknownRoutes(t *testing.T) {
	for _, tc := range []struct {
		path, method string
		code         int
	}{{"/status", "GET", 503}, {"/terminal", "POST", 405}, {"/docker/containers", "GET", 404}} {
		w := httptest.NewRecorder()
		(&server{}).runtime(w, httptest.NewRequest(tc.method, "http://console/api/runtime"+tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s returned %d", tc.path, w.Code)
		}
	}
}
