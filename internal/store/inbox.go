package store

import (
	"context"
	"errors"
	"time"
)

const WebhookInboxLimit = 1000

var ErrWebhookInboxFull = errors.New("webhook inbox full")

type WebhookDelivery struct {
	ID       int64
	Provider string
	Payload  []byte
}

// EnqueueWebhook commits the authenticated delivery before its HTTP ACK. The
// advisory lock makes the pending bound atomic across concurrent HTTP servers.
func (s *Store) EnqueueWebhook(ctx context.Context, provider, deliveryID, eventType string, payload []byte) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(84261306)"); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM webhook_inbox WHERE provider=$1 AND delivery_id=$2)", provider, deliveryID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	var pending int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM webhook_inbox WHERE processed_at IS NULL").Scan(&pending); err != nil {
		return err
	}
	if pending >= WebhookInboxLimit {
		return ErrWebhookInboxFull
	}
	if _, err = tx.Exec(ctx, "INSERT INTO webhook_inbox(provider,delivery_id,event_type,payload) VALUES($1,$2,$3,$4)", provider, deliveryID, eventType, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ClaimWebhookInbox leases a small batch. Expired leases are retried after a
// process crash; task creation remains idempotent in the task store.
func (s *Store) ClaimWebhookInbox(ctx context.Context, limit int) ([]WebhookDelivery, error) {
	rows, err := s.Pool.Query(ctx, `WITH batch AS (
		SELECT id FROM webhook_inbox WHERE processed_at IS NULL AND next_attempt_at <= now()
		AND (lease_until IS NULL OR lease_until < now()) ORDER BY next_attempt_at,id
		LIMIT $1 FOR UPDATE SKIP LOCKED
	) UPDATE webhook_inbox i SET lease_until=now()+interval '30 minutes', attempts=attempts+1
	FROM batch WHERE i.id=batch.id RETURNING i.id,i.provider,i.payload`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := make([]WebhookDelivery, 0, limit)
	for rows.Next() {
		var delivery WebhookDelivery
		if err = rows.Scan(&delivery.ID, &delivery.Provider, &delivery.Payload); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (s *Store) CompleteWebhookInbox(ctx context.Context, ids []int64) error {
	_, err := s.Pool.Exec(ctx, "UPDATE webhook_inbox SET processed_at=now(),lease_until=NULL WHERE id=ANY($1)", ids)
	return err
}

func (s *Store) RetryWebhookInbox(ctx context.Context, ids []int64, delay time.Duration) error {
	_, err := s.Pool.Exec(ctx, "UPDATE webhook_inbox SET lease_until=NULL,next_attempt_at=now()+$2::interval WHERE id=ANY($1)", ids, delay.String())
	return err
}

func (s *Store) PruneWebhookInbox(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, "DELETE FROM webhook_inbox WHERE processed_at < now()-interval '7 days'")
	return err
}
