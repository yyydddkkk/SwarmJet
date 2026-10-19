package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeMountsInProcessCLILogin(t *testing.T) {
	var seen string
	var sawBrowserAuth bool
	cli := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		sawBrowserAuth = r.Header.Get("X-SwarmJet-Runtime-Secret") != "" || r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"available":true}`)
	})
	a := &server{cfg: Config{CLILogin: cli}}
	req := httptest.NewRequest("GET", "http://console/api/runtime/status?token=attacker", nil)
	req.Header.Set("Cookie", "swarmjet_session=browser-cookie")
	w := httptest.NewRecorder()
	a.runtime(w, req)
	if w.Code != http.StatusOK || seen != "/status" {
		t.Fatalf("response %d path %q", w.Code, seen)
	}
	if sawBrowserAuth {
		t.Fatal("browser credentials were forwarded to the CLI login handler")
	}
	if strings.Contains(w.Body.String(), "browser-cookie") {
		t.Fatal("session cookie leaked")
	}
}

func TestRuntimeTerminalRequiresUpgradeAndSameOrigin(t *testing.T) {
	a := &server{cfg: Config{CLILogin: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), AllowedOrigin: "https://console.example"}}
	for _, tc := range []struct {
		upgrade, origin string
		code            int
	}{
		{"", "https://console.example", http.StatusBadRequest},
		{"websocket", "", http.StatusForbidden},
		{"websocket", "https://attacker.example", http.StatusForbidden},
		{"websocket", "null", http.StatusForbidden},
	} {
		r := httptest.NewRequest("GET", "https://console.example/api/runtime/terminal", nil)
		if tc.upgrade != "" {
			r.Header.Set("Upgrade", tc.upgrade)
		}
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		a.runtime(w, r)
		if w.Code != tc.code {
			t.Fatalf("upgrade=%q origin=%q code=%d", tc.upgrade, tc.origin, w.Code)
		}
	}
}

func TestRuntimeDisabledAndUnknownRoutes(t *testing.T) {
	for _, tc := range []struct {
		path, method string
		code         int
	}{
		{"/status", "GET", http.StatusServiceUnavailable},
		{"/models", "GET", http.StatusServiceUnavailable},
		{"/terminal", "GET", http.StatusServiceUnavailable},
		{"/status", "POST", http.StatusMethodNotAllowed},
		{"/docker/containers", "GET", http.StatusNotFound},
		{"/images", "GET", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		(&server{}).runtime(w, httptest.NewRequest(tc.method, "http://console/api/runtime"+tc.path, nil))
		if w.Code != tc.code {
			t.Fatalf("%s %s returned %d", tc.method, tc.path, w.Code)
		}
	}
}
