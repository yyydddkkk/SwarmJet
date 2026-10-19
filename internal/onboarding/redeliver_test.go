package onboarding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
)

var redeliverNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

type fakeDelivery struct {
	id     int64
	guid   string
	age    time.Duration
	status string
	code   int
}

type fakeGitHubHooks struct {
	mu          sync.Mutex
	deliveries  []fakeDelivery // newest first
	pageSize    int
	redelivered []int64
	failOn      int64
	limited     bool
	listFails   bool
}

func (f *fakeGitHubHooks) handler(baseURL func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/hook/deliveries":
			if f.listFails {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			start := 0
			if c := r.URL.Query().Get("cursor"); c != "" {
				start, _ = strconv.Atoi(c)
			}
			end := min(start+f.pageSize, len(f.deliveries))
			if end < len(f.deliveries) {
				w.Header().Set("Link", fmt.Sprintf(`<%s/app/hook/deliveries?per_page=100&cursor=%d>; rel="next"`, baseURL(), end))
			}
			var parts []string
			for _, d := range f.deliveries[start:end] {
				parts = append(parts, fmt.Sprintf(`{"id":%d,"guid":%q,"delivered_at":%q,"status":%q,"status_code":%d}`,
					d.id, d.guid, redeliverNow.Add(-d.age).Format(time.RFC3339), d.status, d.code))
			}
			_, _ = w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/attempts"):
			if f.limited {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "limited", http.StatusForbidden)
				return
			}
			id, _ := strconv.ParseInt(strings.Split(r.URL.Path, "/")[4], 10, 64)
			if id == f.failOn {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			f.redelivered = append(f.redelivered, id)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeGitHubHooks) sent() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]int64(nil), f.redelivered...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func newRedeliverer(t *testing.T, fake *fakeGitHubHooks, maxPer int) *Redeliverer {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(fake.handler(func() string { return server.URL }))
	t.Cleanup(server.Close)
	privateKey, _ := testPrivateKey(t)
	app, err := githubapp.NewWithHTTP(githubapp.Config{AppID: 99, PrivateKeyPEM: privateKey}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRedeliverer(RedeliveryConfig{
		Apps:   &fakeTunnelApps{app: app},
		MaxPer: maxPer,
		Pause:  time.Nanosecond,
		Now:    func() time.Time { return redeliverNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRedeliveryRedeliversOnlyFailedWithoutSuccess(t *testing.T) {
	fake := &fakeGitHubHooks{pageSize: 2, deliveries: []fakeDelivery{
		{10, "g-ok", time.Hour, "OK", 202},
		{9, "g-fail", 2 * time.Hour, "Invalid HTTP Response: 502", 502},
		{8, "g-ok", 3 * time.Hour, "Invalid HTTP Response: 502", 502}, // later succeeded
		{7, "g-timeout", 4 * time.Hour, "timed out", 0},
		{6, "g-5xx", 5 * time.Hour, "OK", 500},
		{5, "g-5xx", 6 * time.Hour, "OK", 500}, // only the newest attempt per guid
		{4, "g-old", 80 * time.Hour, "timed out", 0},
		{3, "g-older", 90 * time.Hour, "timed out", 0},
	}}
	r := newRedeliverer(t, fake, 20)
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := fake.sent()
	want := []int64{6, 7, 9}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("redelivered = %v, want %v", got, want)
	}
	status := r.Status()
	if status.Redelivered != 3 || status.LastError != "" || status.LastRunAt == nil {
		t.Fatalf("status = %+v", status)
	}

	// A second run does not request the same deliveries again.
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got = fake.sent(); len(got) != 3 || r.Status().Redelivered != 0 {
		t.Fatalf("second run redelivered %v, status %+v", got, r.Status())
	}
}

func TestRedeliveryLimitsPerRun(t *testing.T) {
	fake := &fakeGitHubHooks{pageSize: 100}
	for i := int64(1); i <= 5; i++ {
		fake.deliveries = append(fake.deliveries, fakeDelivery{100 - i, fmt.Sprintf("g-%d", i), time.Duration(i) * time.Hour, "timed out", 0})
	}
	r := newRedeliverer(t, fake, 2)
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.sent(); len(got) != 2 {
		t.Fatalf("first run redelivered %v", got)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.sent(); len(got) != 4 {
		t.Fatalf("second run total %v", got)
	}
}

func TestRedeliveryErrorsDoNotBlockNextRun(t *testing.T) {
	fake := &fakeGitHubHooks{pageSize: 100, failOn: 9, deliveries: []fakeDelivery{
		{9, "g-a", time.Hour, "timed out", 0},
		{8, "g-b", 2 * time.Hour, "timed out", 0},
	}}
	r := newRedeliverer(t, fake, 20)
	// Oldest first: 8 succeeds, 9 fails and ends the run.
	if err := r.RunOnce(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if s := r.Status(); s.Redelivered != 1 || !strings.Contains(s.LastError, "500") {
		t.Fatalf("status = %+v", s)
	}
	fake.mu.Lock()
	fake.failOn = 0
	fake.mu.Unlock()
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.sent(); fmt.Sprint(got) != "[8 9]" {
		t.Fatalf("redelivered = %v", got)
	}
	if s := r.Status(); s.LastError != "" || s.Redelivered != 1 {
		t.Fatalf("status = %+v", s)
	}
}

func TestRedeliveryStopsOnRateLimitAndListErrors(t *testing.T) {
	fake := &fakeGitHubHooks{pageSize: 100, limited: true, deliveries: []fakeDelivery{{9, "g-a", time.Hour, "timed out", 0}}}
	r := newRedeliverer(t, fake, 20)
	err := r.RunOnce(context.Background())
	var apiErr *githubapp.APIError
	if !errors.As(err, &apiErr) || !apiErr.RateLimited() {
		t.Fatalf("err = %v", err)
	}
	fake.mu.Lock()
	fake.limited, fake.listFails = false, true
	fake.mu.Unlock()
	if err = r.RunOnce(context.Background()); err == nil || r.Status().LastError == "" {
		t.Fatalf("list error not recorded: %v %+v", err, r.Status())
	}
	fake.mu.Lock()
	fake.listFails = false
	fake.mu.Unlock()
	if err = r.RunOnce(context.Background()); err != nil || fmt.Sprint(fake.sent()) != "[9]" {
		t.Fatalf("recovery err=%v sent=%v", err, fake.sent())
	}
}

func TestRedeliveryMissingAppIsQuiet(t *testing.T) {
	r, err := NewRedeliverer(RedeliveryConfig{Apps: &fakeTunnelApps{err: ErrNotConfigured}})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.RunOnce(context.Background()); err != nil || r.Status().LastRunAt != nil {
		t.Fatalf("err=%v status=%+v", err, r.Status())
	}
}

func TestRedeliveryRunTriggersOnSignal(t *testing.T) {
	fake := &fakeGitHubHooks{pageSize: 100}
	r := newRedeliverer(t, fake, 20)
	trigger := make(chan struct{}, 1)
	r.cfg.Trigger = trigger
	r.cfg.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	waitRun := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if s := r.Status(); s.LastRunAt != nil && s.Redelivered == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no run with %d redelivered, status %+v", want, r.Status())
	}
	waitRun(0) // startup run
	fake.mu.Lock()
	fake.deliveries = []fakeDelivery{{5, "g-x", time.Hour, "timed out", 0}}
	fake.mu.Unlock()
	trigger <- struct{}{}
	waitRun(1)
	cancel()
	<-done
}

func TestTunnelSignalsReadyOnChangeAndRecovery(t *testing.T) {
	var mu sync.Mutex
	hostname, fail := "one.trycloudflare.com", false
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"hostname":"` + hostname + `"}`))
	}))
	defer metrics.Close()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer github.Close()
	privateKey, _ := testPrivateKey(t)
	app, err := githubapp.NewWithHTTP(githubapp.Config{AppID: 5, PrivateKeyPEM: privateKey}, github.URL, github.Client())
	if err != nil {
		t.Fatal(err)
	}
	tunnel, err := NewTunnel(TunnelConfig{MetricsURL: metrics.URL, Apps: &fakeTunnelApps{app: app, secret: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	expect := func(want bool, step string) {
		t.Helper()
		select {
		case <-tunnel.Ready():
			if !want {
				t.Fatalf("%s: unexpected signal", step)
			}
		default:
			if want {
				t.Fatalf("%s: missing signal", step)
			}
		}
	}
	ctx := context.Background()
	_ = tunnel.SyncNow(ctx)
	expect(true, "first sync")
	_ = tunnel.SyncNow(ctx)
	expect(false, "unchanged")
	mu.Lock()
	fail = true
	mu.Unlock()
	if tunnel.SyncNow(ctx) == nil {
		t.Fatal("want error")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	_ = tunnel.SyncNow(ctx)
	expect(true, "recovered")
	mu.Lock()
	hostname = "two.trycloudflare.com"
	mu.Unlock()
	_ = tunnel.SyncNow(ctx)
	expect(true, "address changed")
}
