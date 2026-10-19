package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/llmproxy"
)

func tokenFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func modelTestClient(t *testing.T, brokerURL, gatewayURL string) *llmproxy.BrokerClient {
	t.Helper()
	client, err := llmproxy.NewBrokerClient(llmproxy.BrokerConfig{
		BrokerURL:        brokerURL,
		BrokerTokenFile:  tokenFile(t, "broker-token"),
		GatewayURL:       gatewayURL,
		GatewayTokenFile: tokenFile(t, "gateway-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestModelsEndpointMasksCredentials(t *testing.T) {
	const existingKey = "sk-existing-secret-1234"
	const newKey = "sk-new-secret-5678"
	posted := ""
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/snapshot":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"credentials": []map[string]any{{
					"id": 1, "provider": "deepseek",
					"credential": map[string]any{"type": "api_key", "key": existingKey},
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credential":
			var body struct {
				Provider   string `json:"provider"`
				Credential struct {
					Type string `json:"type"`
					Key  string `json:"key"`
				} `json:"credential"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode credential: %v", err)
			}
			posted = body.Credential.Key
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": []any{}})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/credential/"):
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer broker.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek/deepseek-chat","owned_by":"deepseek","display_name":"DeepSeek Chat"}]}`)
	}))
	defer gateway.Close()

	srv := &server{cfg: Config{
		Models:       modelTestClient(t, broker.URL, gateway.URL),
		DefaultModel: "deepseek/deepseek-chat",
	}}

	rec := httptest.NewRecorder()
	srv.models(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/models = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{existingKey, newKey} {
		if strings.Contains(body, secret) {
			t.Fatalf("GET /api/models leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "••••1234") {
		t.Fatalf("GET /api/models has no masked value: %s", body)
	}
	var listing modelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.DefaultModel != "deepseek/deepseek-chat" || !listing.GatewayAvailable || len(listing.Models) != 1 {
		t.Fatalf("listing = %+v", listing)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/models/credentials/deepseek",
		strings.NewReader(`{"apiKey":"`+newKey+`"}`))
	srv.models(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if posted != newKey {
		t.Fatal("the broker did not receive the submitted key")
	}
	if strings.Contains(rec.Body.String(), newKey) || !strings.Contains(rec.Body.String(), "••••5678") {
		t.Fatalf("PUT response = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.models(rec, httptest.NewRequest(http.MethodDelete, "/api/models/credentials/deepseek", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.models(rec, httptest.NewRequest(http.MethodPut, "/api/models/credentials/DeepSeek",
		strings.NewReader(`{"apiKey":"sk-valid-key"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid provider = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.models(rec, httptest.NewRequest(http.MethodPut, "/api/models/credentials/deepseek",
		strings.NewReader(`{"apiKey":""}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty key = %d", rec.Code)
	}
}

func TestUsageEndpoint(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	ctx := context.Background()
	if err := s.RecordLLMUsage(ctx, domain.LLMUsage{
		AgentID: "agent-1", HiveID: "hive-1", Model: "deepseek/deepseek-chat",
		InputTokens: 100, OutputTokens: 50, CostUSD: 0.25,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLLMUsage(ctx, domain.LLMUsage{
		AgentID: "agent-2", HiveID: "hive-2", Model: "vendor/unknown",
		InputTokens: 1, OutputTokens: 1, CostUSD: 0, UnknownPrice: true,
	}); err != nil {
		t.Fatal(err)
	}

	w := tokenAPIRequest(t, handler, cookie, http.MethodGet, "/api/usage?hive=hive-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/usage = %d: %s", w.Code, w.Body.String())
	}
	var summary domain.LLMUsageSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Requests != 1 || summary.Totals.CostUSD != 0.25 || summary.HiveID != "hive-1" {
		t.Fatalf("summary = %+v", summary)
	}

	w = tokenAPIRequest(t, handler, cookie, http.MethodPost, "/api/usage", "{}")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/usage = %d", w.Code)
	}
}
