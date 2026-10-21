package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

const eventBufferSize = 32

type streamEvent struct {
	hiveID string
	frame  []byte
}
type eventClient struct {
	hiveID string
	events chan streamEvent
	done   chan struct{}
}
type eventHub struct {
	mu       sync.Mutex
	clients  map[*eventClient]struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func (h *eventHub) add(hiveID string) *eventClient {
	c := &eventClient{hiveID: hiveID, events: make(chan streamEvent, eventBufferSize), done: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.done:
		close(c.done)
	default:
		h.clients[c] = struct{}{}
	}
	return c
}
func (h *eventHub) remove(c *eventClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.done)
	}
}
func (h *eventHub) broadcast(e streamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.hiveID != "" && c.hiveID != e.hiveID {
			continue
		}
		select {
		case c.events <- e:
		default:
			delete(h.clients, c)
			close(c.done)
		}
	}
}
func (h *eventHub) stop() {
	h.stopOnce.Do(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		close(h.done)
		for c := range h.clients {
			delete(h.clients, c)
			close(c.done)
		}
	})
}

func (a *server) eventHub(ctx context.Context) (*eventHub, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.events != nil {
		select {
		case <-a.events.done:
		default:
			return a.events, nil
		}
	}
	subscription, err := a.s.SubscribeEvents(ctx)
	if err != nil {
		return nil, err
	}
	h := &eventHub{clients: make(map[*eventClient]struct{}), done: make(chan struct{})}
	a.events = h
	go func() {
		select {
		case <-subscription.Done():
			h.stop()
		case <-h.done:
		}
	}()
	go func() {
		defer subscription.Close()
		defer h.stop()
		for {
			select {
			case <-subscription.Done():
				return
			default:
			}
			var ref store.EventRef
			select {
			case <-subscription.Done():
				return
			case next, ok := <-subscription.Events:
				if !ok {
					return
				}
				ref = next
			}
			ctx, cancel := context.WithTimeout(subscription.Context(), 10*time.Second)
			data, err := a.eventData(ctx, ref)
			cancel()
			// Other database schemas share NOTIFY channels; never hydrate their rows.
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			// A failed hydration creates an unknown gap; reconnecting forces a snapshot.
			if err != nil {
				return
			}
			encoded, err := json.Marshal(data)
			if err != nil {
				return
			}
			h.broadcast(streamEvent{hiveID: ref.HiveID, frame: []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", ref.Type, encoded))})
		}
	}()
	return h, nil
}

func (a *server) eventData(ctx context.Context, ref store.EventRef) (any, error) {
	if ref.Type == "pr" {
		number, err := strconv.ParseInt(ref.ID, 10, 64)
		if err != nil {
			return nil, err
		}
		snapshots, err := a.s.PRSnapshots(ctx, ref.HiveID)
		if err != nil {
			return nil, err
		}
		if snapshot, ok := snapshots[number]; ok && (ref.Source == "snapshot" || snapshot.State == "closed") {
			a.applyPRSnapshot(snapshot)
			return map[string]any{"hiveId": ref.HiveID, "number": number, "headSha": snapshot.HeadSHA, "state": snapshot.State, "ready": snapshot.Ready, "merged": snapshot.Merged}, nil
		}
		if ref.Source == "snapshot" {
			return nil, store.ErrNotFound
		}
		hive, err := a.s.Hive(ctx, ref.HiveID)
		if err != nil {
			return nil, err
		}
		if a.cfg.Merge == nil {
			return nil, fmt.Errorf("pull request status unavailable")
		}
		readStartedAt := time.Now().UTC()
		ready, err := a.cfg.Merge.Check(ctx, hive, number)
		if err != nil {
			return nil, err
		}
		_, err = a.s.SavePRSnapshot(ctx, store.PRSnapshot{HiveID: ref.HiveID, Number: number, Title: ready.Pull.Title, State: ready.Pull.State, Merged: ready.Pull.Merged, ClosedAt: ready.Pull.ClosedAt, URL: ready.Pull.URL, HeadSHA: ready.HeadSHA, UpdatedAt: ready.Pull.UpdatedAt, ReadStartedAt: readStartedAt, Ready: ready.Ready, Reasons: ready.Reasons})
		if err != nil {
			return nil, err
		}
		// A webhook may have won while the remote read was in flight. Hydrate
		// the persisted winner rather than publishing the superseded result.
		snapshots, err = a.s.PRSnapshots(ctx, ref.HiveID)
		if err != nil {
			return nil, err
		}
		snapshot, ok := snapshots[number]
		if !ok {
			return nil, store.ErrNotFound
		}
		a.applyPRSnapshot(snapshot)
		return map[string]any{"hiveId": ref.HiveID, "number": number, "headSha": snapshot.HeadSHA, "state": snapshot.State, "ready": snapshot.Ready, "merged": snapshot.Merged}, nil
	}
	data, err := a.s.EventData(ctx, ref)
	if err != nil {
		return nil, err
	}
	if agent, ok := data.(domain.Agent); ok {
		agents, err := a.s.AgentsWithCosts(ctx, []domain.Agent{agent})
		if err != nil {
			return nil, err
		}
		return agents[0], nil
	}
	return data, nil
}

func (a *server) applyPRSnapshot(snapshot store.PRSnapshot) {
	a.prCacheMu.Lock()
	defer a.prCacheMu.Unlock()
	for key := range a.prCache {
		if key.hive == snapshot.HiveID && key.number == snapshot.Number {
			delete(a.prCache, key)
		}
	}
	entry, ok := a.prLists[snapshot.HiveID]
	if !ok {
		return
	}
	items := append([]prListItem(nil), entry.prs...)
	for i := range items {
		if items[i].Number == snapshot.Number {
			items[i].Title, items[i].URL, items[i].HeadSHA, items[i].State, items[i].Merged, items[i].Ready, items[i].Reasons, items[i].UpdatedAt, items[i].ClosedAt = snapshot.Title, snapshot.URL, snapshot.HeadSHA, snapshot.State, snapshot.Merged, snapshot.Ready, snapshot.Reasons, snapshot.UpdatedAt, snapshot.ClosedAt
			items[i].Unavailable = false
			entry.prs = items
			a.prLists[snapshot.HiveID] = entry
			return
		}
	}
	delete(a.prLists, snapshot.HiveID)
}

func (a *server) eventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.s == nil {
		problem(w, http.StatusServiceUnavailable, "event storage unavailable")
		return
	}
	hiveID := r.URL.Query().Get("hive")
	if hiveID != "" {
		if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
			hiveFailure(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	h, err := a.eventHub(ctx)
	cancel()
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}
	c := h.add(hiveID)
	defer h.remove(c)
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	// Each write has a bounded deadline. This also overrides a server-wide
	// WriteTimeout only for this long-lived route.
	write := func(frame []byte) bool {
		if controller.SetWriteDeadline(time.Now().Add(10*time.Second)) != nil {
			return false
		}
		if _, err := w.Write(frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	defer controller.SetWriteDeadline(time.Time{})
	if !write([]byte("event: ready\ndata: {}\n\n")) {
		return
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.done:
			return
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case e := <-c.events:
			if !write(e.frame) {
				return
			}
		case <-heartbeat.C:
			if !write([]byte(": ping\n\n")) {
				return
			}
		}
	}
}
