package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// inboxRecheckInterval bounds how long a missed LISTEN notification can delay
// a waiter. Notifications are best-effort; the messages table is the truth.
const inboxRecheckInterval = 2 * time.Second

// inboxListenerApplicationName marks the shared LISTEN connection in
// pg_stat_activity, which keeps it easy to find and to count in tests.
const inboxListenerApplicationName = "swarmjet-inbox-listener"

var errInboxListenerStopped = errors.New("store: inbox listener stopped")

// inboxWaiter is one blocked WaitInbox call. wake is buffered so the listener
// never blocks on a slow waiter.
type inboxWaiter struct {
	hiveID string
	to     domain.Address
	wake   chan struct{}
}

// inboxListener owns the single shared LISTEN connection for a Store. It
// reconnects on its own; waiters only see wake-ups and periodic re-checks.
type inboxListener struct {
	store *Store

	ready     chan struct{}
	readyOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	stopOnce  sync.Once

	mu      sync.Mutex
	waiters map[*inboxWaiter]struct{}
}

// inboxListener returns the Store's listener, starting it on first use.
func (s *Store) inboxListener() *inboxListener {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	if s.listener == nil {
		ctx, cancel := context.WithCancel(context.Background())
		l := &inboxListener{
			store:   s,
			ready:   make(chan struct{}),
			cancel:  cancel,
			done:    make(chan struct{}),
			waiters: make(map[*inboxWaiter]struct{}),
		}
		s.listener = l
		go l.run(ctx)
	}
	return s.listener
}

// waitReady blocks until the shared connection is listening, or ctx ends.
func (l *inboxListener) waitReady(ctx context.Context) error {
	select {
	case <-l.ready:
		return nil
	case <-l.done:
		return errInboxListenerStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *inboxListener) add(w *inboxWaiter) {
	l.mu.Lock()
	l.waiters[w] = struct{}{}
	l.mu.Unlock()
}

func (l *inboxListener) remove(w *inboxWaiter) {
	l.mu.Lock()
	delete(l.waiters, w)
	l.mu.Unlock()
}

func (l *inboxListener) stop() {
	l.stopOnce.Do(func() {
		l.cancel()
		<-l.done
	})
}

// run keeps a LISTEN connection alive across server restarts and network
// errors. The first successful LISTEN closes ready.
func (l *inboxListener) run(ctx context.Context) {
	defer close(l.done)
	backoff := 100 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}
		config := l.store.Pool.Config().ConnConfig.Copy()
		if config.RuntimeParams == nil {
			config.RuntimeParams = make(map[string]string)
		}
		config.RuntimeParams["application_name"] = inboxListenerApplicationName
		conn, err := pgx.ConnectConfig(ctx, config)
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+inboxChannel)
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
		l.readyOnce.Do(func() { close(l.ready) })
		for {
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				break
			}
			l.dispatch(notification.Payload)
		}
		_ = conn.Close(context.Background())
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

// dispatch wakes every waiter matching the notification payload. The payload
// is "hiveID\taddress"; a malformed payload is ignored because waiters re-check
// the table anyway.
func (l *inboxListener) dispatch(payload string) {
	hiveID, to, ok := strings.Cut(payload, "\t")
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for w := range l.waiters {
		if w.hiveID == hiveID && string(w.to) == to {
			select {
			case w.wake <- struct{}{}:
			default:
			}
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
