package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestCreatePRCommentPostsToIssuesEndpoint(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/repos/owner/repo/issues/3/comments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Body != "attestation body" {
			t.Errorf("body = %q", body.Body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/owner/repo/pull/3#issuecomment-9"}`))
	})

	commentURL, err := c.CreatePRComment(context.Background(), 1, 3, "attestation body")
	if err != nil {
		t.Fatalf("CreatePRComment: %v", err)
	}
	if commentURL != "https://github.com/owner/repo/pull/3#issuecomment-9" {
		t.Fatalf("url = %q", commentURL)
	}
}

func TestCreatePRCommentRejectsIncompleteInput(t *testing.T) {
	c := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("request should not be sent")
	})
	if _, err := c.CreatePRComment(context.Background(), 1, 0, "body"); err == nil {
		t.Fatal("zero PR number was accepted")
	}
	if _, err := c.CreatePRComment(context.Background(), 1, 3, "  "); err == nil {
		t.Fatal("empty body was accepted")
	}
}

func TestGetPullStateReadsDraftAndMergeability(t *testing.T) {
	mergeable := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls/7" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 7, "state": "open", "draft": true, "merged": false,
			"title": "Implement change", "updated_at": "2026-10-20T12:00:00Z", "closed_at": "2026-10-19T12:00:00Z",
			"html_url": "https://github.com/owner/repo/pull/7", "mergeable": mergeable,
			"mergeable_state": "dirty",
			"head":            map[string]any{"sha": "head", "ref": "feat"},
			"base":            map[string]any{"ref": "dev", "repo": map[string]any{"full_name": "owner/repo"}},
		})
	})
	pull, err := c.GetPullState(context.Background(), 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	if pull.Title != "Implement change" || pull.UpdatedAt.Format("2006-01-02") != "2026-10-20" || pull.ClosedAt.Format("2006-01-02") != "2026-10-19" {
		t.Fatalf("metadata = %+v", pull)
	}
	if !pull.Draft || pull.HeadSHA != "head" || pull.BaseRef != "dev" ||
		pull.BaseRepository != "owner/repo" || pull.Mergeable == nil || *pull.Mergeable {
		t.Fatalf("pull = %+v", pull)
	}
}

func TestCheckRunsFollowsPagination(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/commits/head/check-runs") || r.URL.Query().Get("filter") != "latest" {
			t.Errorf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<https://api.github.com/next>; rel="next"`)
			fmt.Fprint(w, `{"check_runs":[{"id":1,"name":"build","head_sha":"head","status":"completed","conclusion":"success","output":{"summary":"ok"}}]}`)
			return
		}
		fmt.Fprint(w, `{"check_runs":[{"id":2,"name":"lint","head_sha":"head","status":"completed","conclusion":"failure"}]}`)
	})
	runs, err := c.CheckRuns(context.Background(), 1, "head")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(runs) != 2 || runs[1].Conclusion != "failure" {
		t.Fatalf("calls=%d runs=%+v", calls, runs)
	}
}
