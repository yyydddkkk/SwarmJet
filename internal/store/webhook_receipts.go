package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

var ErrWebhookConflict = errors.New("webhook delivery ID reused with different content")

// AcceptWebhook records a delivery and queues its scan in one transaction.
// The same provider and delivery ID with the same content is a no-op. A
// different payload for that ID returns ErrWebhookConflict and queues nothing.
func (s *Store) AcceptWebhook(ctx context.Context, provider, deliveryID, eventType string, payload []byte) error {
	if s.Jobs == nil {
		return errors.New("webhook queue unavailable")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	key := provider + ":" + deliveryID
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", key); err != nil {
		return err
	}
	hash := sha256.Sum256(append(append([]byte(eventType), 0), payload...))
	var existing []byte
	err = tx.QueryRow(ctx, "SELECT content_hash FROM webhook_receipts WHERE provider=$1 AND delivery_id=$2", provider, deliveryID).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, hash[:]) {
			return ErrWebhookConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO webhook_receipts(provider,delivery_id,content_hash) VALUES($1,$2,$3)", provider, deliveryID, hash[:]); err != nil {
		return fmt.Errorf("record webhook receipt: %w", err)
	}
	if err = s.Jobs.InsertWebhook(ctx, tx, jobs.Webhook{Provider: provider, DeliveryID: deliveryID, EventType: eventType, Payload: payload}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
