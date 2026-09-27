package k8srunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsurePVCRejectsMismatchedClaim(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusConflict)
			return
		}
		posted["spec"].(map[string]any)["accessModes"] = []string{"ReadWriteMany"}
		json.NewEncoder(w).Encode(posted)
	}))
	defer srv.Close()
	c, _ := NewController(srv.Client(), srv.URL, "runners", StaticFence(true))
	if _, err := c.EnsurePVC(context.Background(), "task-a", 64); err == nil {
		t.Fatal("accepted incompatible existing PVC")
	}
}

func TestReadPodLogBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/namespaces/runners/pods" {
			w.Write([]byte(`{"items":[{"metadata":{"name":"pod-a","uid":"uid-a"},"status":{"phase":"Succeeded"}}]}`))
			return
		}
		if r.URL.Path == "/api/v1/namespaces/runners/pods/pod-a/log" {
			if r.URL.Query().Get("limitBytes") != "262144" {
				t.Errorf("missing bound")
			}
			w.Write([]byte(`{"status":"succeeded"}`))
			return
		}
		t.Errorf("unexpected path: %s", r.URL.Path)
	}))
	defer srv.Close()
	c, _ := NewController(srv.Client(), srv.URL, "runners", StaticFence(true))
	data, err := c.ReadPodLog(context.Background(), "exec-a", 1)
	if err != nil || string(data) != `{"status":"succeeded"}` {
		t.Fatalf("log=%s err=%v", data, err)
	}
}
