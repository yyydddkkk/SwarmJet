package gitlab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"swarmjet/internal/domain"
)

func TestListIssueLabelEventsPaginatesAndDecodesActor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/7/issues/3/resource_label_events" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			_, _ = io.WriteString(w, `[{"id":12,"action":"remove","label":{"name":"autonomous"},"user":{"id":4}}]`)
			return
		}
		_, _ = io.WriteString(w, `[{"id":13,"action":"add","label":{"name":"autonomous"},"user":{"id":42}}]`)
	}))
	defer server.Close()
	events, err := New(server.URL, "token").ListIssueLabelEvents(context.Background(), domain.Issue{ProjectID: 7, IID: 3})
	if err != nil || len(events) != 2 || events[1].User.ID != 42 || events[1].ID != 13 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
