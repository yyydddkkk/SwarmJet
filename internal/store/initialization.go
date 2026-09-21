package store

import (
	"context"
	"encoding/json"
	"errors"

	"swarmjet/internal/domain"
)

// SaveInitialization upserts durable initialization state with optimistic
// concurrency. The caller sets init.Version to the next value; the update only
// succeeds when the stored version is exactly one lower. A concurrent console
// cancel or answer therefore makes an in-flight step's save fail instead of
// overwriting the newer state.
func (s *Store) SaveInitialization(ctx context.Context, init domain.Initialization) error {
	if init.Version < 1 {
		return errors.New("initialization version must be positive")
	}
	previous := init.Version - 1
	body, err := json.Marshal(init)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO initializations(id,project_id,status,body,updated_at) VALUES($1,$2,$3,$4,now())
		ON CONFLICT(id) DO UPDATE SET project_id=$2,status=$3,body=$4,updated_at=now()
		WHERE (initializations.body->>'version')::int = $5`, init.ID, init.ProjectID, init.Status, body, previous)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) Initialization(ctx context.Context, id string) (domain.Initialization, error) {
	var body []byte
	err := s.Pool.QueryRow(ctx, "SELECT body FROM initializations WHERE id=$1", id).Scan(&body)
	var init domain.Initialization
	if err == nil {
		err = json.Unmarshal(body, &init)
	}
	return init, mapErr(err)
}

func (s *Store) Initializations(ctx context.Context) ([]domain.Initialization, error) {
	rows, err := s.Pool.Query(ctx, "SELECT body FROM initializations ORDER BY updated_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Initialization{}
	for rows.Next() {
		var body []byte
		var init domain.Initialization
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &init); err != nil {
			return nil, err
		}
		out = append(out, init)
	}
	return out, rows.Err()
}
