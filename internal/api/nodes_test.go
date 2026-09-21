package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmjet/internal/store"
)

func TestNodeHeartbeatRejectsBadSecret(t *testing.T) {
	secret := strings.Repeat("s", 32)
	h := New(&store.Store{}, Config{NodeSecret: secret})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/nodes", strings.NewReader("{}"))
	r.Header.Set("X-SwarmJet-Node-Secret", "wrong")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestNodeHeartbeatDisabledWithoutSecret(t *testing.T) {
	h := New(&store.Store{}, Config{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/nodes", strings.NewReader("{}")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", w.Code)
	}
}
