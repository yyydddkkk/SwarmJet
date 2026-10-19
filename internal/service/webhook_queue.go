package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// WebhookMessage is the stable payload of a webhook scan.
type WebhookMessage struct {
	Provider   string `json:"provider"`
	DeliveryID string `json:"deliveryId"`
	EventType  string `json:"eventType"`
	Payload    []byte `json:"payload"`
}

var ErrInvalidWebhookDelivery = errors.New("invalid webhook queue message")

type webhookDeliveryContextKey struct{}

type webhookDeliveryContext struct {
	provider   string
	deliveryID string
}

type scanGroup struct {
	done chan struct{}
	err  error
}

// scanCoalescer runs one repository scan for jobs that arrive inside the window.
type scanCoalescer struct {
	window time.Duration
	mu     sync.Mutex
	groups map[string]*scanGroup
}

func (c *scanCoalescer) Do(ctx context.Context, key string, fn func(context.Context) error) error {
	c.mu.Lock()
	if existing, ok := c.groups[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-existing.done:
			return existing.err
		}
	}
	group := &scanGroup{done: make(chan struct{})}
	c.groups[key] = group
	c.mu.Unlock()
	timer := time.NewTimer(c.window)
	select {
	case <-ctx.Done():
		timer.Stop()
		c.finish(key, group, ctx.Err())
		return ctx.Err()
	case <-timer.C:
	}
	err := fn(ctx)
	c.finish(key, group, err)
	return err
}

func (c *scanCoalescer) finish(key string, group *scanGroup, err error) {
	c.mu.Lock()
	group.err = err
	if c.groups[key] == group {
		delete(c.groups, key)
	}
	c.mu.Unlock()
	close(group.done)
}

func (s *Service) repositoryScans() *scanCoalescer {
	s.scanGroupOnce.Do(func() {
		s.webhookScans = &scanCoalescer{window: 20 * time.Millisecond, groups: map[string]*scanGroup{}}
	})
	return s.webhookScans
}

// HandleWebhookJob scans the delivery's repository. Concurrent jobs for the
// same repository share one scan. Admission pressure is retried without
// consuming a job attempt.
func (s *Service) HandleWebhookJob(ctx context.Context, job jobs.Webhook) error {
	if (job.Provider != "gitlab" && job.Provider != "github") || job.DeliveryID == "" || len(job.DeliveryID) > 200 {
		return fmt.Errorf("%w: %w", jobs.ErrCancel, ErrInvalidWebhookDelivery)
	}
	// A repository that belongs to a Hive is routed deterministically and never
	// reaches the legacy scanner; everything else keeps the old behaviour.
	if decision, routed, err := s.routeHiveDelivery(ctx, job); routed {
		logHiveDelivery(job, decision, err)
		return err
	}
	kind, repoID := webhookRepository(job.Provider, job.Payload)
	key := job.Provider + ":" + strconv.FormatInt(repoID, 10)
	err := s.repositoryScans().Do(ctx, key, func(ctx context.Context) error {
		scanCtx := context.WithValue(ctx, webhookDeliveryContextKey{}, webhookDeliveryContext{provider: job.Provider, deliveryID: job.DeliveryID})
		var scanErr error
		if repoID == 0 {
			scanErr = s.Scan(scanCtx)
		} else {
			scanErr = s.ScanRepository(scanCtx, kind, repoID)
		}
		if errors.Is(scanErr, store.ErrAdmissionQueueFull) {
			return fmt.Errorf("%w: %w", jobs.ErrSnooze, scanErr)
		}
		return scanErr
	})
	if errors.Is(err, store.ErrWebhookConflict) || errors.Is(err, ErrInvalidWebhookDelivery) {
		slog.Error("webhook delivery failed", "provider", job.Provider, "delivery_id", job.DeliveryID, "repository_id", repoID, "error", err)
	} else if err == nil {
		slog.Info("webhook delivery processed", "provider", job.Provider, "delivery_id", job.DeliveryID, "repository_id", repoID)
	} else if !errors.Is(err, jobs.ErrSnooze) {
		slog.Warn("webhook delivery processing failed", "provider", job.Provider, "delivery_id", job.DeliveryID, "repository_id", repoID, "error", err)
	}
	return err
}

// ProcessWebhookDelivery scans one queued webhook message.
func (s *Service) ProcessWebhookDelivery(ctx context.Context, body []byte) error {
	return s.ProcessWebhookDeliveries(ctx, [][]byte{body})[0]
}

// ProcessWebhookDeliveries scans once per repository in the batch. Results
// align with bodies. Receipts are recorded when the delivery is accepted.
func (s *Service) ProcessWebhookDeliveries(ctx context.Context, bodies [][]byte) []error {
	results := make([]error, len(bodies))
	type target struct {
		provider string
		repoID   int64
	}
	groups := make(map[target][]int)
	events := make([]WebhookMessage, len(bodies))
	for i, body := range bodies {
		var event WebhookMessage
		if err := json.Unmarshal(body, &event); err != nil {
			results[i] = fmt.Errorf("%w: %v", ErrInvalidWebhookDelivery, err)
			continue
		}
		if (event.Provider != "gitlab" && event.Provider != "github") || event.DeliveryID == "" || len(event.DeliveryID) > 200 || event.Payload == nil {
			results[i] = ErrInvalidWebhookDelivery
			continue
		}
		_, repoID := webhookRepository(event.Provider, event.Payload)
		events[i] = event
		groups[target{event.Provider, repoID}] = append(groups[target{event.Provider, repoID}], i)
	}
	if s.Store == nil {
		for i := range results {
			if results[i] == nil {
				results[i] = errors.New("webhook store unavailable")
			}
		}
		return results
	}
	for key, ids := range groups {
		var scanErr error
		scanned := false
		for _, i := range ids {
			event := events[i]
			if scanned || scanErr != nil {
				results[i] = scanErr
			} else {
				scanCtx := context.WithValue(ctx, webhookDeliveryContextKey{}, webhookDeliveryContext{provider: event.Provider, deliveryID: event.DeliveryID})
				if key.repoID == 0 {
					scanErr = s.Scan(scanCtx)
				} else {
					provider, _ := webhookRepository(key.provider, event.Payload)
					scanErr = s.ScanRepository(scanCtx, provider, key.repoID)
				}
				scanned = scanErr == nil
				results[i] = scanErr
			}
			if results[i] == nil {
				slog.Info("webhook delivery processed", "provider", event.Provider, "delivery_id", event.DeliveryID, "repository_id", key.repoID)
			} else {
				slog.Warn("webhook delivery processing failed", "provider", event.Provider, "delivery_id", event.DeliveryID, "repository_id", key.repoID, "error", results[i])
			}
		}
	}
	return results
}
