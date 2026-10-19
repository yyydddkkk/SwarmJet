package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

type fakeMergeStatus struct {
	readiness merge.Readiness
	err       error
	verdicts  []domain.Verdict
}

func (f fakeMergeStatus) Check(context.Context, domain.Hive, int64) (merge.Readiness, error) {
	return f.readiness, f.err
}

func (f fakeMergeStatus) Verdicts(context.Context, string, int64) ([]domain.Verdict, error) {
	return f.verdicts, nil
}

type verifyResponse struct {
	Hive   domain.Hive         `json:"hive"`
	Verify domain.VerifyConfig `json:"verify"`
}

func TestHiveVerifySettingsEndpoints(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	hive, err := s.CreateHive(context.Background(), domain.Hive{Name: "alpha", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}

	updated := hiveDecode[verifyResponse](t, hiveDo(t, handler, cookie, http.MethodPatch,
		"/api/hives/"+hive.ID+"/verify", map[string]any{
			"mode": "on", "model": "review-model", "maxRounds": 2,
			"autoMerge": true, "mergeMethod": "rebase",
		}), http.StatusOK)
	if updated.Verify.Mode != "on" || updated.Verify.Model != "review-model" ||
		updated.Verify.MaxRounds != 2 || !updated.Verify.AutoMerge || updated.Verify.MergeMethod != "rebase" {
		t.Fatalf("verify = %+v", updated.Verify)
	}

	got := hiveDecode[verifyResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/verify", nil), http.StatusOK)
	if got.Verify != updated.Verify {
		t.Fatalf("stored verify = %+v, want %+v", got.Verify, updated.Verify)
	}

	bad := hiveDo(t, handler, cookie, http.MethodPatch,
		"/api/hives/"+hive.ID+"/verify", map[string]any{"mode": "maybe"})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid mode status = %d: %s", bad.Code, bad.Body.String())
	}
	bad = hiveDo(t, handler, cookie, http.MethodPatch,
		"/api/hives/"+hive.ID+"/verify", map[string]any{"mergeMethod": "octopus"})
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid merge method status = %d", bad.Code)
	}
}

func TestForagerMergeEndpoint(t *testing.T) {
	status := fakeMergeStatus{
		readiness: merge.Readiness{
			Ready: true, HeadSHA: "abcdef0123456789",
			Pull:    merge.Pull{Number: 7, State: "open", URL: "https://github.com/acme/widgets/pull/7"},
			Checks:  []merge.Check{{Name: "test", Status: "completed", Conclusion: "success"}},
			Reasons: []string{},
		},
		verdicts: []domain.Verdict{{
			ID: "v1", HiveID: "x", PRNumber: 7, HeadSHA: "abcdef0123456789",
			Result: domain.VerdictPass, Summary: "ok",
		}},
	}
	s, handler, cookie := newHiveAPIWithMerge(t, status)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	forager, err := s.CreateForager(ctx, domain.Agent{
		HiveID: hive.ID, Role: domain.RoleForager, PRNumber: 7,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := hiveDecode[foragerMergeResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/foragers/"+forager.ID+"/merge", nil), http.StatusOK)
	if got.PRNumber != 7 || got.Readiness == nil || !got.Readiness.Ready {
		t.Fatalf("response = %+v", got)
	}
	if len(got.Verdicts) != 1 || got.Verdicts[0].Result != domain.VerdictPass {
		t.Fatalf("verdicts = %+v", got.Verdicts)
	}

	// A Forager without a PR still returns an honest empty result.
	other, err := s.CreateForager(ctx, domain.Agent{HiveID: hive.ID, Role: domain.RoleForager})
	if err != nil {
		t.Fatal(err)
	}
	empty := hiveDecode[foragerMergeResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/foragers/"+other.ID+"/merge", nil), http.StatusOK)
	if empty.PRNumber != 0 || empty.Readiness != nil || len(empty.Verdicts) != 0 {
		t.Fatalf("empty response = %+v", empty)
	}
}

func TestForagerMergeRequiresAdministratorSession(t *testing.T) {
	_, handler, _ := newHiveAPITest(t)
	w := hiveDo(t, handler, nil, http.MethodGet, "/api/foragers/whatever/merge", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
