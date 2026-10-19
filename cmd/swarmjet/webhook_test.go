package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/service"
)

type recordingInbox struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingInbox) AcceptWebhook(_ context.Context, _, id, _ string, _ []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

func TestWebhookListenerServesOnlyWebhook(t *testing.T) {
	inbox := &recordingInbox{}
	svc := &service.Service{Webhooks: inbox}
	secret := "test-webhook-secret"
	server := httptest.NewServer(webhookRoutes(svc.GitHubWebhookDynamic(func(context.Context) (string, error) { return secret, nil })))
	defer server.Close()

	body := `{"repository":{"full_name":"acme/demo"}}`
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	post := func(sig string) int {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Delivery", "delivery-1")
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-Hub-Signature-256", sig)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if code := post("sha256=" + strings.Repeat("0", 64)); code != http.StatusUnauthorized {
		t.Fatalf("bad signature status = %d", code)
	}
	if len(inbox.ids) != 0 {
		t.Fatalf("unsigned delivery enqueued: %v", inbox.ids)
	}
	if code := post(signature); code != http.StatusAccepted {
		t.Fatalf("signed status = %d", code)
	}
	if len(inbox.ids) != 1 || inbox.ids[0] != "delivery-1" {
		t.Fatalf("enqueued = %v", inbox.ids)
	}

	for _, path := range []string{"/", "/index.html", "/mcp", "/api/session", "/api/setup/status", "/api/login", "/readyz", "/metrics", "/api/webhooks/github/x"} {
		res, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
	}
	res, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", res.StatusCode)
	}
}
