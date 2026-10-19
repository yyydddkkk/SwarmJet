package forager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const (
	// DefaultCLI is the CLI used when neither the Agent nor the Hive pins one.
	DefaultCLI = "opencode"
	// DefaultModel pins a model explicitly; relying on the CLI default has
	// resolved to models that need a subscription (see the OpenCode spike).
	DefaultModel = "opencode/big-pickle"
	// DefaultScanInterval is how often the recovery and lifecycle scan runs.
	DefaultScanInterval = 30 * time.Second
	// DefaultMaxFailures is the consecutive Start/Prompt failure budget.
	DefaultMaxFailures = 3
	// DefaultUnhandledLimit caps the messages read into one prompt.
	DefaultUnhandledLimit = 100
	// DefaultTurnAbortWait bounds how long a cancelled Prompt may take to return.
	DefaultTurnAbortWait = 5 * time.Second
)

// Clock is the supervisor's view of time. Tests inject a fake.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configures a Supervisor. Store, Runtime and Publisher are required.
type Options struct {
	Store     Store
	Runtime   cell.Runtime
	Publisher cell.Publisher

	// Clock defaults to the system clock.
	Clock Clock
	// Model is passed as cell.Spec.Model on every turn.
	Model string
	// BaseBranch is the branch verifier PRs target. It is only used in the
	// verifier prompt; empty means "the repository default branch".
	BaseBranch string
	// MCPURL is passed as cell.Spec.MCPURL on every turn.
	MCPURL string
	// CLIEnv resolves the CLI secrets for a Hive. It is called once per Start
	// attempt; values are passed to the Cell but never logged.
	CLIEnv func(ctx context.Context, hive domain.Hive) (map[string]string, error)
	// OnUpdate receives streamed output. It must not block.
	OnUpdate func(agent domain.Agent, update cell.Update)
	// Logger defaults to discarding output.
	Logger *slog.Logger

	// ScanInterval defaults to DefaultScanInterval.
	ScanInterval time.Duration
	// MaxFailures is the consecutive Start/Prompt failure budget, default 3.
	MaxFailures int
	// RetryBackoff returns the delay before retry number attempt (1-based).
	// Defaults to exponential backoff capped at one minute.
	RetryBackoff func(attempt int) time.Duration
	// UnhandledLimit defaults to DefaultUnhandledLimit.
	UnhandledLimit int
	// TurnAbortWait defaults to DefaultTurnAbortWait.
	TurnAbortWait time.Duration
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Model == "" {
		o.Model = DefaultModel
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.ScanInterval <= 0 {
		o.ScanInterval = DefaultScanInterval
	}
	if o.MaxFailures <= 0 {
		o.MaxFailures = DefaultMaxFailures
	}
	if o.RetryBackoff == nil {
		o.RetryBackoff = func(attempt int) time.Duration {
			delay := time.Second
			for i := 1; i < attempt && delay < time.Minute; i++ {
				delay *= 2
			}
			if delay > time.Minute {
				delay = time.Minute
			}
			return delay
		}
	}
	if o.UnhandledLimit <= 0 {
		o.UnhandledLimit = DefaultUnhandledLimit
	}
	if o.TurnAbortWait <= 0 {
		o.TurnAbortWait = DefaultTurnAbortWait
	}
	if o.CLIEnv == nil {
		o.CLIEnv = func(context.Context, domain.Hive) (map[string]string, error) { return nil, nil }
	}
	return o
}

// Supervisor owns one serial executor per live Forager.
type Supervisor struct {
	opts  Options
	clock Clock
	log   *slog.Logger

	mu     sync.Mutex
	actors map[string]*actor
	runCtx context.Context
}

// New validates the required dependencies and fills in defaults.
func New(opts Options) (*Supervisor, error) {
	if opts.Store == nil {
		return nil, errors.New("forager: Store is required")
	}
	if opts.Runtime == nil {
		return nil, errors.New("forager: Runtime is required")
	}
	if opts.Publisher == nil {
		return nil, errors.New("forager: Publisher is required")
	}
	opts = opts.withDefaults()
	return &Supervisor{
		opts:   opts,
		clock:  opts.Clock,
		log:    opts.Logger,
		actors: make(map[string]*actor),
	}, nil
}

// Notify accepts a delivered Inbox message. It matches store.MessageSink so it
// can be installed on store.Store later. Messages for anything but a Forager
// are ignored.
func (s *Supervisor) Notify(ctx context.Context, m domain.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, ok := foragerID(m.To)
	if !ok {
		return nil
	}
	return s.actor(id, m.HiveID).enqueue(s.baseContext(), m)
}

// Run starts the periodic scan and blocks until ctx ends. The context also
// bounds in-flight turns, so it must outlive individual Notify calls.
func (s *Supervisor) Run(ctx context.Context) error {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()

	if err := s.Scan(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("forager: initial scan failed", "error", err)
	}
	ticker := time.NewTicker(s.opts.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Scan(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("forager: scan failed", "error", err)
			}
		}
	}
}

// Scan applies lifecycle rules and recovers messages whose notification was
// missed, for example after a restart. One Forager's error is logged and does
// not stop the others.
func (s *Supervisor) Scan(ctx context.Context) error {
	hives, err := s.opts.Store.Hives(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, hive := range hives {
		foragers, err := s.opts.Store.Foragers(ctx, hive.ID,
			domain.AgentCreating, domain.AgentActive, domain.AgentParked, domain.AgentHibernated)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, agent := range foragers {
			if agent.Role != "" && agent.Role != domain.RoleForager {
				continue
			}
			if err := s.actor(agent.ID, agent.HiveID).scan(ctx, agent, hive); err != nil &&
				!errors.Is(err, domain.ErrNotFound) {
				s.log.Warn("forager: scan failed", "agent", agent.ID, "error", err)
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Supervisor) actor(id, hiveID string) *actor {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.actors[id]; ok {
		if a.hiveID == "" {
			a.hiveID = hiveID
		}
		return a
	}
	a := newActor(s, id, hiveID)
	s.actors[id] = a
	return a
}

// baseContext is the supervisor's long-lived context. Notify hands it to a new
// actor so a short-lived delivery-job context cannot cancel a turn, while Run
// can still shut everything down.
func (s *Supervisor) baseContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}
