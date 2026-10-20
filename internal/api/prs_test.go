package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

type fakePRMergeStatus struct {
	readiness    merge.Readiness
	checkErr     error
	attestations []domain.Attestation
}

func (f fakePRMergeStatus) Check(context.Context, domain.Hive, int64) (merge.Readiness, error) {
	return f.readiness, f.checkErr
}

func (f fakePRMergeStatus) Attestations(context.Context, string, int64) ([]domain.Attestation, error) {
	return f.attestations, nil
}

func TestPullRequestStatusEndpoint(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	status := fakePRMergeStatus{
		readiness: merge.Readiness{
			Ready:   true,
			HeadSHA: "abcdef0123456789",
			Pull: merge.Pull{
				Number: 7, State: "open", HeadSHA: "abcdef0123456789",
				URL: "https://github.com/acme/widgets/pull/7",
			},
			Checks:  []merge.Check{{Name: "test", Status: "completed", Conclusion: "success"}},
			Reasons: []string{},
		},
		attestations: []domain.Attestation{{
			ID: "att-1", HiveID: hive.ID, PRNumber: 7, HeadSHA: "abcdef0123456789",
			AttesterID: "verifier-1", AuthorID: "forager-1", Result: domain.AttestPass, Summary: "ok",
		}},
	}
	handler := New(s, Config{
		Username: "admin", Password: "secret",
		AllowedOrigin: "https://console.test", SecureCookies: true, Merge: status,
	})
	cookie := hiveTestLogin(t, handler)

	got := hiveDecode[prStatusResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/prs/7", nil), http.StatusOK)
	if !got.Ready || got.PRNumber != 7 || got.HeadSHA != "abcdef0123456789" || got.State != "open" {
		t.Fatalf("response = %+v", got)
	}
	if len(got.Checks) != 1 || len(got.Attestations) != 1 || got.Attestations[0].Result != domain.AttestPass {
		t.Fatalf("checks=%+v attestations=%+v", got.Checks, got.Attestations)
	}

	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+hive.ID+"/prs/7", map[string]any{}); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/missing/prs/7", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing Hive status = %d, want 404", w.Code)
	}
	if w := hiveDo(t, handler, nil, http.MethodGet, "/api/hives/"+hive.ID+"/prs/7", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/prs/zero", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid PR status = %d, want 400", w.Code)
	}
}

func TestPullRequestStatusUnavailableWithoutMergeService(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/prs/7", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestPullRequestStatusReportsGitHubFailure(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(s, Config{
		Username: "admin", Password: "secret",
		AllowedOrigin: "https://console.test", SecureCookies: true,
		Merge: fakePRMergeStatus{checkErr: errors.New("GitHub unavailable")},
	})
	cookie := hiveTestLogin(t, handler)
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/prs/7", nil); w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
}

type webPRFake struct {
	fakePRMergeStatus
	statuses   map[int64]merge.Readiness
	calls      int
	mergeCalls int
	outcome    merge.Outcome
	mergeErr   error
}

func (f *webPRFake) Check(_ context.Context, _ domain.Hive, pr int64) (merge.Readiness, error) {
	f.calls++
	return f.statuses[pr], nil
}
func (f *webPRFake) MergeHead(_ context.Context, _ domain.Hive, _ int64, head string) (merge.Outcome, error) {
	f.mergeCalls++
	if head != "head" {
		return merge.Outcome{}, &merge.HeadChangedError{HeadSHA: "head"}
	}
	return f.outcome, f.mergeErr
}

func TestProjectPRListAndMerge(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.CreateForager(ctx, domain.Agent{HiveID: hive.ID, Task: "work", Branch: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAgentPR(ctx, owner.ID, 7); err != nil {
		t.Fatal(err)
	}
	// Attestations alone are a supported source; ownership survives terminal agents.
	for _, pr := range []int64{7, 8, 9} {
		_, _, err = s.RecordAttestation(ctx, domain.Attestation{HiveID: hive.ID, PRNumber: pr, HeadSHA: "head", AttesterID: "verifier", Result: domain.AttestPass, Summary: "ok"})
		if err != nil {
			t.Fatal(err)
		}
	}
	f := &webPRFake{statuses: map[int64]merge.Readiness{}, outcome: merge.Outcome{Merged: true, HeadSHA: "head", Method: "squash"}}
	for _, pr := range []int64{7, 8, 9} {
		f.statuses[pr] = merge.Readiness{Ready: pr == 7, HeadSHA: "head", Pull: merge.Pull{Number: pr, State: "closed", Title: "work", UpdatedAt: time.Now().UTC(), ClosedAt: time.Now().UTC()}}
	}
	open := f.statuses[7]
	open.Pull.State = "open"
	f.statuses[7] = open
	old := f.statuses[9]
	old.Pull.ClosedAt = time.Now().Add(-8 * 24 * time.Hour)
	f.statuses[9] = old
	handler := New(s, Config{Username: "admin", Password: "secret", AllowedOrigin: "https://console.test", Merge: f})
	cookie := hiveTestLogin(t, handler)
	path := "/api/hives/" + hive.ID + "/prs"
	list := hiveDecode[struct {
		PRs []prListItem `json:"prs"`
	}](t, hiveDo(t, handler, cookie, http.MethodGet, path, nil), 200)
	if len(list.PRs) != 1 || list.PRs[0].Number != 7 || list.PRs[0].Title != "work" || list.PRs[0].OwnerID != owner.ID {
		t.Fatalf("open = %+v", list)
	}
	list = hiveDecode[struct {
		PRs []prListItem `json:"prs"`
	}](t, hiveDo(t, handler, cookie, http.MethodGet, path+"?state=all", nil), 200)
	if len(list.PRs) != 2 || f.calls != 3 {
		t.Fatalf("all=%+v checks=%d", list, f.calls)
	}
	for _, tc := range []struct {
		path string
		code int
	}{{path + "?state=bad", 400}, {"/api/hives/missing/prs", 404}} {
		if w := hiveDo(t, handler, cookie, http.MethodGet, tc.path, nil); w.Code != tc.code {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
	if w := hiveDo(t, handler, nil, http.MethodGet, path, nil); w.Code != 401 {
		t.Fatalf("anonymous list=%d", w.Code)
	}
	mergePath := path + "/7/merge"
	got := hiveDecode[webMergeResponse](t, hiveDo(t, handler, cookie, http.MethodPost, mergePath, map[string]any{"headSha": "head"}), 200)
	if !got.Merged || !got.Ready || got.Method != "squash" {
		t.Fatalf("merge=%+v", got)
	}
	var actor, details string
	if err = s.Pool.QueryRow(ctx, `SELECT actor_type,details->>'administrator' FROM audit_events WHERE action='pr.merge.request' ORDER BY id DESC LIMIT 1`).Scan(&actor, &details); err != nil || actor != "web_administrator" || details != "admin" {
		t.Fatalf("audit=%s %s %v", actor, details, err)
	}
	f.mergeErr = &merge.NotReadyError{Readiness: merge.Readiness{HeadSHA: "head", Reasons: []string{"CI is not green"}}}
	got = hiveDecode[webMergeResponse](t, hiveDo(t, handler, cookie, http.MethodPost, mergePath, map[string]any{"headSha": "head"}), 200)
	if got.Merged || got.Ready || len(got.Reasons) != 1 {
		t.Fatalf("blocked=%+v", got)
	}
	for _, tc := range []struct {
		head string
		code int
	}{{"old", 409}, {"", 400}} {
		if w := hiveDo(t, handler, cookie, http.MethodPost, mergePath, map[string]any{"headSha": tc.head}); w.Code != tc.code {
			t.Fatalf("head=%s status=%d", tc.head, w.Code)
		}
	}
	if w := hiveDo(t, handler, nil, http.MethodPost, mergePath, map[string]any{"headSha": "head"}); w.Code != 401 {
		t.Fatalf("anonymous merge=%d", w.Code)
	}
	req := httptest.NewRequest(http.MethodPost, mergePath, strings.NewReader(`{"headSha":"head"}`))
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("CSRF=%d", w.Code)
	}
}

func TestPRCacheExpiresAndSeparatesHeads(t *testing.T) {
	fake := &webPRFake{statuses: map[int64]merge.Readiness{7: {Ready: true, HeadSHA: "head"}}}
	a := &server{cfg: Config{Merge: fake}}
	hive := domain.Hive{ID: "hive", Repository: "acme/widgets"}
	for range 2 {
		if _, err := a.cachedPR(context.Background(), hive, 7, "head"); err != nil {
			t.Fatal(err)
		}
	}
	if fake.calls != 1 {
		t.Fatalf("cache hit made %d checks", fake.calls)
	}
	if _, err := a.cachedPR(context.Background(), hive, 7, "new-head"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Fatal("attested head change did not invalidate cache")
	}
	for key, entry := range a.prCache {
		entry.expires = time.Now().Add(-time.Second)
		a.prCache[key] = entry
	}
	if _, err := a.cachedPR(context.Background(), hive, 7, "new-head"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 3 {
		t.Fatal("expired status was reused")
	}
}
