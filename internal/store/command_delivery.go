package store

import (
	"context"
	"encoding/json"
)

// StartDelivered reports whether the task's fixed start command has reached Temporal.
func (s *Store) StartDelivered(ctx context.Context, taskID string) (bool, error) {
	var delivered bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commands WHERE id=$1 AND delivered)`, "start-"+taskID).Scan(&delivered)
	return delivered, err
}

// CommandForDelivery retrieves the authoritative command for a queued job.
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
