package onboarding

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
)

const (
	redeliverWindow      = 72 * time.Hour
	redeliverInterval    = 10 * time.Minute
	redeliverMaxPerRun   = 20
	redeliverMaxPages    = 30
	redeliverMaxPerGUID  = 3
	redeliverRunDeadline = 2 * time.Minute
)

// RedeliveryStatus is the result of the most recent redelivery run.
type RedeliveryStatus struct {
	LastRunAt   *time.Time `json:"lastRunAt"`
	Redelivered int        `json:"redelivered"`
	LastError   string     `json:"lastError"`
}

// RedeliveryConfig configures the failed-delivery redelivery job.
type RedeliveryConfig struct {
	Apps interface {
		App(context.Context) (*githubapp.App, error)
	}
	// Trigger starts an extra run, for example when the tunnel address changed.
	Trigger  <-chan struct{}
	Interval time.Duration
	Window   time.Duration
	MaxPer   int
	Pause    time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

// Redeliverer asks GitHub to redeliver App webhook deliveries that failed, for
// example while the tunnel was down. GitHub never retries them on its own. The
// platform is idempotent per delivery id, so a redelivery is safe.
// See "Handling failed webhook deliveries" in the GitHub documentation.
type Redeliverer struct {
	cfg RedeliveryConfig

	mu        sync.Mutex
	requested map[int64]time.Time
	guids     map[string]guidAttempts
	status    RedeliveryStatus
}

type guidAttempts struct {
	count int
	last  time.Time
}

// NewRedeliverer applies defaults to cfg. Pause is the delay between
// redelivery requests and defaults to one second to respect rate limits.
func NewRedeliverer(cfg RedeliveryConfig) (*Redeliverer, error) {
	if cfg.Apps == nil {
		return nil, errors.New("redelivery requires a GitHub App source")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = redeliverInterval
	}
	if cfg.Window <= 0 {
		cfg.Window = redeliverWindow
	}
	if cfg.MaxPer <= 0 {
		cfg.MaxPer = redeliverMaxPerRun
	}
	if cfg.Pause == 0 {
		cfg.Pause = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Redeliverer{cfg: cfg, requested: map[int64]time.Time{}, guids: map[string]guidAttempts{}}, nil
}

// Status returns a snapshot for GET /api/setup/status.
func (r *Redeliverer) Status() RedeliveryStatus {
	if r == nil {
		return RedeliveryStatus{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Run redelivers at startup, on every trigger and on every interval until ctx
// ends.
func (r *Redeliverer) Run(ctx context.Context) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	r.runLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.cfg.Trigger:
			r.runLogged(ctx)
		case <-ticker.C:
			r.runLogged(ctx)
		}
	}
}

func (r *Redeliverer) runLogged(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, redeliverRunDeadline)
	defer cancel()
	if err := r.RunOnce(runCtx); err != nil && ctx.Err() == nil {
		r.cfg.Logger.Warn("GitHub webhook redelivery failed", "error", err)
	}
}

// RunOnce lists recent deliveries and requests redelivery of failed ones.
// The outcome is recorded in Status. A missing App is not an error.
func (r *Redeliverer) RunOnce(ctx context.Context) error {
	app, err := r.cfg.Apps.App(ctx)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return nil
		}
		return r.finish(0, err)
	}
	now := r.cfg.Now()
	cutoff := now.Add(-r.cfg.Window)
	r.prune(cutoff)

	var recent []githubapp.HookDelivery
	cursor := ""
	for page := 0; page < redeliverMaxPages; page++ {
		rows, next, listErr := app.ListHookDeliveries(ctx, cursor)
		if listErr != nil {
			return r.finish(0, listErr)
		}
		old := false
		for _, row := range rows {
			if row.DeliveredAt.Before(cutoff) {
				old = true
				continue
			}
			recent = append(recent, row)
		}
		if next == "" || old {
			break
		}
		cursor = next
	}

	count := 0
	for _, delivery := range r.pending(recent) {
		if count >= r.cfg.MaxPer {
			break
		}
		if count > 0 && r.cfg.Pause > 0 {
			select {
			case <-ctx.Done():
				return r.finish(count, ctx.Err())
			case <-time.After(r.cfg.Pause):
			}
		}
		if err = app.RedeliverHook(ctx, delivery.ID); err != nil {
			// A rate limit or any other failure ends this run; the delivery is
			// still unrecorded, so the next run tries it again.
			return r.finish(count, err)
		}
		r.record(delivery, now)
		count++
	}
	return r.finish(count, nil)
}

// pending returns, oldest first, the newest failed attempt of every guid that
// has no successful delivery and was not already requested.
func (r *Redeliverer) pending(recent []githubapp.HookDelivery) []githubapp.HookDelivery {
	succeeded := map[string]bool{}
	for _, d := range recent {
		if !d.Failed() {
			succeeded[d.GUID] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	var out []githubapp.HookDelivery
	for _, d := range recent { // newest first
		if !d.Failed() || succeeded[d.GUID] || seen[d.GUID] {
			continue
		}
		seen[d.GUID] = true
		if _, done := r.requested[d.ID]; done || r.guids[d.GUID].count >= redeliverMaxPerGUID {
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeliveredAt.Before(out[j].DeliveredAt) })
	return out
}

func (r *Redeliverer) record(d githubapp.HookDelivery, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requested[d.ID] = now
	attempts := r.guids[d.GUID]
	r.guids[d.GUID] = guidAttempts{count: attempts.count + 1, last: now}
}

func (r *Redeliverer) prune(cutoff time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, at := range r.requested {
		if at.Before(cutoff) {
			delete(r.requested, id)
		}
	}
	for guid, attempts := range r.guids {
		if attempts.last.Before(cutoff) {
			delete(r.guids, guid)
		}
	}
}

func (r *Redeliverer) finish(count int, err error) error {
	at := r.cfg.Now()
	r.mu.Lock()
	r.status = RedeliveryStatus{LastRunAt: &at, Redelivered: count}
	if err != nil {
		r.status.LastError = "补投 webhook 失败：" + err.Error()
	}
	r.mu.Unlock()
	return err
}
