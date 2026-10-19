package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeWebhookInbox struct {
	mu       sync.Mutex
	accepted int
	provider string
	id       string
	event    string
	body     []byte
}

func (f *fakeWebhookInbox) AcceptWebhook(_ context.Context, provider, id, event string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepted++
	f.provider = provider
	f.id = id
	f.event = event
	f.body = append([]byte(nil), body...)
	return nil
}

func TestGitHubWebhookDynamicUsesCurrentSecret(t *testing.T) {
	inbox := &fakeWebhookInbox{}
	svc := &Service{Webhooks: inbox}
	handler := svc.GitHubWebhookDynamic(func(context.Context) (string, error) { return "current-secret", nil })
	body := `{"repository":{"full_name":"acme/demo"}}`
	mac := hmac.New(sha256.New, []byte("current-secret"))
	_, _ = mac.Write([]byte(body))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	r := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
	r.Header.Set("X-GitHub-Delivery", "delivery-1")
	r.Header.Set("X-GitHub-Event", "issues")
	r.Header.Set("X-Hub-Signature-256", signature)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	if inbox.accepted != 1 || inbox.provider != "github" || inbox.id != "delivery-1" || inbox.event != "issues" || string(inbox.body) != body {
		t.Fatalf("accepted = %+v", inbox)
	}
}

func TestGitHubWebhookDynamicRejectsBadSecretAndMissingApp(t *testing.T) {
	inbox := &fakeWebhookInbox{}
	svc := &Service{Webhooks: inbox}
	body := `{"repository":{"full_name":"acme/demo"}}`

	handler := svc.GitHubWebhookDynamic(func(context.Context) (string, error) { return "other-secret", nil })
	r := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
	r.Header.Set("X-GitHub-Delivery", "delivery-2")
	r.Header.Set("X-GitHub-Event", "issues")
	r.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || inbox.accepted != 0 {
		t.Fatalf("bad secret status = %d accepted = %d", w.Code, inbox.accepted)
	}

	handler = svc.GitHubWebhookDynamic(func(context.Context) (string, error) { return "", errors.New("not configured") })
	r = httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || inbox.accepted != 0 {
		t.Fatalf("missing secret status = %d accepted = %d", w.Code, inbox.accepted)
	}
}
