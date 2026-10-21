package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
	"github.com/yyydddkkk/SwarmJet/internal/store"
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
type prCall struct {
	done      chan struct{}
	readiness merge.Readiness
	err       error
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
	Unavailable  bool                 `json:"unavailable,omitempty"`
	ClosedAt     time.Time            `json:"-"`
}

type prListCacheEntry struct {
	prs     []prListItem
	expires time.Time
}

func (a *server) cachedPR(ctx context.Context, hive domain.Hive, pr int64, attestedHead string) (merge.Readiness, error) {
	return a.cachedPRWithPull(ctx, hive, pr, attestedHead, nil)
}

type listedPRChecker interface {
	CheckListed(context.Context, domain.Hive, int64, github.PullState) (merge.Readiness, error)
}
type listedPRBlocker interface {
	CheckListedBlocker(context.Context, domain.Hive, int64, github.PullState) (merge.Readiness, bool, error)
}

func (a *server) cachedPRWithPull(ctx context.Context, hive domain.Hive, pr int64, attestedHead string, listed *github.PullState) (merge.Readiness, error) {
	key := prCacheKey{hive: hive.ID, repository: hive.Repository, number: pr, attestedHead: attestedHead}
	if listed != nil {
		key.head = listed.HeadSHA
	}
	flightKey := key
	for {
		a.prCacheMu.Lock()
		now := time.Now()
		for cached, entry := range a.prCache {
			if cached.hive == key.hive && cached.repository == key.repository && cached.number == pr && cached.attestedHead == attestedHead && (listed == nil || cached.head == listed.HeadSHA) && now.Before(entry.expires) {
				a.prCacheMu.Unlock()
				return entry.readiness, nil
			}
		}
		if call := a.prInFlight[key]; call != nil {
			a.prCacheMu.Unlock()
			select {
			case <-call.done:
				return call.readiness, call.err
			case <-ctx.Done():
				return merge.Readiness{}, ctx.Err()
			}
		}
		if a.prInFlight == nil {
			a.prInFlight = map[prCacheKey]*prCall{}
		}
		call := &prCall{done: make(chan struct{})}
		a.prInFlight[key] = call
		a.prCacheMu.Unlock()
		var readiness merge.Readiness
		var err error
		if checker, ok := a.cfg.Merge.(listedPRChecker); ok && listed != nil {
			readiness, err = checker.CheckListed(ctx, hive, pr, *listed)
		} else {
			readiness, err = a.cfg.Merge.Check(ctx, hive, pr)
		}
		a.prCacheMu.Lock()
		if err == nil {
			if a.prCache == nil {
				a.prCache = map[prCacheKey]prCacheEntry{}
			}
			for old := range a.prCache {
				if old.hive == key.hive && old.number == pr {
					delete(a.prCache, old)
				}
			}
			key.head = readiness.HeadSHA
			a.prCache[key] = prCacheEntry{readiness, time.Now().Add(30 * time.Second)}
		}
		delete(a.prInFlight, flightKey)
		call.readiness, call.err = readiness, err
		close(call.done)
		a.prCacheMu.Unlock()
		return readiness, err
	}
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
	a.prCacheMu.Lock()
	entry, hit := a.prLists[hiveID]
	if hit && time.Now().Before(entry.expires) {
		a.prCacheMu.Unlock()
		respond(w, 200, map[string]any{"prs": filterPRList(entry.prs, state)})
		return
	}
	if hit {
		if a.prListRefreshing == nil {
			a.prListRefreshing = map[string]bool{}
		}
		if !a.prListRefreshing[hiveID] {
			a.prListRefreshing[hiveID] = true
			go a.refreshPRList(hive)
		}
		a.prCacheMu.Unlock()
		respond(w, 200, map[string]any{"prs": filterPRList(entry.prs, state), "stale": true})
		return
	}
	a.prCacheMu.Unlock()
	prs, err := a.readPRList(r.Context(), hive, false)
	if err != nil {
		failure(w, err)
		return
	}
	a.prCacheMu.Lock()
	if a.prLists == nil {
		a.prLists = map[string]prListCacheEntry{}
	}
	a.prLists[hiveID] = prListCacheEntry{prs: prs, expires: time.Now().Add(30 * time.Second)}
	a.prCacheMu.Unlock()
	respond(w, 200, map[string]any{"prs": filterPRList(prs, state)})
}

func filterPRList(items []prListItem, state string) []prListItem {
	out := []prListItem{}
	for _, item := range items {
		if item.State == "open" || item.Unavailable || state == "all" && item.ClosedAt.After(time.Now().Add(-7*24*time.Hour)) {
			out = append(out, item)
		}
	}
	return out
}

func (a *server) refreshPRList(hive domain.Hive) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := a.readPRList(ctx, hive, true)
	a.prCacheMu.Lock()
	if err == nil {
		if a.prLists == nil {
			a.prLists = map[string]prListCacheEntry{}
		}
		a.prLists[hive.ID] = prListCacheEntry{prs: items, expires: time.Now().Add(30 * time.Second)}
	}
	delete(a.prListRefreshing, hive.ID)
	a.prCacheMu.Unlock()
}

type bulkPRLister interface {
	ListPullStates(context.Context, domain.Hive, string) ([]github.PullState, error)
}

func (a *server) readPRList(ctx context.Context, hive domain.Hive, notify bool) ([]prListItem, error) {
	candidates, err := a.s.ProjectPRs(ctx, hive.ID)
	if err != nil {
		return nil, err
	}
	snapshots, err := a.s.PRSnapshots(ctx, hive.ID)
	if err != nil {
		return nil, err
	}
	visible := make([]store.ProjectPR, 0, len(candidates))
	needsNetwork := false
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, candidate := range candidates {
		p := snapshots[candidate.Number]
		if p.State != "" && p.State != "open" && !p.ClosedAt.After(cutoff) {
			continue
		}
		visible = append(visible, candidate)
		if p.State == "" || p.State == "open" {
			needsNetwork = true
		}
	}
	candidates = visible
	if len(candidates) == 0 {
		return []prListItem{}, nil
	}
	open := map[int64]github.PullState{}
	closed := map[int64]github.PullState{}
	lister, bulk := a.cfg.Merge.(bulkPRLister)
	var openErr, closedErr error
	if bulk && needsNetwork {
		needClosed := false
		for _, c := range candidates {
			if snapshots[c.Number].State == "" {
				needClosed = true
				break
			}
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			var rows []github.PullState
			rows, openErr = lister.ListPullStates(ctx, hive, "open")
			for _, row := range rows {
				open[row.Number] = row
			}
		}()
		if needClosed {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var rows []github.PullState
				rows, closedErr = lister.ListPullStates(ctx, hive, "closed")
				for _, row := range rows {
					closed[row.Number] = row
				}
			}()
		}
		wg.Wait()
		if openErr == nil && !needClosed {
			for _, c := range candidates {
				if snapshots[c.Number].State == "open" {
					if _, ok := open[c.Number]; !ok {
						needClosed = true
						break
					}
				}
			}
			if needClosed {
				var rows []github.PullState
				rows, closedErr = lister.ListPullStates(ctx, hive, "closed")
				for _, row := range rows {
					closed[row.Number] = row
				}
			}
		}
	}
	items := make([]prListItem, len(candidates))
	sem := make(chan struct{}, 4)
	failures := make(chan error, len(candidates)*2)
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		go func(i int, c store.ProjectPR) {
			defer wg.Done()
			finish := func(item prListItem) {
				withAttestations, err := a.withPRAttestations(ctx, hive.ID, item)
				if err != nil {
					failures <- err
				}
				items[i] = withAttestations
			}
			prior := snapshots[c.Number]
			var listed *github.PullState
			item := prListItem{Number: c.Number, Title: prior.Title, URL: prior.URL, HeadSHA: prior.HeadSHA, State: prior.State, Merged: prior.Merged, Ready: prior.Ready, Reasons: prior.Reasons, OwnerID: c.OwnerID, UpdatedAt: prior.UpdatedAt, ClosedAt: prior.ClosedAt}
			if prior.State != "open" && prior.State != "" {
				finish(item)
				return
			}
			if bulk && openErr == nil {
				if p, ok := open[c.Number]; ok {
					listed = &p
					item.Title, item.URL, item.HeadSHA, item.State, item.Merged, item.UpdatedAt, item.ClosedAt = p.Title, p.URL, p.HeadSHA, p.State, p.Merged, p.UpdatedAt, p.ClosedAt
				} else if p, ok := closed[c.Number]; ok {
					item.Title, item.URL, item.HeadSHA, item.State, item.Merged, item.UpdatedAt, item.ClosedAt = p.Title, p.URL, p.HeadSHA, p.State, p.Merged, p.UpdatedAt, p.ClosedAt
					item.Ready = false
					item.Reasons = []string{"pull request is not open"}
					if err := a.savePRListItem(ctx, hive.ID, item, p.ClosedAt, notify); err != nil {
						failures <- err
					}
					finish(item)
					return
				} else if closedErr == nil && prior.State == "" {
					item.Unavailable = true
					item.Reasons = []string{"状态暂时无法获取"}
					finish(item)
					return
				}
			}
			if listed != nil {
				if checker, ok := a.cfg.Merge.(listedPRBlocker); ok {
					blocked, decisive, blockerErr := checker.CheckListedBlocker(ctx, hive, c.Number, *listed)
					if blockerErr != nil {
						item.Unavailable = true
						item.Ready = false
						item.Reasons = []string{"状态暂时无法获取"}
						finish(item)
						return
					}
					if decisive {
						item.Title, item.URL, item.HeadSHA, item.State, item.Merged, item.Ready, item.Reasons, item.UpdatedAt = blocked.Pull.Title, blocked.Pull.URL, blocked.HeadSHA, blocked.Pull.State, blocked.Pull.Merged, blocked.Ready, blocked.Reasons, blocked.Pull.UpdatedAt
						if err := a.savePRListItem(ctx, hive.ID, item, time.Time{}, notify); err != nil {
							failures <- err
						}
						finish(item)
						return
					}
				}
			}
			sem <- struct{}{}
			status, checkErr := a.cachedPRWithPull(ctx, hive, c.Number, c.HeadSHA, listed)
			<-sem
			if checkErr != nil {
				item.Unavailable = true
				item.Ready = false
				item.Reasons = []string{"状态暂时无法获取"}
			} else {
				item.Title, item.URL, item.HeadSHA, item.State, item.Merged, item.Ready, item.Reasons, item.UpdatedAt, item.ClosedAt = status.Pull.Title, status.Pull.URL, status.HeadSHA, status.Pull.State, status.Pull.Merged, status.Ready, status.Reasons, status.Pull.UpdatedAt, status.Pull.ClosedAt
				if item.Reasons == nil {
					item.Reasons = []string{}
				}
				if err := a.savePRListItem(ctx, hive.ID, item, status.Pull.ClosedAt, notify); err != nil {
					failures <- err
				}
			}
			finish(item)
		}(i, c)
	}
	wg.Wait()
	select {
	case err := <-failures:
		return nil, err
	default:
	}
	latest, err := a.s.PRSnapshots(ctx, hive.ID)
	if err == nil {
		for i, item := range items {
			p, ok := latest[item.Number]
			if !ok {
				continue
			}
			if p.UpdatedAt.After(item.UpdatedAt) || p.UpdatedAt.Equal(item.UpdatedAt) && p.State != item.State {
				item.Title, item.URL, item.HeadSHA, item.State, item.Merged, item.Ready, item.Reasons, item.UpdatedAt, item.ClosedAt = p.Title, p.URL, p.HeadSHA, p.State, p.Merged, p.Ready, p.Reasons, p.UpdatedAt, p.ClosedAt
				item.Unavailable = false
				items[i] = item
			}
		}
	}
	return items, nil
}

func (a *server) withPRAttestations(ctx context.Context, hiveID string, item prListItem) (prListItem, error) {
	attestations, err := a.cfg.Merge.Attestations(ctx, hiveID, item.Number)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return item, err
	}
	if attestations != nil {
		item.Attestations = attestations
	} else {
		item.Attestations = []domain.Attestation{}
	}
	if item.Reasons == nil {
		item.Reasons = []string{}
	}
	return item, nil
}

func (a *server) savePRListItem(ctx context.Context, hiveID string, item prListItem, closedAt time.Time, notify bool) error {
	changed, err := a.s.SavePRSnapshot(ctx, store.PRSnapshot{HiveID: hiveID, Number: item.Number, Title: item.Title, State: item.State, Merged: item.Merged, ClosedAt: closedAt, URL: item.URL, HeadSHA: item.HeadSHA, UpdatedAt: item.UpdatedAt, Ready: item.Ready, Reasons: item.Reasons})
	if err != nil {
		return err
	}
	if changed && notify {
		return a.s.PublishPRSnapshotEvent(ctx, hiveID, item.Number)
	}
	return nil
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
