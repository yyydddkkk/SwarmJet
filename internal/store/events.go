package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// eventChannel carries small committed entity invalidations.
const eventChannel = "swarmjet_events"

// eventListenerApplicationName marks the shared LISTEN connection in
// pg_stat_activity, which keeps it easy to find and to count in tests.
const eventListenerApplicationName = "swarmjet-event-listener"

var errEventListenerStopped = errors.New("store: event listener stopped")

// EventRef is the small database notification; entity data is read after commit.
type EventRef struct {
	Type   string `json:"type"`
	HiveID string `json:"hiveId"`
	ID     string `json:"id"`
	Source string `json:"source,omitempty"`
}
type eventWaiter struct {
	events chan EventRef
	ctx    context.Context
	cancel context.CancelFunc
}

// EventSubscription carries invalidations until the listener disconnects or
// its buffer overflows. Done cancels hydration without draining stale events.
type EventSubscription struct {
	Events <-chan EventRef
	ctx    context.Context
	close  func()
}

func (s *EventSubscription) Context() context.Context { return s.ctx }
func (s *EventSubscription) Done() <-chan struct{}    { return s.ctx.Done() }
func (s *EventSubscription) Close()                   { s.close() }

// eventListener owns the single shared LISTEN connection for a Store. It
// reconnects on its own; connection loss closes subscribers for a fresh snapshot.
type eventListener struct {
	store *Store

	ready     chan struct{}
	readyOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	stopOnce  sync.Once

	mu      sync.Mutex
	stopped bool
	waiters map[*eventWaiter]struct{}
}

// eventListener returns the Store's listener, starting it on first use.
func (s *Store) eventListener() *eventListener {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if s.events == nil {
		ctx, cancel := context.WithCancel(context.Background())
		l := &eventListener{
			store:   s,
			ready:   make(chan struct{}),
			cancel:  cancel,
			done:    make(chan struct{}),
			waiters: make(map[*eventWaiter]struct{}),
		}
		s.events = l
		go l.run(ctx)
	}
	return s.events
}

// waitReady blocks until the shared connection is listening, or ctx ends.
func (l *eventListener) waitReady(ctx context.Context) error {
	select {
	case <-l.ready:
		return nil
	case <-l.done:
		return errEventListenerStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *eventListener) add(w *eventWaiter) {
	l.mu.Lock()
	if l.stopped {
		w.cancel()
		close(w.events)
	} else {
		l.waiters[w] = struct{}{}
	}
	l.mu.Unlock()
}

func (l *eventListener) remove(w *eventWaiter) {
	l.mu.Lock()
	if _, ok := l.waiters[w]; ok {
		w.cancel()
		close(w.events)
		delete(l.waiters, w)
	}
	l.mu.Unlock()
}

func (l *eventListener) stop() {
	l.stopOnce.Do(func() {
		l.cancel()
		<-l.done
	})
}

// run keeps a LISTEN connection alive across server restarts and network
// errors. The first successful LISTEN closes ready.
func (l *eventListener) run(ctx context.Context) {
	defer close(l.done)
	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.stopped = true
		for w := range l.waiters {
			w.cancel()
			close(w.events)
			delete(l.waiters, w)
		}
	}()
	backoff := 100 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}
		config := l.store.Pool.Config().ConnConfig.Copy()
		if config.RuntimeParams == nil {
			config.RuntimeParams = make(map[string]string)
		}
		config.RuntimeParams["application_name"] = eventListenerApplicationName
		conn, err := pgx.ConnectConfig(ctx, config)
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+eventChannel)
		}
		if err != nil {
			if conn != nil {
				_ = conn.Close(context.Background())
			}
			if !sleepContext(ctx, backoff) {
				return
			}
			if backoff < 5*time.Second {
				backoff *= 2
				if backoff > 5*time.Second {
					backoff = 5 * time.Second
				}
			}
			continue
		}
		backoff = 100 * time.Millisecond
		l.mu.Lock()
		for w := range l.waiters {
			w.cancel()
			close(w.events)
			delete(l.waiters, w)
		}
		l.mu.Unlock()
		l.readyOnce.Do(func() { close(l.ready) })
		for {
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				break
			}
			l.dispatch(notification.Payload)
		}
		_ = conn.Close(context.Background())
		l.mu.Lock()
		for w := range l.waiters {
			w.cancel()
			close(w.events)
			delete(l.waiters, w)
		}
		l.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		// The server dropped the connection; reconnect after a short pause so a
		// permanent outage does not spin.
		if !sleepContext(ctx, 100*time.Millisecond) {
			return
		}
	}
}

func (l *eventListener) dispatch(payload string) {
	var ref EventRef
	if json.Unmarshal([]byte(payload), &ref) != nil || ref.Type == "" || ref.HiveID == "" || ref.ID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for w := range l.waiters {
		select {
		case w.events <- ref:
		default:
			w.cancel()
			close(w.events)
			delete(l.waiters, w)
		}
	}
}

// SubscribeEvents shares one LISTEN connection. Overflow closes the subscription.
func (s *Store) SubscribeEvents(ctx context.Context) (*EventSubscription, error) {
	l := s.eventListener()
	if err := l.waitReady(ctx); err != nil {
		return nil, err
	}
	subscriptionCtx, cancel := context.WithCancel(context.Background())
	w := &eventWaiter{events: make(chan EventRef, 256), ctx: subscriptionCtx, cancel: cancel}
	l.add(w)
	return &EventSubscription{Events: w.events, ctx: w.ctx, close: func() { l.remove(w) }}, nil
}

// PublishPREvent is used only for PR changes that have no persisted event row.
func (s *Store) PublishPREvent(ctx context.Context, hiveID string, pr int64) error {
	payload, err := json.Marshal(EventRef{Type: "pr", HiveID: hiveID, ID: fmt.Sprint(pr)})
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `SELECT pg_notify($1,$2)`, eventChannel, string(payload))
	return err
}

// PublishPRSnapshotEvent carries a hint that hydration can use the freshly
// persisted read view without making another GitHub request.
func (s *Store) PublishPRSnapshotEvent(ctx context.Context, hiveID string, pr int64) error {
	payload, err := json.Marshal(EventRef{Type: "pr", HiveID: hiveID, ID: fmt.Sprint(pr), Source: "snapshot"})
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `SELECT pg_notify($1,$2)`, eventChannel, string(payload))
	return err
}
