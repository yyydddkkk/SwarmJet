package labels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarmjet/internal/gitlab"
)

func TestEnsurePreservesExistingAndCreatesOnlyMissing(t *testing.T) {
	created := []gitlab.ProjectLabel{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/7/labels" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode([]gitlab.ProjectLabel{{Name: "autonomous", Color: "#111111"}})
			return
		}
		var label gitlab.ProjectLabel
		if err := json.NewDecoder(r.Body).Decode(&label); err != nil {
			t.Error(err)
		}
		created = append(created, label)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	wanted := Selection([]string{"codex"})
	status, err := Ensure(context.Background(), gitlab.New(server.URL, "token"), 7, wanted)
	if err != nil || len(created) != 1 || created[0] != wanted[1] || !status[0].Exists || status[0].ExistingColor != "#111111" || !status[1].Exists {
		t.Fatalf("status=%+v created=%+v err=%v", status, created, err)
	}
}

func TestEnsureReportsPermissionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	if _, err := Ensure(context.Background(), gitlab.New(server.URL, "token"), 7, Selection(nil)); err == nil {
		t.Fatal("permission failure was hidden")
	}
}

func TestEnsureAcceptsConcurrentCreation(t *testing.T) {
	listed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			listed = true
			w.WriteHeader(http.StatusConflict)
			return
		}
		if listed {
			w.Write([]byte(`[{"name":"autonomous","color":"#AABBCC"}]`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()
	status, err := Ensure(context.Background(), gitlab.New(server.URL, "token"), 7, Selection(nil))
	if err != nil || len(status) != 1 || !status[0].Exists || status[0].ExistingColor != "#AABBCC" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestSystemLabelsHaveIntentionalColors(t *testing.T) {
	blocked := System("blocked", true)
	if len(blocked) != 2 || blocked[0].Color != "#DC2626" || blocked[1].Name != "needs-clarification" {
		t.Fatalf("unexpected system labels %+v", blocked)
	}
}

func TestPlanReportsArchivedLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"agent:codex","color":"#AABBCC","archived":true}]`))
	}))
	defer server.Close()
	status, err := Plan(context.Background(), gitlab.New(server.URL, "token"), 7, Selection([]string{"codex"}))
	if err != nil || len(status) != 2 || !status[1].Exists || !status[1].Archived {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
