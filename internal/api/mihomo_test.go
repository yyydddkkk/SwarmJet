package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMihomoDisabledHasExplicitStatus(t *testing.T) {
	a := &server{}
	w := httptest.NewRecorder()
	a.mihomo(w, httptest.NewRequest("GET", "/api/mihomo/status", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestMihomoProxyConstrainedAndPrivate(t *testing.T) {
	secret := strings.Repeat("s", 32)
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-SwarmJet-Mihomo-Secret") != secret || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("private management auth was not isolated")
		}
		if r.URL.Path != "/probe" || r.Method != "POST" {
			t.Error("wrong route")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["id"] != "revision" {
			t.Error("request body lost")
		}
		respond(w, 200, map[string]bool{"ok": true})
	}))
	defer remote.Close()
	a := &server{cfg: Config{MihomoURL: remote.URL, MihomoSecret: secret}}
	for _, path := range []string{"/api/mihomo/access", "/api/mihomo/upgrade", "/api/mihomo/configs"} {
		w := httptest.NewRecorder()
		a.mihomo(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal("exposed private core API", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/mihomo/probe", strings.NewReader(`{"id":"revision"}`))
	r.Header.Set("Cookie", "session=private")
	r.Header.Set("Authorization", "Bearer browser-secret")
	a.mihomo(w, r)
	if w.Code != 200 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
}
func TestMihomoControlRedirectDoesNotForwardCredentials(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer remote.Close()
	a := &server{cfg: Config{MihomoURL: remote.URL, MihomoSecret: strings.Repeat("x", 32)}}
	w := httptest.NewRecorder()
	a.mihomo(w, httptest.NewRequest("GET", "/api/mihomo/status", nil))
	if leaked {
		t.Fatal("admin credentials followed a redirect")
	}
}
