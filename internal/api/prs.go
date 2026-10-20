package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

// MergeStatus is the readiness/attestation surface the console needs.
// *merge.Service implements it; tests use a fake.
type MergeStatus interface {
	Check(ctx context.Context, hive domain.Hive, pr int64) (merge.Readiness, error)
	Attestations(ctx context.Context, hiveID string, pr int64) ([]domain.Attestation, error)
}

type prStatusResponse struct {
	HiveID       string               `json:"hiveId"`
	PRNumber     int64                `json:"prNumber"`
	State        string               `json:"state,omitempty"`
	Draft        bool                 `json:"draft"`
	Merged       bool                 `json:"merged"`
	HeadSHA      string               `json:"headSha,omitempty"`
	URL          string               `json:"url,omitempty"`
	Checks       []merge.Check        `json:"checks"`
	Attestations []domain.Attestation `json:"attestations"`
	Ready        bool                 `json:"ready"`
	Reasons      []string             `json:"reasons"`
}

// pullRequestStatus handles GET /api/hives/{id}/prs/{number}. It returns false
// for every other path so the existing Hive router keeps owning them.
func (a *server) pullRequestStatus(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	if len(parts) != 4 || parts[0] != "hives" || parts[2] != "prs" {
		return false
	}
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}
	if a.s == nil {
		problem(w, http.StatusServiceUnavailable, "hive storage unavailable")
		return true
	}
	if a.cfg.Merge == nil {
		problem(w, http.StatusServiceUnavailable, "pull request status is unavailable")
		return true
	}
	pr, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || pr <= 0 {
		problem(w, http.StatusBadRequest, "invalid pull request number")
		return true
	}
	hive, err := a.s.Hive(r.Context(), parts[1])
	if err != nil {
		hiveFailure(w, err)
		return true
	}
	readiness, err := a.cfg.Merge.Check(r.Context(), hive, pr)
	if err != nil {
		problem(w, http.StatusBadGateway, "pull request status is unavailable: "+err.Error())
		return true
	}
	attestations, err := a.cfg.Merge.Attestations(r.Context(), hive.ID, pr)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		failure(w, err)
		return true
	}
	if attestations == nil {
		attestations = []domain.Attestation{}
	}
	checks := readiness.Checks
	if checks == nil {
		checks = []merge.Check{}
	}
	reasons := readiness.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	respond(w, http.StatusOK, prStatusResponse{
		HiveID:       hive.ID,
		PRNumber:     pr,
		State:        readiness.Pull.State,
		Draft:        readiness.Pull.Draft,
		Merged:       readiness.Pull.Merged,
		HeadSHA:      readiness.HeadSHA,
		URL:          readiness.Pull.URL,
		Checks:       checks,
		Attestations: attestations,
		Ready:        readiness.Ready,
		Reasons:      reasons,
	})
	return true
}
