package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"swarmjet/internal/domain"
)

// enqueueStoredNote keeps notes on tasks that have no running Workflow.
// The allocation lock serializes this choice with admission and cancellation.
func (s *Store) enqueueStoredNote(ctx context.Context, taskID string, c domain.Command) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(84261302)"); err != nil {
		return false, err
	}
	var raw []byte
	var stage string
	if err = tx.QueryRow(ctx, "SELECT body,stage FROM tasks WHERE id=$1 FOR UPDATE", taskID).Scan(&raw, &stage); err != nil {
		return false, mapErr(err)
	}
	var task domain.Task
	if err = json.Unmarshal(raw, &task); err != nil {
		return false, err
	}
	var started bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM commands WHERE id=$1)", "start-"+taskID).Scan(&started); err != nil {
		return false, err
	}
	if !domain.Terminal(stage) && !(stage == "queued" && task.AdmissionStatus == "waiting" && !started) {
		return false, tx.Commit(ctx)
	}
	body, err := json.Marshal(c)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, "INSERT INTO commands(id,task_id,body,delivered) VALUES($1,$2,$3,true) ON CONFLICT DO NOTHING", c.ID, taskID, body)
	if err != nil {
		return false, mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		var existingTask string
		var existingBody []byte
		if err = tx.QueryRow(ctx, "SELECT task_id,body FROM commands WHERE id=$1", c.ID).Scan(&existingTask, &existingBody); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, ErrConflict
			}
			return false, err
		}
		var existing domain.Command
		if err = json.Unmarshal(existingBody, &existing); err != nil {
			return false, err
		}
		canonical, err := json.Marshal(existing)
		if err != nil {
			return false, err
		}
		if existingTask != taskID || string(canonical) != string(body) {
			return false, ErrConflict
		}
		return true, tx.Commit(ctx)
	}
	task.Notes = append(task.Notes, c.Text)
	task.UpdatedAt = time.Now().UTC()
	updated, err := json.Marshal(task)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE tasks SET body=$2,updated_at=now() WHERE id=$1", taskID, updated); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
