package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

// MergeStatus is the merge gate surface the console needs. *merge.Gate
// implements it; tests use a fake.
type MergeStatus interface {
	Check(ctx context.Context, hive domain.Hive, pr int64) (merge.Readiness, error)
	Verdicts(ctx context.Context, hiveID string, pr int64) ([]domain.Verdict, error)
}

// round4 routes the round-4 admin endpoints. It returns false for every other
// path so the existing Hive router keeps owning them.
func (a *server) round4(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "hives" && parts[2] == "verify":
		a.hiveVerify(w, r, parts[1])
		return true
	case len(parts) == 3 && parts[0] == "foragers" && parts[2] == "merge":
		a.foragerMerge(w, r, parts[1])
		return true
	default:
		return false
	}
}

// requireAdminSession keeps both round-4 endpoints to the console's
// administrator. The API already authenticates every request; the explicit
// check documents the stronger requirement and survives future session types.
func requireAdminSession(w http.ResponseWriter, r *http.Request) bool {
	principal := requestPrincipal(r)
	if principal.Breakglass || principal.PlatformAdmin {
		return true
	}
	problem(w, http.StatusForbidden, "administrator session required")
	return false
}

type verifyConfigPatch struct {
	Mode        *string `json:"mode"`
	Model       *string `json:"model"`
	MaxRounds   *int    `json:"maxRounds"`
	AutoMerge   *bool   `json:"autoMerge"`
	MergeMethod *string `json:"mergeMethod"`
}

func (p verifyConfigPatch) apply(v domain.VerifyConfig) domain.VerifyConfig {
	if p.Mode != nil {
		v.Mode = strings.TrimSpace(*p.Mode)
	}
	if p.Model != nil {
		v.Model = strings.TrimSpace(*p.Model)
	}
	if p.MaxRounds != nil {
		v.MaxRounds = *p.MaxRounds
	}
	if p.AutoMerge != nil {
		v.AutoMerge = *p.AutoMerge
	}
	if p.MergeMethod != nil {
		v.MergeMethod = strings.TrimSpace(*p.MergeMethod)
	}
	return v
}

func validateVerifyConfig(v domain.VerifyConfig) string {
	if v.Mode != "on" && v.Mode != "off" {
		return "mode must be on or off"
	}
	if v.MaxRounds < 0 || v.MaxRounds > 50 {
		return "maxRounds must be between 0 and 50"
	}
	switch v.MergeMethod {
	case "merge", "squash", "rebase":
	default:
		return "mergeMethod must be merge, squash or rebase"
	}
	if utf8.RuneCountInString(v.Model) > 200 {
		return "model is longer than 200 characters"
	}
	return ""
}

func (a *server) hiveVerify(w http.ResponseWriter, r *http.Request, hiveID string) {
	if !requireAdminSession(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		hive, err := a.s.Hive(r.Context(), hiveID)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]any{
			"hiveId": hive.ID,
			"verify": merge.ResolveVerify(hive.Config.Verify),
		})
	case http.MethodPatch, http.MethodPut:
		var patch verifyConfigPatch
		if !decode(w, r, &patch) {
			return
		}
		hive, err := a.s.Hive(r.Context(), hiveID)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		verify := merge.ResolveVerify(patch.apply(hive.Config.Verify))
		if msg := validateVerifyConfig(verify); msg != "" {
			problem(w, http.StatusUnprocessableEntity, msg)
			return
		}
		hive.Config.Verify = verify
		updated, err := a.s.UpdateHive(r.Context(), hive)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]any{
			"hive":   updated,
			"verify": merge.ResolveVerify(updated.Config.Verify),
		})
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type foragerMergeResponse struct {
	ForagerID string           `json:"foragerId"`
	HiveID    string           `json:"hiveId"`
	PRNumber  int64            `json:"prNumber"`
	Readiness *merge.Readiness `json:"readiness,omitempty"`
	Verdicts  []domain.Verdict `json:"verdicts"`
	Error     string           `json:"error,omitempty"`
}

func (a *server) foragerMerge(w http.ResponseWriter, r *http.Request, foragerID string) {
	if !requireAdminSession(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	forager, err := a.s.Agent(r.Context(), foragerID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if forager.Role != domain.RoleForager {
		problem(w, http.StatusBadRequest, "agent is not a forager")
		return
	}
	out := foragerMergeResponse{
		ForagerID: forager.ID,
		HiveID:    forager.HiveID,
		PRNumber:  forager.PRNumber,
		Verdicts:  []domain.Verdict{},
	}
	if forager.PRNumber == 0 {
		respond(w, http.StatusOK, out)
		return
	}
	if a.cfg.Merge == nil {
		problem(w, http.StatusServiceUnavailable, "merge status is unavailable")
		return
	}
	hive, err := a.s.Hive(r.Context(), forager.HiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	verdicts, verdictErr := a.cfg.Merge.Verdicts(r.Context(), hive.ID, forager.PRNumber)
	if verdictErr != nil && !errors.Is(verdictErr, domain.ErrNotFound) {
		failure(w, verdictErr)
		return
	}
	if verdicts != nil {
		out.Verdicts = verdicts
	}
	readiness, checkErr := a.cfg.Merge.Check(r.Context(), hive, forager.PRNumber)
	if checkErr != nil {
		// The verdict history is still useful when GitHub is temporarily
		// unreadable; report the gate error instead of discarding the page.
		out.Error = checkErr.Error()
		respond(w, http.StatusOK, out)
		return
	}
	out.Readiness = &readiness
	respond(w, http.StatusOK, out)
}
