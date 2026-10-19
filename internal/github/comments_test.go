package github

import (
	"context"
	"encoding/json"
	"net/http"
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
		if body.Body != "verdict body" {
			t.Errorf("body = %q", body.Body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/owner/repo/pull/3#issuecomment-9"}`))
	})

	url, err := c.CreatePRComment(context.Background(), 1, 3, "verdict body")
	if err != nil {
		t.Fatalf("CreatePRComment: %v", err)
	}
	if url != "https://github.com/owner/repo/pull/3#issuecomment-9" {
		t.Fatalf("url = %q", url)
	}
}

func TestCreatePRCommentRejectsIncompleteInput(t *testing.T) {
	c := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("request should not be sent")
	})
	if _, err := c.CreatePRComment(context.Background(), 1, 0, "body"); err == nil {
		t.Fatal("zero PR number was accepted")
	}
	if _, err := c.CreatePRComment(context.Background(), 1, 3, ""); err == nil {
		t.Fatal("empty body was accepted")
	}
}
