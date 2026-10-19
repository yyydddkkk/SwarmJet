package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

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
