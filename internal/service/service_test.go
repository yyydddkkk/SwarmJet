package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWebhookAuthenticationMethodAndBodyLimit(t *testing.T) {
	for _, test := range []struct {
		name, secret, token, method string
		size, status                int
	}{
		{"authenticated", "shared-secret", "shared-secret", "POST", 16, http.StatusAccepted},
		{"missing-token", "shared-secret", "", "POST", 16, http.StatusUnauthorized},
		{"wrong-token", "shared-secret", "different", "POST", 16, http.StatusUnauthorized},
		{"disabled-secret", "", "", "POST", 16, http.StatusUnauthorized},
		{"wrong-method", "shared-secret", "shared-secret", "GET", 0, http.StatusMethodNotAllowed},
		{"exact-limit", "shared-secret", "shared-secret", "POST", 1 << 20, http.StatusAccepted},
		{"oversized", "shared-secret", "shared-secret", "POST", (1 << 20) + 1, http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{Wake: make(chan struct{}, 1)}
			request := httptest.NewRequest(test.method, "/webhooks/gitlab", strings.NewReader(strings.Repeat("x", test.size)))
			request.Header.Set("X-Gitlab-Token", test.token)
			response := httptest.NewRecorder()
			service.Webhook(test.secret).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			wantWake := 0
			if test.status == http.StatusAccepted {
				wantWake = 1
			}
			if len(service.Wake) != wantWake {
				t.Fatalf("wake signalled before validation: %d", len(service.Wake))
			}
		})
	}
}
func TestWebhookCoalescesConcurrentNotificationsWithoutBlocking(t *testing.T) {
	service := &Service{Wake: make(chan struct{}, 1)}
	server := httptest.NewServer(service.Webhook("token"))
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			req, err := http.NewRequest("POST", server.URL, strings.NewReader(`{"object_kind":"issue"}`))
			if err != nil {
				t.Error(err)
				return
			}
			req.Header.Set("X-Gitlab-Token", "token")
			resp, err := client.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusAccepted {
				t.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	workers.Wait()
	if len(service.Wake) != 1 {
		t.Fatalf("expected one coalesced notification, got %d", len(service.Wake))
	}
	<-service.Wake
	req := httptest.NewRequest("POST", "/webhooks/gitlab", nil)
	req.Header.Set("X-Gitlab-Token", "token")
	response := httptest.NewRecorder()
	service.Webhook("token").ServeHTTP(response, req)
	if response.Code != http.StatusAccepted || len(service.Wake) != 1 {
		t.Fatal("next event did not wake after queue drained")
	}
}
func TestWebhookNilWakeDoesNotBlock(t *testing.T) {
	service := &Service{}
	request := httptest.NewRequest("POST", "/webhooks/gitlab", nil)
	request.Header.Set("X-Gitlab-Token", "token")
	response := httptest.NewRecorder()
	service.Webhook("token").ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d", response.Code)
	}
}
