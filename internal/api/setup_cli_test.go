package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetupStatusUsesCLILoginHook(t *testing.T) {
	a := &server{cfg: Config{
		CellsEnabled:   true,
		CellsImage:     "agent:test",
		CellsModel:     "provider/model",
		CLILoginStatus: func() (bool, bool) { return true, true },
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
	if !status.CLILogin.Available || !status.CLILogin.LoggedIn || status.CLILogin.CLI != "opencode" {
		t.Fatalf("cliLogin = %+v", status.CLILogin)
	}
	if !status.Cells.Enabled || status.Cells.DockerAvailable {
		t.Fatalf("cells = %+v", status.Cells)
	}
}
