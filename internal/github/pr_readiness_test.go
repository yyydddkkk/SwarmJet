package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequiredChecksSnakeCaseRunMatchesListedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/pulls/7":
			fmt.Fprint(w, `{"head":{"sha":"head"},"base":{"ref":"main"}}`)
		case "/repos/acme/widgets/branches/main/protection/required_status_checks":
			fmt.Fprint(w, `{"checks":[{"context":"test"}]}`)
		case "/repos/acme/widgets/rules/branches/main":
			fmt.Fprint(w, `[]`)
		case "/repos/acme/widgets/commits/head/statuses":
			fmt.Fprint(w, `[]`)
		case "/repos/acme/widgets/commits/head/check-runs":
			fmt.Fprint(w, `{"check_runs":[{"id":1,"name":"test","head_sha":"head","status":"completed","conclusion":"success","app":{"id":1}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewWithBaseURL(func(context.Context) (string, error) { return "test-token", nil }, server.URL)
	client.SetRepositoryName(1, "acme/widgets")
	live, err := client.RequiredChecks(context.Background(), 1, 7, "head")
	if err != nil || !live.Ready {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	listed, runs, err := client.ReadListedChecks(context.Background(), 1, "main", "head")
	if err != nil || !listed.Ready || len(runs) != 1 || runs[0].HeadSHA != "head" {
		t.Fatalf("listed=%+v runs=%+v err=%v", listed, runs, err)
	}
}
