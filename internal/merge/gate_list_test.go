package merge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

func TestCheckListedRechecksChangedBaseBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/pulls/7"):
			fmt.Fprint(w, `{"number":7,"state":"open","mergeable":true,"mergeable_state":"clean","head":{"sha":"head"},"base":{"ref":"new","repo":{"full_name":"acme/widgets"}}}`)
		case strings.Contains(p, "/branches/old/protection/"):
			fmt.Fprint(w, `{"checks":[{"context":"old-only"}]}`)
		case strings.Contains(p, "/branches/new/protection/"):
			fmt.Fprint(w, `{"checks":[{"context":"test"}]}`)
		case strings.Contains(p, "/rules/branches/"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(p, "/statuses"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(p, "/check-runs"):
			fmt.Fprint(w, `{"check_runs":[{"id":1,"name":"test","head_sha":"head","status":"completed","conclusion":"success","app":{"id":1}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := github.NewWithBaseURL(func(context.Context) (string, error) { return "test-token", nil }, server.URL)
	client.SetRepositoryName(1, "acme/widgets")
	svc := New(Options{Store: &fakeStore{hive: testHive()}, Attestations: &fakeAttestations{}, GitHub: StaticSource{Client: client, RepositoryID: 1}})
	ready, err := svc.CheckListed(context.Background(), testHive(), 7, github.PullState{Number: 7, State: "open", HeadSHA: "head", BaseRef: "old"})
	if err != nil || !ready.Ready {
		t.Fatalf("changed base readiness=%+v err=%v", ready, err)
	}
}

func TestCheckListedCIErrorMatchesLivePolicy(t *testing.T) {
	for _, requireChecks := range []bool{false, true} {
		stub := readyStub()
		stub.checkStatus = 500
		policy := domain.MergePolicy{RequireChecks: requireChecks}
		svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{}, methodologyWith(policy), nil)
		listed := github.PullState{Number: 7, State: "open", HeadSHA: "head", BaseRef: "main"}
		live, liveErr := svc.Check(context.Background(), testHive(), 7)
		fast, fastErr := svc.CheckListed(context.Background(), testHive(), 7, listed)
		if (liveErr == nil) != (fastErr == nil) || live.Ready != fast.Ready || strings.Join(live.Reasons, ";") != strings.Join(fast.Reasons, ";") {
			t.Fatalf("requireChecks=%t live=%+v err=%v listed=%+v err=%v", requireChecks, live, liveErr, fast, fastErr)
		}
	}
}
