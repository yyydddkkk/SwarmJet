package llmproxy

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
)

func writeToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestBroker(t *testing.T, brokerURL, gatewayURL string) *BrokerClient {
	t.Helper()
	client, err := NewBrokerClient(BrokerConfig{
		BrokerURL:        brokerURL,
		BrokerTokenFile:  writeToken(t, "broker-secret"),
		GatewayURL:       gatewayURL,
		GatewayTokenFile: writeToken(t, "gateway-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestBrokerSnapshotAndSetAPIKeyMaskValues(t *testing.T) {
	secret := "sk-very-secret-value-1234"
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer broker-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/snapshot":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"credentials": []map[string]any{{
					"id": 1, "provider": "deepseek",
					"credential": map[string]any{"type": "api_key", "key": secret},
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
				t.Fatal(err)
			}
			if body.Provider != "deepseek" || body.Credential.Type != "api_key" || body.Credential.Key != secret {
				t.Errorf("credential body = %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer broker.Close()

	client := newTestBroker(t, broker.URL, "")
	credentials, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].Masked == "" || strings.Contains(credentials[0].Masked, secret) {
		t.Fatalf("credentials = %+v", credentials)
	}
	created, err := client.SetAPIKey(context.Background(), "deepseek", secret)
	if err != nil {
		t.Fatal(err)
	}
	if created.Masked == secret || strings.Contains(created.Masked, secret) || !created.Configured {
		t.Fatalf("created = %+v", created)
	}
	masked, err := client.SetModelCredential(context.Background(), "deepseek", secret)
	if err != nil || masked == secret || strings.Contains(masked, secret) {
		t.Fatalf("masked = %q err = %v", masked, err)
	}
}

func TestBrokerDeleteProviderDisablesEveryRow(t *testing.T) {
	disabled := []string{}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/snapshot":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"credentials": []map[string]any{
					{"id": 7, "provider": "deepseek", "credential": map[string]any{"type": "api_key", "key": "sk-one"}},
					{"id": 8, "provider": "deepseek", "credential": map[string]any{"type": "api_key", "key": "sk-two"}},
					{"id": 9, "provider": "openai", "credential": map[string]any{"type": "api_key", "key": "sk-other"}},
				},
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/credential/") && strings.HasSuffix(r.URL.Path, "/disable"):
			disabled = append(disabled, r.URL.Path)
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer broker.Close()

	client := newTestBroker(t, broker.URL, "")
	if err := client.DeleteProvider(context.Background(), "deepseek"); err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 2 || !strings.Contains(disabled[0], "/7/") || !strings.Contains(disabled[1], "/8/") {
		t.Fatalf("disabled = %v", disabled)
	}
	if err := client.DeleteProvider(context.Background(), "missing"); err != ErrCredentialNotFound {
		t.Fatalf("missing provider err = %v", err)
	}
}

func TestBrokerModelsUsesGatewayToken(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer gateway-secret" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek/deepseek-chat","owned_by":"deepseek","display_name":"DeepSeek Chat","context_length":128000}]}`)
	}))
	defer gateway.Close()
	client := newTestBroker(t, "", gateway.URL)
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Provider != "deepseek" || models[0].ContextLength != 128000 {
		t.Fatalf("models = %+v", models)
	}
}

func TestBrokerErrorDoesNotIncludeResponseBody(t *testing.T) {
	secret := "sk-must-not-leak-9999"
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"bad key `+secret+`"}`)
	}))
	defer broker.Close()
	client := newTestBroker(t, broker.URL, "")
	_, err := client.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the broker body: %v", err)
	}
}

func TestValidateAPIKey(t *testing.T) {
	if err := ValidateAPIKey(""); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := ValidateAPIKey(" sk-padded "); err == nil {
		t.Fatal("padded key accepted")
	}
	if err := ValidateAPIKey("sk-with\nnewline"); err == nil {
		t.Fatal("control character accepted")
	}
	if err := ValidateAPIKey("sk-normal-key"); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"deepseek", "openai-codex", "google"} {
		if !ValidProvider(provider) {
			t.Fatalf("provider %q rejected", provider)
		}
	}
	for _, provider := range []string{"", "DeepSeek", "a/b", "../etc", "with space"} {
		if ValidProvider(provider) {
			t.Fatalf("provider %q accepted", provider)
		}
	}
}
