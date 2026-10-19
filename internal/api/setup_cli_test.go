package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetupStatusReportsCellCLIWithoutLoginTerminal(t *testing.T) {
	a := &server{cfg: Config{
		CellsEnabled: true,
		CellsImage:   "agent:test",
		CellsModel:   "provider/model",
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
	if status.CLILogin.Available || status.CLILogin.LoggedIn || status.CLILogin.CLI != "omp" {
		t.Fatalf("cliLogin = %+v", status.CLILogin)
	}
	if !status.Cells.Enabled || status.Cells.DockerAvailable {
		t.Fatalf("cells = %+v", status.Cells)
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
