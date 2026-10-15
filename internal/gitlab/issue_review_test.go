package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestIssueReviewNoteSupersedesOldRevisionWithoutDuplication(t *testing.T) {
	var mu sync.Mutex
	notes := map[int64]string{}
	next := int64(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			list := []map[string]any{}
			for id := int64(1); id < next; id++ {
				list = append(list, map[string]any{"id": id, "body": notes[id]})
			}
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if r.Method == "POST" {
			id := next
			next++
			notes[id] = body["body"]
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if err != nil {
			t.Error(err)
			return
		}
		notes[id] = body["body"]
		_, _ = fmt.Fprint(w, "{}")
	}))
	defer server.Close()
	issue := domain.Issue{ProjectID: 1, IID: 2, URL: server.URL + "/issues/2"}
	client := New(server.URL, "token")
	first, err := client.UpsertIssueReview(context.Background(), issue, "task", "sha-a", "guide A")
	if err != nil || !strings.HasSuffix(first, "#note_1") {
		t.Fatalf("first=%q err=%v", first, err)
	}
	again, err := client.UpsertIssueReview(context.Background(), issue, "task", "sha-a", "guide A")
	if err != nil || again != first || next != 2 {
		t.Fatalf("duplicate note: %q %v %d", again, err, next)
	}
	second, err := client.UpsertIssueReview(context.Background(), issue, "task", "sha-b", "guide B")
	if err != nil || !strings.HasSuffix(second, "#note_2") || !strings.Contains(notes[1], "此版本已失效") || next != 3 {
		t.Fatalf("new note=%q notes=%v err=%v", second, notes, err)
	}
}
