package acp

import "sync"

// updateQueue decouples the JSON-RPC read loop from session/update consumers.
//
// The read loop must keep draining stdio: a response to session/prompt can only
// arrive after the notifications that precede it, and some notifications carry
// very large payloads. Pushing into this unbounded queue keeps the read loop
// non-blocking; a single dispatcher goroutine invokes the current sink.
type updateQueue struct {
	mu   sync.Mutex
	cond *sync.Cond

	items     []SessionNotification
	sink      UpdateSink
	enqueued  uint64
	delivered uint64
	stopped   bool
}

func newUpdateQueue() *updateQueue {
	q := &updateQueue{}
	q.cond = sync.NewCond(&q.mu)
	go q.run()
	return q
}

// push appends one notification. It never blocks on the sink.
func (q *updateQueue) push(n SessionNotification) {
	q.mu.Lock()
	if !q.stopped {
		q.items = append(q.items, n)
		q.enqueued++
		q.cond.Signal()
	}
	q.mu.Unlock()
}

// setSink installs the consumer for subsequent notifications. Holding the lock
// ensures the dispatcher observes a consistent sink for each update.
func (q *updateQueue) setSink(sink UpdateSink) {
	q.mu.Lock()
	q.sink = sink
	q.mu.Unlock()
}

// flush waits until every notification enqueued before the call has been
// handed to the sink. It is used when a prompt or session load ends: the
// response is ordered after the turn's notifications, so at that point the
// queue contains the complete turn.
func (q *updateQueue) flush() {
	q.mu.Lock()
	target := q.enqueued
	for q.delivered < target && !q.stopped {
		q.cond.Wait()
	}
	q.mu.Unlock()
}

// stop ends dispatch. Notifications still queued are dropped; the transport is
// being torn down and no caller is waiting for them.
func (q *updateQueue) stop() {
	q.mu.Lock()
	q.stopped = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *updateQueue) run() {
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.stopped {
			q.cond.Wait()
		}
		if q.stopped {
			// The transport is gone; no consumer is waiting for these.
			q.items = nil
			q.mu.Unlock()
			return
		}
		n := q.items[0]
		q.items = q.items[1:]
		sink := q.sink
		q.mu.Unlock()

		if sink != nil {
			// A panicking sink must not kill the dispatcher and strand later
			// updates; the sink is consumer-owned code.
			func() {
				defer func() { _ = recover() }()
				_ = sink(n)
			}()
		}

		q.mu.Lock()
		q.delivered++
		q.cond.Broadcast()
		q.mu.Unlock()
	}
}
