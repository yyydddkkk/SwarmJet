package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSystemShowsEffectiveDeploymentValuesWithoutSecrets(t *testing.T) {
	t.Setenv("SWARMJET_HOST_LISTEN_ADDRESS", "10.0.0.20:8180")
	a := &server{cfg: Config{AllowedOrigin: "https://console.example", Password: "private-admin-password", NodeSecret: "private-node-secret", RequireNode: true, SecureCookies: true}}
	w := httptest.NewRecorder()
	a.system(w, httptest.NewRequest(http.MethodGet, "/api/system", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["executionMode"] != "distributed" || got["publicOrigin"] != "https://console.example" || got["hostListen"] != "10.0.0.20:8180" || got["secureCookies"] != true {
		t.Fatalf("unexpected deployment status: %#v", got)
	}
	if strings.Contains(w.Body.String(), "private-admin-password") || strings.Contains(w.Body.String(), "private-node-secret") {
		t.Fatal("deployment status disclosed a secret")
	}
}

func TestSystemChecksReportDisabledServices(t *testing.T) {
	a := &server{}
	w := httptest.NewRecorder()
	a.system(w, httptest.NewRequest(http.MethodPost, "/api/system/checks", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"mihomo","status":"unavailable"`) {
		t.Fatalf("unexpected checks: status %d, body %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.system(w, httptest.NewRequest(http.MethodPost, "/api/system", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected method status %d", w.Code)
	}
}
