package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// WebhookMessage is the stable payload of the durable webhook queue.
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

// ProcessWebhookDelivery is called by a manual-ACK RabbitMQ consumer. A nil
// result means the receipt is durable and the delivery can be ACKed.
func (s *Service) ProcessWebhookDelivery(ctx context.Context, body []byte) error {
	return s.ProcessWebhookDeliveries(ctx, [][]byte{body})[0]
}

// ProcessWebhookDeliveries coalesces a broker batch into one scan per target.
// Results align with bodies so the consumer can ACK or retry each delivery.
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
			results[i] = s.Store.ProcessWebhook(ctx, event.Provider, event.DeliveryID, event.EventType, event.Payload, func() error {
				if scanned || scanErr != nil {
					return scanErr
				}
				scanCtx := context.WithValue(ctx, webhookDeliveryContextKey{}, webhookDeliveryContext{provider: event.Provider, deliveryID: event.DeliveryID})
				if key.repoID == 0 {
					scanErr = s.Scan(scanCtx)
				} else {
					provider, _ := webhookRepository(key.provider, event.Payload)
					scanErr = s.ScanRepository(scanCtx, provider, key.repoID)
				}
				scanned = scanErr == nil
				return scanErr
			})
			if errors.Is(results[i], store.ErrWebhookConflict) {
				slog.Error("webhook delivery ID content mismatch", "provider", event.Provider, "delivery_id", event.DeliveryID, "repository_id", key.repoID, "error", results[i])
			} else if results[i] == nil {
				slog.Info("webhook delivery processed", "provider", event.Provider, "delivery_id", event.DeliveryID, "repository_id", key.repoID)
			} else {
				slog.Warn("webhook delivery processing failed", "provider", event.Provider, "delivery_id", event.DeliveryID, "repository_id", key.repoID, "error", results[i])
			}
		}
	}
	return results
}
