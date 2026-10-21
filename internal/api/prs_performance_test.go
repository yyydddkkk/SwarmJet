package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

func TestPRListColdAndWarmWithDelayedGitHub(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "bulk", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	for n := int64(1); n <= 18; n++ {
		_, _, err = s.RecordAttestation(ctx, domain.Attestation{HiveID: hive.ID, PRNumber: n, HeadSHA: fmt.Sprintf("head-%d", n), AttesterID: "verifier", Result: domain.AttestPass, Summary: "ok"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	counts := map[string]int{}
	now := time.Now().UTC().Truncate(time.Second)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()
		time.Sleep(time.Second)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/acme/widgets/pulls" && r.URL.Query().Get("state") == "open":
			fmt.Fprint(w, "[")
			for n := int64(16); n <= 18; n++ {
				if n > 16 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"number":%d,"title":"open %d","state":"open","html_url":"https://github.test/pr/%d","updated_at":%q,"head":{"sha":%q},"base":{"ref":"main"}}`, n, n, n, now.Format(time.RFC3339), fmt.Sprintf("head-%d", n))
			}
			fmt.Fprint(w, "]")
		case r.URL.Path == "/repos/acme/widgets/pulls" && r.URL.Query().Get("state") == "closed":
			fmt.Fprint(w, "[")
			for n := int64(1); n <= 15; n++ {
				if n > 1 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"number":%d,"title":"closed %d","state":"closed","html_url":"https://github.test/pr/%d","updated_at":%q,"closed_at":%q,"merged_at":%q,"head":{"sha":%q}}`, n, n, n, now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339), fmt.Sprintf("head-%d", n))
			}
			fmt.Fprint(w, "]")
		case strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/pulls/"):
			n, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/acme/widgets/pulls/"), 10, 64)
			fmt.Fprintf(w, `{"number":%d,"title":"open %d","state":"open","mergeable":true,"mergeable_state":"clean","html_url":"https://github.test/pr/%d","updated_at":%q,"head":{"sha":%q},"base":{"ref":"main","repo":{"full_name":"acme/widgets"}}}`, n, n, n, now.Format(time.RFC3339), fmt.Sprintf("head-%d", n))
		case strings.Contains(r.URL.Path, "/protection/required_status_checks"):
			fmt.Fprint(w, `{"checks":[{"context":"test"}]}`)
		case strings.Contains(r.URL.Path, "/rules/branches/"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/statuses"):
			fmt.Fprint(w, `[{"context":"test","state":"success"}]`)
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			sha := strings.Split(r.URL.Path, "/")[5]
			fmt.Fprintf(w, `{"check_runs":[{"id":1,"name":"test","head_sha":%q,"status":"completed","conclusion":"success","app":{"id":1}}]}`, sha)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	client := github.NewWithBaseURL(func(context.Context) (string, error) { return "test-token", nil }, fake.URL)
	service := merge.New(merge.Options{Store: s, Attestations: s, GitHub: merge.StaticSource{Client: client, RepositoryID: 1}})
	a := &server{s: s, cfg: Config{Username: "admin", Password: "secret", AllowedOrigin: "https://console.test", Merge: service}, attempts: map[string]attempt{}}
	handler := http.HandlerFunc(a.serve)
	cookie := hiveTestLogin(t, handler)
	path := "/api/hives/" + hive.ID + "/prs?state=all"
	start := time.Now()
	response := hiveDecode[struct {
		PRs []prListItem `json:"prs"`
	}](t, hiveDo(t, handler, cookie, http.MethodGet, path, nil), 200)
	cold := time.Since(start)
	if len(response.PRs) != 18 {
		t.Fatalf("cold list has %d PRs", len(response.PRs))
	}
	for _, item := range response.PRs {
		if item.State == "open" && !item.Ready {
			t.Fatalf("open PR %d not ready: %v", item.Number, item.Reasons)
		}
	}
	mu.Lock()
	coldRequests := 0
	for _, n := range counts {
		coldRequests += n
	}
	mu.Unlock()
	start = time.Now()
	hot := hiveDecode[struct {
		PRs []prListItem `json:"prs"`
	}](t, hiveDo(t, handler, cookie, http.MethodGet, path, nil), 200)
	warm := time.Since(start)
	mu.Lock()
	warmRequests := 0
	for _, n := range counts {
		warmRequests += n
	}
	mu.Unlock()
	t.Logf("cold=%s requests=%d; warm=%s additionalRequests=%d; history=15 open=3 delay=1s/request", cold, coldRequests, warm, warmRequests-coldRequests)
	if len(hot.PRs) != 18 || cold > 3*time.Second || warm > 300*time.Millisecond || warmRequests != coldRequests {
		t.Fatalf("performance contract: cold=%s warm=%s requests=%d/%d", cold, warm, coldRequests, warmRequests)
	}
}
