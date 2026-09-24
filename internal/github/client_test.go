package github

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

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c := New(func(context.Context) (string, error) { return "secret", nil })
	c.baseURL = s.URL
	c.http = s.Client()
	c.names.Store(int64(1), "owner/repo")
	return c
}
func TestIssuePaginationExcludesPullRequests(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing scoped auth")
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<https://evil.example/token>; rel="next"`)
			fmt.Fprint(w, `[{"number":1,"state":"open","pull_request":{}},{"number":2,"state":"open","labels":[{"name":"autonomous"}]}]`)
		} else {
			fmt.Fprint(w, `[{"number":3,"state":"open"}]`)
		}
	})
	issues, e := c.ListEligibleIssues(context.Background(), 1)
	if e != nil || len(issues) != 2 || issues[0].IID != 2 || issues[0].Provider != "github" || calls != 2 {
		t.Fatalf("issues=%+v calls=%d err=%v", issues, calls, e)
	}
}
func TestRequiredChecksCurrentSHAAndApp(t *testing.T) {
	for _, tt := range []struct {
		name, head, conclusion string
		app                    int64
		ready, failed          bool
	}{{"passed", "head", "success", 7, true, false}, {"wrong app", "head", "success", 8, false, false}, {"failed", "head", "failure", 7, false, true}, {"stale check", "old", "success", 7, false, false}} {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/pulls/3"):
					fmt.Fprint(w, `{"number":3,"head":{"sha":"head"},"base":{"ref":"main"}}`)
				case strings.HasSuffix(r.URL.Path, "/required_status_checks"):
					fmt.Fprint(w, `{"checks":[{"context":"test","app_id":7}],"contexts":["test"]}`)
				case strings.Contains(r.URL.Path, "/rules/branches/"):
					fmt.Fprint(w, `[]`)
				case strings.HasSuffix(r.URL.Path, "/statuses"):
					fmt.Fprint(w, `[]`)
				case strings.HasSuffix(r.URL.Path, "/check-runs"):
					fmt.Fprintf(w, `{"check_runs":[{"id":1,"name":"test","head_sha":%q,"status":"completed","conclusion":%q,"app":{"id":%d}}]}`, tt.head, tt.conclusion, tt.app)
				default:
					t.Error(r.URL.Path)
					w.WriteHeader(500)
				}
			})
			got, e := c.RequiredChecks(context.Background(), 1, 3, "head")
			if e != nil || got.Ready != tt.ready || got.Failed != tt.failed {
				t.Fatalf("%+v %v", got, e)
			}
		})
	}
}
func TestNoRequiredChecksCannotPass(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/pulls/"):
			fmt.Fprint(w, `{"head":{"sha":"head"},"base":{"ref":"main"}}`)
		case strings.Contains(r.URL.Path, "/protection/"):
			w.WriteHeader(404)
		default:
			fmt.Fprint(w, `[]`)
		}
	})
	r, e := c.RequiredChecks(context.Background(), 1, 3, "head")
	if e != nil || r.Ready || !r.Failed || r.RequiredCount != 0 {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestRequestMergeUsesExpectedSHA(t *testing.T) {
	merged := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/3/merge"):
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["sha"] != "head" || body["merge_method"] != "squash" {
				t.Errorf("bad merge body: %+v", body)
			}
			merged = true
			fmt.Fprint(w, `{"merged":true}`)
		case strings.HasSuffix(r.URL.Path, "/pulls/3"):
			fmt.Fprint(w, `{"number":3,"state":"open","head":{"sha":"head"},"base":{"ref":"main"}}`)
		case r.URL.Path == "/repositories/1":
			fmt.Fprint(w, `{"id":1,"full_name":"owner/repo","allow_squash_merge":true}`)
		case strings.Contains(r.URL.Path, "/protection/"):
			w.WriteHeader(404)
		default:
			fmt.Fprint(w, `[]`)
		}
	})
	r, e := c.RequestMerge(context.Background(), 1, 3, "head", "squash")
	if e != nil || !r.Merged || !merged {
		t.Fatalf("%+v %v", r, e)
	}
	if _, e = c.RequestMerge(context.Background(), 1, 3, "other", "squash"); e == nil {
		t.Fatal("accepted stale head")
	}
}
func TestMergeQueueIdempotency(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls/3"):
			fmt.Fprint(w, `{"number":3,"node_id":"PR_3","state":"open","head":{"sha":"head"},"base":{"ref":"main"}}`)
		case r.URL.Path == "/repositories/1":
			fmt.Fprint(w, `{"id":1,"full_name":"owner/repo","allow_merge_commit":true}`)
		case strings.Contains(r.URL.Path, "/protection/"):
			w.WriteHeader(404)
		case strings.Contains(r.URL.Path, "/rules/"):
			fmt.Fprint(w, `[{"type":"merge_queue"}]`)
		case r.URL.Path == "/graphql":
			var body struct {
				Query string `json:"query"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(body.Query, "mutation") {
				t.Fatal("existing queue entry was enqueued twice")
			}
			fmt.Fprint(w, `{"data":{"node":{"mergeQueueEntry":{"id":"entry"}}}}`)
		default:
			t.Fatal(r.URL.Path)
		}
	})
	r, e := c.RequestMerge(context.Background(), 1, 3, "head", "merge")
	if e != nil || !r.Queued || r.Merged || r.QueueEntryID != "entry" {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestTokensRefreshedForEveryOperation(t *testing.T) {
	count := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"number":1,"state":"open"}`) })
	c.token = func(context.Context) (string, error) { count++; return fmt.Sprint(count), nil }
	for range 2 {
		if _, e := c.GetIssue(context.Background(), 1, 1); e != nil {
			t.Fatal(e)
		}
	}
	if count != 2 {
		t.Fatalf("token calls %d", count)
	}
}

func TestCreateOrUpdatePRRecoversExisting(t *testing.T) {
	patches := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			if r.URL.Query().Get("head") != "owner:task" || r.URL.Query().Get("base") != "main" {
				t.Fatal(r.URL.String())
			}
			fmt.Fprint(w, `[{"number":8,"state":"open","head":{"ref":"task","sha":"head","repo":{"id":1}},"base":{"ref":"main"}}]`)
		case "PATCH":
			patches++
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["title"] != "title" || body["body"] != "body" {
				t.Fatalf("%+v", body)
			}
			fmt.Fprint(w, `{"number":8,"state":"open","head":{"ref":"task","sha":"head","repo":{"id":1}},"base":{"ref":"main"}}`)
		default:
			t.Fatalf("unexpected %s", r.Method)
		}
	})
	p, e := c.CreateOrUpdatePR(context.Background(), 1, "task", "main", "title", "body")
	if e != nil || p.IID != 8 || patches != 1 {
		t.Fatalf("%+v %v", p, e)
	}
}

func TestRemovedMergeQueueDoesNotReenqueue(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			var body struct {
				Query string `json:"query"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(body.Query, "mutation") {
				t.Fatal("poll mutated queue")
			}
			fmt.Fprint(w, `{"data":{"node":{"mergeQueueEntry":null}}}`)
		} else {
			fmt.Fprint(w, `{"number":3,"node_id":"PR_3","state":"open","head":{"sha":"head"}}`)
		}
	})
	r, e := c.GetMergeState(context.Background(), 1, 3, "head")
	if e != nil || r.Queued || r.Merged {
		t.Fatalf("%+v %v", r, e)
	}
}

func TestClosingIssuesIncludesCommitKeywords(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repositories/1":
			fmt.Fprint(w, `{"id":1,"full_name":"owner/repo"}`)
		case "/graphql":
			var body struct {
				Query string `json:"query"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(body.Query, "commits(") {
				fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"number":3,"commits":{"nodes":[{"commit":{"message":"Fixes #4\nResolves owner/other#7\nCloses https://github.com/owner/other/issues/9"}}],"pageInfo":{"hasNextPage":false}}}}}}`)
			} else {
				fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"number":3,"closingIssuesReferences":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`)
			}
		case "/repos/owner/other":
			fmt.Fprint(w, `{"id":2,"full_name":"owner/other"}`)
		default:
			t.Fatal(r.URL.Path)
		}
	})
	refs, e := c.ClosingIssues(context.Background(), 1, 3)
	if e != nil || len(refs) != 3 || refs[0].IID != 4 || refs[1].ProjectID != 2 || refs[2].IID != 9 {
		t.Fatalf("%+v %v", refs, e)
	}
}

func TestExternalDependencyUsesSeparateReadOnlyCredential(t *testing.T) {
	var minted []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/issues/1":
			fmt.Fprint(w, `{"number":1,"issue_dependencies_summary":{"blocked_by":1}}`)
		case "/repos/owner/repo/issues/1/dependencies/blocked_by":
			fmt.Fprint(w, `[{"number":8,"repository_url":"https://api.github.com/repos/owner/external"}]`)
		case "/repos/owner/external":
			if r.Header.Get("Authorization") == "Bearer secret" {
				w.WriteHeader(404)
				return
			}
			fmt.Fprint(w, `{"id":2,"full_name":"owner/external"}`)
		case "/repositories/2":
			if r.Header.Get("Authorization") != "Bearer read-only" {
				t.Error("dependency lookup used write credential")
			}
			fmt.Fprint(w, `{"id":2,"full_name":"owner/external"}`)
		case "/repos/owner/external/issues/8":
			if r.Header.Get("Authorization") == "Bearer secret" {
				w.WriteHeader(404)
				return
			}
			fmt.Fprint(w, `{"number":8,"state":"closed"}`)
		case "/repos/owner/repo/issues/1/labels":
			if r.Header.Get("Authorization") != "Bearer secret" {
				t.Error("write credential was replaced")
			}
			fmt.Fprint(w, `[]`)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(500)
		}
	})
	c.SetDependencyTokenSource(func(_ context.Context, id int64, name string) (string, error) {
		minted = append(minted, fmt.Sprintf("%d:%s", id, name))
		return "read-only", nil
	})
	issue := domain.Issue{Provider: domain.ProviderGitHub, ProjectID: 1, IID: 1}
	refs, err := c.Dependencies(context.Background(), issue)
	if err != nil || len(refs) != 1 || refs[0].ProjectID != 2 {
		t.Fatalf("%+v %v", refs, err)
	}
	dependency, err := c.GetIssue(context.Background(), 2, 8)
	if err != nil || dependency.State != "closed" {
		t.Fatalf("%+v %v", dependency, err)
	}
	if err = c.UpdateLabels(context.Background(), issue, []string{"ready"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(minted) < 2 || minted[0] != "0:owner/external" || minted[1] != "2:" {
		t.Fatalf("unexpected scopes: %v", minted)
	}
}

func TestExternalDependencyWithoutReaderFailsClearly(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/blocked_by") {
			fmt.Fprint(w, `[{"number":8,"repository_url":"https://api.github.com/repos/owner/external"}]`)
		} else if r.URL.Path == "/repos/owner/repo/issues/1" {
			fmt.Fprint(w, `{"number":1,"issue_dependencies_summary":{"blocked_by":1}}`)
		} else {
			w.WriteHeader(404)
		}
	})
	_, err := c.Dependencies(context.Background(), domain.Issue{ProjectID: 1, IID: 1})
	if err == nil || !strings.Contains(err.Error(), "read-only dependency access") {
		t.Fatalf("%v", err)
	}
}

func TestHiddenDependenciesFailClosed(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/blocked_by") {
			fmt.Fprint(w, `[]`)
		} else {
			fmt.Fprint(w, `{"number":1,"issue_dependencies_summary":{"blocked_by":1}}`)
		}
	})
	_, err := c.Dependencies(context.Background(), domain.Issue{ProjectID: 1, IID: 1})
	if err == nil || !strings.Contains(err.Error(), "dependency list is incomplete") {
		t.Fatalf("%v", err)
	}
}
