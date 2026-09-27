package store

import (
	"context"
	"encoding/json"
	"time"
)

// CommandsForPublication includes unconfirmed messages and commands whose
// broker delivery may have been lost. A command stays eligible until the
// Temporal consumer marks it delivered.
func (s *Store) CommandsForPublication(ctx context.Context, resendAfter time.Duration) ([]PendingCommand, error) {
	if resendAfter <= 0 {
		resendAfter = 5 * time.Minute
	}
	rows, err := s.Pool.Query(ctx, `SELECT c.task_id,c.body FROM commands c WHERE NOT c.delivered
 AND (c.published_at IS NULL OR c.published_at < $1)
 AND (c.body->>'kind'='start' OR EXISTS(SELECT 1 FROM commands start_command WHERE start_command.id='start-'||c.task_id AND start_command.delivered))
 ORDER BY c.created_at,c.id LIMIT 100`, time.Now().Add(-resendAfter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingCommand{}
	for rows.Next() {
		var item PendingCommand
		var body []byte
		if err := rows.Scan(&item.TaskID, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &item.Command); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkCommandPublished follows an acknowledged and routed broker publication.
func (s *Store) MarkCommandPublished(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE commands SET published_at=now() WHERE id=$1 AND NOT delivered`, id)
	return err
}

// CommandForDelivery retrieves the authoritative command for a broker message.
func (s *Store) CommandForDelivery(ctx context.Context, id string) (PendingCommand, bool, error) {
	var item PendingCommand
	var body []byte
	var delivered bool
	err := s.Pool.QueryRow(ctx, `SELECT task_id,body,delivered FROM commands WHERE id=$1`, id).Scan(&item.TaskID, &body, &delivered)
	if err != nil {
		return item, false, mapErr(err)
	}
	err = json.Unmarshal(body, &item.Command)
	return item, delivered, err
}
