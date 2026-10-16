package runtimeadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNetworkCheckWorksBeforeImageIsReady(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	handler := New(Config{DataDir: t.TempDir(), Secret: "node-secret",
		DockerCommand: func(context.Context, []string, []string) ([]byte, error) { return nil, errors.New("image unavailable") }})
	req := httptest.NewRequest(http.MethodPost, "/network-test", strings.NewReader(`{"target":"`+target.URL+`"}`))
	req.Header.Set("X-SwarmJet-Runtime-Secret", "node-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("network check blocked by missing image: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Checks []struct {
			Scope  string `json:"scope"`
			Status int    `json:"status"`
			Error  string `json:"error"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 2 || result.Checks[0].Status != http.StatusNoContent || result.Checks[1].Error != "节点镜像不可用" {
		t.Fatalf("unexpected checks: %+v", result.Checks)
	}
}
