package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func tokenAPIRequest(t *testing.T, handler http.Handler, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://console.test"+path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", "https://console.test")
		r.Header.Set("X-Requested-With", "swarmjet")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestTokenAPICreateListRevoke(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	ctx := context.Background()

	if w := tokenAPIRequest(t, handler, nil, http.MethodGet, "/api/tokens", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d", w.Code)
	}
	created := tokenAPIRequest(t, handler, cookie, http.MethodPost, "/api/tokens", `{"name":"laptop","scope":"default"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var response tokenCreateResponse
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Plaintext) != 64 || response.Token.Name != "laptop" || response.Token.Scope != domain.TokenDefault {
		t.Fatalf("unexpected token %+v", response)
	}
	if response.Connect.MCPURL != "https://console.test/mcp" {
		t.Fatalf("mcp url = %q", response.Connect.MCPURL)
	}
	if !strings.Contains(response.Connect.ClaudeCode, response.Plaintext) ||
		!strings.Contains(response.Connect.Codex, response.Plaintext) ||
		response.Connect.JSON.MCPServers["swarmjet"].Headers["Authorization"] != "Bearer "+response.Plaintext {
		t.Fatal("connection bundle does not contain the plaintext")
	}
	if _, err := s.TokenByPlaintext(ctx, response.Plaintext); err != nil {
		t.Fatalf("token not stored: %v", err)
	}

	admin := tokenAPIRequest(t, handler, cookie, http.MethodPost, "/api/tokens", `{"name":"ops","scope":"admin"}`)
	if admin.Code != http.StatusCreated {
		t.Fatalf("admin create = %d %s", admin.Code, admin.Body.String())
	}

	// Agent tokens are platform-minted and must stay invisible in the console.
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "demo", Repository: "acme/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO agents(id,hive_id,role,state,cli) VALUES('agent1',$1,'forager','active','opencode')`, hive.ID); err != nil {
		t.Fatal(err)
	}
	agentPlaintext, agentToken, err := s.CreateAgentToken(ctx, "agent1")
	if err != nil {
		t.Fatal(err)
	}
	if agentPlaintext == "" {
		t.Fatal("agent token plaintext missing")
	}

	list := tokenAPIRequest(t, handler, cookie, http.MethodGet, "/api/tokens", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	var listed struct {
		Tokens []domain.Token `json:"tokens"`
	}
	if err = json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tokens) != 2 {
		t.Fatalf("listed %d tokens: %+v", len(listed.Tokens), listed.Tokens)
	}
	for _, token := range listed.Tokens {
		if token.ID == agentToken.ID {
			t.Fatal("agent token listed")
		}
	}
	if strings.Contains(list.Body.String(), response.Plaintext) || strings.Contains(list.Body.String(), agentPlaintext) {
		t.Fatal("token plaintext listed")
	}

	w := tokenAPIRequest(t, handler, cookie, http.MethodDelete, "/api/tokens/"+response.Token.ID, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", w.Code, w.Body.String())
	}
	if _, err = s.TokenByPlaintext(ctx, response.Plaintext); err == nil {
		t.Fatal("revoked token still authenticates")
	}
	if w = tokenAPIRequest(t, handler, cookie, http.MethodDelete, "/api/tokens/"+response.Token.ID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("second revoke = %d %s", w.Code, w.Body.String())
	}
}

func TestTokenAPIValidatesInput(t *testing.T) {
	_, handler, cookie := newHiveAPITest(t)
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"name":"","scope":"default"}`, http.StatusBadRequest},
		{`{"name":"x","scope":"agent"}`, http.StatusBadRequest},
		{`{"name":"x","scope":"root"}`, http.StatusBadRequest},
		{`{"name":"` + strings.Repeat("x", 201) + `"}`, http.StatusBadRequest},
		{`{"unknown":true}`, http.StatusBadRequest},
	} {
		w := tokenAPIRequest(t, handler, cookie, http.MethodPost, "/api/tokens", tc.body)
		if w.Code != tc.code {
			t.Fatalf("%s = %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	if w := tokenAPIRequest(t, handler, cookie, http.MethodPut, "/api/tokens", `{}`); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d", w.Code)
	}
	if w := tokenAPIRequest(t, handler, cookie, http.MethodDelete, "/api/tokens/not-an-id!", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", w.Code)
	}
}

func TestSetupStatusReportsCLILogin(t *testing.T) {
	_, handler, cookie := newHiveAPITest(t)
	w := tokenAPIRequest(t, handler, cookie, http.MethodGet, "/api/setup/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var status setupStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.CLILogin.CLI != "opencode" || status.CLILogin.Available {
		t.Fatalf("unexpected cliLogin %+v", status.CLILogin)
	}
	if w = tokenAPIRequest(t, handler, cookie, http.MethodPost, "/api/setup/status", `{}`); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", w.Code)
	}
}
