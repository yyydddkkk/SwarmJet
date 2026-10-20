package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	if len(parts) >= 3 && parts[0] == "hives" && parts[2] == "prs" {
		if len(parts) == 3 {
			a.pullRequestList(w, r, parts[1])
			return true
		}
		if len(parts) == 5 && parts[4] == "merge" {
			a.mergePullRequest(w, r, parts[1], parts[3])
			return true
		}
	}
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

// The cache includes both the observed head and the latest attested head. A new
// attestation for another head invalidates it immediately; other GitHub changes age out in 30s.
type prCacheKey struct {
	hive, repository string
	number           int64
	attestedHead     string
	head             string
}
type prCacheEntry struct {
	readiness merge.Readiness
	expires   time.Time
}

type prListItem struct {
	Number       int64                `json:"number"`
	Title        string               `json:"title"`
	URL          string               `json:"url"`
	HeadSHA      string               `json:"headSha"`
	State        string               `json:"state"`
	Merged       bool                 `json:"merged"`
	Ready        bool                 `json:"ready"`
	Reasons      []string             `json:"reasons"`
	OwnerID      string               `json:"ownerId"`
	Attestations []domain.Attestation `json:"attestations"`
	UpdatedAt    time.Time            `json:"updatedAt"`
}

func (a *server) cachedPR(ctx context.Context, hive domain.Hive, pr int64, attestedHead string) (merge.Readiness, error) {
	key := prCacheKey{hive: hive.ID, repository: hive.Repository, number: pr, attestedHead: attestedHead}
	a.prCacheMu.Lock()
	defer a.prCacheMu.Unlock()
	now := time.Now()
	for cached, entry := range a.prCache {
		if cached.hive == key.hive && cached.repository == key.repository && cached.number == pr && cached.attestedHead == attestedHead && now.Before(entry.expires) {
			return entry.readiness, nil
		}
	}
	readiness, err := a.cfg.Merge.Check(ctx, hive, pr)
	if err != nil {
		return readiness, err
	}
	if a.prCache == nil {
		a.prCache = map[prCacheKey]prCacheEntry{}
	}
	for old, entry := range a.prCache {
		if !now.Before(entry.expires) || old.hive == key.hive && old.number == pr {
			delete(a.prCache, old)
		}
	}
	key.head = readiness.HeadSHA
	a.prCache[key] = prCacheEntry{readiness, now.Add(30 * time.Second)}
	return readiness, nil
}

func (a *server) pullRequestList(w http.ResponseWriter, r *http.Request, hiveID string) {
	if r.Method != http.MethodGet {
		problem(w, 405, "method not allowed")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "all" {
		problem(w, 400, "state must be open or all")
		return
	}
	if a.s == nil || a.cfg.Merge == nil {
		problem(w, 503, "pull request status is unavailable")
		return
	}
	hive, err := a.s.Hive(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	candidates, err := a.s.ProjectPRs(r.Context(), hiveID)
	if err != nil {
		failure(w, err)
		return
	}
	prs := []prListItem{}
	for _, candidate := range candidates {
		status, err := a.cachedPR(r.Context(), hive, candidate.Number, candidate.HeadSHA)
		if err != nil {
			problem(w, 502, "pull request status is unavailable: "+err.Error())
			return
		}
		if status.Pull.State != "open" && (state != "all" || status.Pull.ClosedAt.Before(time.Now().Add(-7*24*time.Hour))) {
			continue
		}
		attestations, err := a.cfg.Merge.Attestations(r.Context(), hiveID, candidate.Number)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			failure(w, err)
			return
		}
		if attestations == nil {
			attestations = []domain.Attestation{}
		}
		reasons := status.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		prs = append(prs, prListItem{candidate.Number, status.Pull.Title, status.Pull.URL, status.HeadSHA, status.Pull.State, status.Pull.Merged, status.Ready, reasons, candidate.OwnerID, attestations, status.Pull.UpdatedAt.UTC()})
	}
	respond(w, 200, map[string]any{"prs": prs})
}

type webPRMerger interface {
	MergeHead(context.Context, domain.Hive, int64, string) (merge.Outcome, error)
}

type webMergeResponse struct {
	Merged  bool     `json:"merged"`
	Queued  bool     `json:"queued"`
	Method  string   `json:"method"`
	HeadSHA string   `json:"headSha"`
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons"`
	Message string   `json:"message"`
}

func (a *server) mergePullRequest(w http.ResponseWriter, r *http.Request, hiveID, number string) {
	if r.Method != http.MethodPost {
		problem(w, 405, "method not allowed")
		return
	}
	pr, err := strconv.ParseInt(number, 10, 64)
	if err != nil || pr <= 0 {
		problem(w, 400, "invalid pull request number")
		return
	}
	var req struct {
		HeadSHA string `json:"headSha"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.HeadSHA = strings.TrimSpace(req.HeadSHA)
	if req.HeadSHA == "" {
		problem(w, 400, "headSha is required")
		return
	}
	merger, ok := a.cfg.Merge.(webPRMerger)
	if !ok || a.s == nil {
		problem(w, 503, "pull request merge is unavailable")
		return
	}
	hive, err := a.s.Hive(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if err = a.s.RecordWebPRMerge(r.Context(), hiveID, pr, req.HeadSHA, a.cfg.Username); err != nil {
		failure(w, err)
		return
	}
	outcome, err := merger.MergeHead(r.Context(), hive, pr, req.HeadSHA)
	a.prCacheMu.Lock()
	for key := range a.prCache {
		if key.hive == hiveID && key.number == pr {
			delete(a.prCache, key)
		}
	}
	a.prCacheMu.Unlock()
	if err != nil {
		var changed *merge.HeadChangedError
		if errors.As(err, &changed) {
			problem(w, 409, changed.Error())
			return
		}
		var notReady *merge.NotReadyError
		if errors.As(err, &notReady) {
			reasons := notReady.Readiness.Reasons
			if reasons == nil {
				reasons = []string{}
			}
			respond(w, 200, webMergeResponse{HeadSHA: notReady.Readiness.HeadSHA, Reasons: reasons, Message: "未合并：" + strings.Join(reasons, "；")})
			return
		}
		problem(w, 502, fmt.Sprintf("pull request merge is unavailable: %v", err))
		return
	}
	message := "GitHub 未确认合并，也未确认进入 merge queue"
	if outcome.Merged {
		message = "已合并"
	} else if outcome.Queued {
		message = "已排队"
	}
	respond(w, 200, webMergeResponse{outcome.Merged, outcome.Queued, outcome.Method, outcome.HeadSHA, true, []string{}, message})
}
