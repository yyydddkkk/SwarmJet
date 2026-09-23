package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"swarmjet/internal/domain"
	"testing"
)

func server(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return New(s.URL, "test-token")
}
func TestPaginatedIssuesAndLinks(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("missing scoped token")
		}
		if strings.HasSuffix(r.URL.Path, "/links") {
			fmt.Fprint(w, `[{"project_id":2,"iid":3,"link_type":"is_blocked_by"},{"project_id":4,"iid":5,"link_type":"relates_to"},{"project_id":6,"iid":7,"link_type":"blocks"}]`)
			return
		}
		if r.URL.Query().Get("labels") != "autonomous" || r.URL.Query().Get("state") != "opened" {
			t.Error("wrong eligibility query")
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"iid":1,"web_url":"url","labels":["autonomous"]}]`)
		} else {
			fmt.Fprint(w, `[{"iid":2,"project_id":9}]`)
		}
	})
	issues, err := c.ListEligibleIssues(context.Background(), 9)
	if err != nil || len(issues) != 2 || issues[0].ProjectID != 9 || issues[0].URL != "url" {
		t.Fatalf("%+v %v", issues, err)
	}
	refs, err := c.Dependencies(context.Background(), issues[0])
	if err != nil || len(refs) != 1 || refs[0].ProjectID != 2 {
		t.Fatalf("%+v %v", refs, err)
	}
}
func TestEnsureTargetOnly404Creates(t *testing.T) {
	for _, code := range []int{401, 403, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			c := server(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(code) })
			_, err := c.EnsureTarget(context.Background(), domain.Repository{ProjectID: 1, TargetBranch: "release", CreateFrom: "main"})
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestEnsureTargetConcurrentCreate(t *testing.T) {
	for _, sha := range []string{"base", "wrong"} {
		t.Run(sha, func(t *testing.T) {
			gets := 0
			c := server(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/commits/"):
					fmt.Fprint(w, `{"id":"base"}`)
				case r.Method == "POST":
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if body["ref"] != "base" {
						t.Error("creation not pinned")
					}
					w.WriteHeader(400)
				case r.Method == "GET":
					gets++
					if gets == 1 {
						w.WriteHeader(404)
					} else {
						fmt.Fprintf(w, `{"name":"release","commit":{"id":%q}}`, sha)
					}
				}
			})
			got, err := c.EnsureTarget(context.Background(), domain.Repository{ProjectID: 1, TargetBranch: "release", CreateFrom: "main"})
			if sha == "base" && (err != nil || got != "base") {
				t.Fatalf("%q %v", got, err)
			}
			if sha == "wrong" && err == nil {
				t.Fatal("accepted competing branch at wrong SHA")
			}
		})
	}
}
func TestCIExactSHA(t *testing.T) {
	for _, tc := range []struct {
		name, head, pipeline, status string
		required, want               bool
	}{{"correct", "a", "a", "success", true, true}, {"old pipeline", "a", "old", "success", true, false}, {"old MR", "old", "a", "success", false, false}, {"failed", "a", "a", "failed", true, false}, {"optional", "a", "", "", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := server(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"sha":%q,"head_pipeline":{"sha":%q,"status":%q}}`, tc.head, tc.pipeline, tc.status)
			})
			got, err := c.RequiredCIPassed(context.Background(), 1, 2, "a", tc.required)
			if err != nil || got != tc.want {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}
func TestMergeRejectsStaleRevisionAndSendsSHA(t *testing.T) {
	for _, head := range []string{"old", "a"} {
		t.Run(head, func(t *testing.T) {
			writes := 0
			c := server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					writes++
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["sha"] != "a" || body["auto_merge"] != false {
						t.Error("unsafe merge payload")
					}
					fmt.Fprint(w, `{}`)
					return
				}
				fmt.Fprintf(w, `{"sha":%q,"target_branch":"release","state":"opened","detailed_merge_status":"mergeable","head_pipeline":{"sha":"a","status":"success"}}`, head)
			})
			err := c.Merge(context.Background(), 1, 2, "a", "release")
			if head == "old" && (err == nil || writes != 0) {
				t.Fatal("stale merge accepted")
			}
			if head == "a" && (err != nil || writes != 1) {
				t.Fatalf("merge failed: %v", err)
			}
		})
	}
}
func TestProgressUpsertAndCloseAreIdempotent(t *testing.T) {
	var note string
	posts, puts := 0, 0
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/notes") {
			if r.Method == "GET" {
				if note == "" {
					fmt.Fprint(w, `[]`)
				} else {
					json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "body": note}})
				}
				return
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			note = body["body"]
			if r.Method == "POST" {
				posts++
			} else {
				puts++
			}
			fmt.Fprint(w, `{}`)
			return
		}
		if r.Method != "GET" {
			t.Error("closed issue mutated")
		}
		fmt.Fprint(w, `{"iid":2,"state":"closed"}`)
	})
	i := domain.Issue{ProjectID: 1, IID: 2}
	for _, body := range []string{"first", "first", "second", "second"} {
		if err := c.UpsertProgress(context.Background(), i, "task", body); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 1 || puts != 1 {
		t.Fatalf("posts=%d puts=%d", posts, puts)
	}
	if err := c.CloseIssue(context.Background(), i); err != nil {
		t.Fatal(err)
	}
}
func TestCancellationAndRedirect(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.invalid", 302) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetIssue(ctx, 1, 2); err == nil {
		t.Fatal("canceled request succeeded")
	}
	_, err := c.GetIssue(context.Background(), 1, 2)
	if e, ok := err.(*APIError); !ok || e.StatusCode != 302 {
		t.Fatalf("redirect was followed: %v", err)
	}
}

func TestEnsureTargetMissingCreateFromNeverCreates(t *testing.T) {
	calls := 0
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.EnsureTarget(context.Background(), domain.Repository{ProjectID: 1, TargetBranch: "release"})
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestMergeRejectsWrongTarget(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected merge")
		}
		fmt.Fprint(w, `{"sha":"a","target_branch":"main","state":"opened","detailed_merge_status":"mergeable","head_pipeline":{"sha":"a","status":"success"}}`)
	})
	if err := c.Merge(context.Background(), 1, 2, "a", "release"); err == nil {
		t.Fatal("wrong target accepted")
	}
}
