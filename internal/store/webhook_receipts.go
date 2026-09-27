package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrWebhookConflict = errors.New("webhook delivery ID reused with different content")

// ProcessWebhook serializes deliveries with the same ID across API instances.
// The receipt is committed only after processing succeeds. A crash between
// processing and receipt insertion may rescan, which task uniqueness tolerates.
func (s *Store) ProcessWebhook(ctx context.Context, provider, deliveryID, eventType string, payload []byte, process func() error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	key := provider + ":" + deliveryID
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", key); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", key); unlockErr != nil {
			// A connection whose lock state is uncertain must not re-enter the pool.
			conn.Conn().Close(unlockCtx)
		}
	}()
	hash := sha256.Sum256(append(append([]byte(eventType), 0), payload...))
	var existing []byte
	err = conn.QueryRow(ctx, "SELECT content_hash FROM webhook_receipts WHERE provider=$1 AND delivery_id=$2", provider, deliveryID).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, hash[:]) {
			return ErrWebhookConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err = process(); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, "INSERT INTO webhook_receipts(provider,delivery_id,content_hash) VALUES($1,$2,$3)", provider, deliveryID, hash[:])
	if err != nil {
		return fmt.Errorf("record webhook receipt: %w", err)
	}
	return nil
}
