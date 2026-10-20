package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetupStatusWithoutBrokerReportsModelDefaults(t *testing.T) {
	a := &server{cfg: Config{
		CellsEnabled: true,
		CellsImage:   "agent:test",
		CellsModel:   "provider/model",
		DefaultModel: "deepseek/deepseek-chat",
	}}
	w := httptest.NewRecorder()
	a.setupStatus(w, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var status setupStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Models.Available || status.Models.Configured {
		t.Fatalf("models unexpectedly available = %+v", status.Models)
	}
	if status.Models.DefaultModel != "deepseek/deepseek-chat" {
		t.Fatalf("defaultModel = %q", status.Models.DefaultModel)
	}
	if !status.Cells.Enabled || status.Cells.DockerAvailable {
		t.Fatalf("cells = %+v", status.Cells)
	}
}

func TestSetupStatusReportsConfiguredModel(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/snapshot" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"credentials":[{"id":1,"provider":"deepseek","credential":{"type":"api_key","key":"sk-secret-value-1234"}}]}`)
	}))
	defer broker.Close()
	a := &server{cfg: Config{
		Models:       modelTestClient(t, broker.URL, ""),
		DefaultModel: "deepseek/deepseek-chat",
	}}
	w := httptest.NewRecorder()
	a.setupStatus(w, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var status setupStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Models.Available || !status.Models.Configured {
		t.Fatalf("models = %+v", status.Models)
	}
	if len(status.Models.Providers) != 1 || status.Models.Providers[0] != "deepseek" {
		t.Fatalf("providers = %v", status.Models.Providers)
	}
}

func TestRuntimeLoginRoutesAreGone(t *testing.T) {
	_, handler, cookie := newHiveAPITest(t)
	for _, path := range []string{"/api/runtime/status", "/api/runtime/models", "/api/runtime/terminal"} {
		w := tokenAPIRequest(t, handler, cookie, http.MethodGet, path, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404 after the CLI login terminal was removed", path, w.Code)
		}
	}
}
