package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
	"github.com/yyydddkkk/SwarmJet/internal/router"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type refreshMerge struct {
	version atomic.Int32
	checks  atomic.Int32
	delay   time.Duration
}

func (f *refreshMerge) Check(ctx context.Context, _ domain.Hive, n int64) (merge.Readiness, error) {
	f.checks.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return merge.Readiness{}, ctx.Err()
		}
	}
	if n == 2 {
		return merge.Readiness{}, errors.New("one pull is unavailable")
	}
	v := f.version.Load()
	return merge.Readiness{Ready: v > 0, HeadSHA: "head", Pull: merge.Pull{Number: n, State: "open", Title: "version", HeadSHA: "head", UpdatedAt: time.Now().UTC()}, Reasons: []string{}}, nil
}
func (f *refreshMerge) CheckListed(ctx context.Context, h domain.Hive, n int64, _ github.PullState) (merge.Readiness, error) {
	return f.Check(ctx, h, n)
}
func (f *refreshMerge) ListPullStates(_ context.Context, _ domain.Hive, state string) ([]github.PullState, error) {
	if state == "closed" {
		return []github.PullState{}, nil
	}
	return []github.PullState{{Number: 1, State: "open", Title: "version", HeadSHA: "head", UpdatedAt: time.Now().UTC()}}, nil
}
func (*refreshMerge) Attestations(context.Context, string, int64) ([]domain.Attestation, error) {
	return nil, nil
}

func TestPRListStaleRefreshPublishesSnapshotEvent(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "refresh", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RecordAttestation(ctx, domain.Attestation{HiveID: h.ID, PRNumber: 1, HeadSHA: "head", AttesterID: "verifier", Result: domain.AttestPass, Summary: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	f := &refreshMerge{}
	a := &server{s: s, cfg: Config{Merge: f}}
	path := "/api/hives/" + h.ID + "/prs"
	first := httptest.NewRecorder()
	a.pullRequestList(first, httptest.NewRequest(http.MethodGet, path, nil), h.ID)
	if first.Code != 200 {
		t.Fatal(first.Body.String())
	}
	sub, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	a.prCacheMu.Lock()
	entry := a.prLists[h.ID]
	entry.expires = time.Now().Add(-time.Second)
	a.prLists[h.ID] = entry
	a.prCacheMu.Unlock()
	a.prCacheMu.Lock()
	for key, cached := range a.prCache {
		cached.expires = time.Now().Add(-time.Second)
		a.prCache[key] = cached
	}
	a.prCacheMu.Unlock()
	f.version.Store(1)
	start := time.Now()
	stale := httptest.NewRecorder()
	a.pullRequestList(stale, httptest.NewRequest(http.MethodGet, path, nil), h.ID)
	if stale.Code != 200 || time.Since(start) > 300*time.Millisecond || !containsJSON(stale.Body.String(), `"stale":true`) {
		t.Fatalf("stale response %s in %s", stale.Body.String(), time.Since(start))
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ref := <-sub.Events:
			if ref.Type == "pr" && ref.ID == "1" && ref.Source == "snapshot" {
				data, err := a.eventData(ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				if data.(map[string]any)["ready"] != true {
					t.Fatalf("event=%v", data)
				}
				return
			}
		case <-deadline:
			t.Fatal("changed refresh emitted no snapshot event")
		}
	}
}

func TestPRItemFailureAndTerminalSnapshotAvoidsGitHub(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "partial", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{1, 2} {
		_, _, err = s.RecordAttestation(ctx, domain.Attestation{HiveID: h.ID, PRNumber: n, HeadSHA: "head", AttesterID: "verifier", Result: domain.AttestPass, Summary: "ok"})
		if err != nil {
			t.Fatal(err)
		}
	}
	f := &failingMerge{}
	_, err = s.SavePRSnapshot(ctx, store.PRSnapshot{HiveID: h.ID, Number: 1, Title: "closed", State: "closed", Merged: true, ClosedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), HeadSHA: "head"})
	if err != nil {
		t.Fatal(err)
	}
	a := &server{s: s, cfg: Config{Merge: f}}
	w := httptest.NewRecorder()
	a.pullRequestList(w, httptest.NewRequest(http.MethodGet, "/api/hives/"+h.ID+"/prs?state=all", nil), h.ID)
	if w.Code != 200 || !containsJSON(w.Body.String(), `"unavailable":true`) || f.checks.Load() != 1 {
		t.Fatalf("response=%s checks=%d", w.Body.String(), f.checks.Load())
	}
	data, err := a.eventData(ctx, store.EventRef{Type: "pr", HiveID: h.ID, ID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["merged"] != true || f.checks.Load() != 1 {
		t.Fatalf("terminal event=%v checks=%d", data, f.checks.Load())
	}
}

func TestClosedWebhookHydratesFromSnapshot(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "webhook", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RecordAttestation(ctx, domain.Attestation{HiveID: h.ID, PRNumber: 7, HeadSHA: "head", AttesterID: "verifier", Result: domain.AttestPass, Summary: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	decision, err := router.New(s).Route(ctx, router.Event{Type: router.EventPullRequest, Action: "closed", Repository: h.Repository, Number: 7, Title: "closed", URL: "https://github.test/pr/7", SHA: "head", PRState: "closed", Merged: true, ClosedAt: now, UpdatedAt: now})
	if err != nil || decision.HiveID != h.ID {
		t.Fatalf("route=%+v err=%v", decision, err)
	}
	f := &failingMerge{}
	a := &server{s: s, cfg: Config{Merge: f}}
	a.prLists = map[string]prListCacheEntry{h.ID: {prs: []prListItem{{Number: 7, State: "open", HeadSHA: "head"}}, expires: time.Now().Add(time.Minute)}}
	data, err := a.eventData(ctx, store.EventRef{Type: "pr", HiveID: h.ID, ID: "7"})
	if err != nil || data.(map[string]any)["merged"] != true || f.checks.Load() != 0 {
		t.Fatalf("event=%v err=%v checks=%d", data, err, f.checks.Load())
	}
	open := httptest.NewRecorder()
	a.pullRequestList(open, httptest.NewRequest(http.MethodGet, "/api/hives/"+h.ID+"/prs", nil), h.ID)
	if open.Code != 200 || !containsJSON(open.Body.String(), `"prs":[]`) {
		t.Fatalf("hot open list retained closed PR: %s", open.Body.String())
	}
	w := httptest.NewRecorder()
	a.pullRequestList(w, httptest.NewRequest(http.MethodGet, "/api/hives/"+h.ID+"/prs?state=all", nil), h.ID)
	if w.Code != 200 || f.checks.Load() != 0 {
		t.Fatalf("list=%s checks=%d", w.Body.String(), f.checks.Load())
	}
}

type overlappingMerge struct {
	active atomic.Int32
	max    atomic.Int32
	checks atomic.Int32
}
type failingMerge struct {
	checks atomic.Int32
	delay  time.Duration
}

func (f *failingMerge) Check(_ context.Context, _ domain.Hive, n int64) (merge.Readiness, error) {
	f.checks.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if n == 2 {
		return merge.Readiness{}, errors.New("failed")
	}
	return merge.Readiness{Pull: merge.Pull{Number: n, State: "closed"}}, nil
}
func (*failingMerge) Attestations(context.Context, string, int64) ([]domain.Attestation, error) {
	return nil, nil
}
func (f *overlappingMerge) Check(ctx context.Context, _ domain.Hive, n int64) (merge.Readiness, error) {
	f.checks.Add(1)
	v := f.active.Add(1)
	for {
		m := f.max.Load()
		if v <= m || f.max.CompareAndSwap(m, v) {
			break
		}
	}
	select {
	case <-time.After(80 * time.Millisecond):
	case <-ctx.Done():
	}
	f.active.Add(-1)
	return merge.Readiness{Pull: merge.Pull{Number: n, State: "open"}}, nil
}

func TestSamePRConcurrentReadsShareOneCheck(t *testing.T) {
	f := &overlappingMerge{}
	a := &server{cfg: Config{Merge: f}}
	h := domain.Hive{ID: "one", Repository: "acme/widgets"}
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	for range 2 {
		go func() { defer wg.Done(); <-start; _, _ = a.cachedPR(context.Background(), h, 1, "") }()
	}
	close(start)
	wg.Wait()
	if f.checks.Load() != 1 {
		t.Fatalf("checks=%d", f.checks.Load())
	}
}
func TestSamePRConcurrentFailureSharesOneCheck(t *testing.T) {
	f := &failingMerge{delay: 80 * time.Millisecond}
	a := &server{cfg: Config{Merge: f}}
	h := domain.Hive{ID: "one", Repository: "acme/widgets"}
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			defer wg.Done()
			<-start
			_, err := a.cachedPR(context.Background(), h, 2, "")
			if err == nil {
				t.Error("expected shared error")
			}
		}()
	}
	close(start)
	wg.Wait()
	if f.checks.Load() != 1 {
		t.Fatalf("failed checks=%d", f.checks.Load())
	}
}
func (*overlappingMerge) Attestations(context.Context, string, int64) ([]domain.Attestation, error) {
	return nil, nil
}
func TestPRCacheDoesNotHoldGlobalLockDuringNetwork(t *testing.T) {
	f := &overlappingMerge{}
	a := &server{cfg: Config{Merge: f}}
	var wg sync.WaitGroup
	wg.Add(2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			defer wg.Done()
			_, _ = a.cachedPR(context.Background(), domain.Hive{ID: id, Repository: "acme/widgets"}, 1, "")
		}(id)
	}
	wg.Wait()
	if f.max.Load() < 2 {
		t.Fatalf("network calls serialized: max=%d", f.max.Load())
	}
}

func containsJSON(body, piece string) bool { return len(body) > 0 && strings.Contains(body, piece) }
