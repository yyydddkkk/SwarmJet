package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func (s *Store) SaveRepositoryChange(ctx context.Context, change domain.RepositoryChange) error {
	if change.Version < 1 {
		return errors.New("repository change version must be positive")
	}
	body, err := json.Marshal(change)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO repository_changes(id,project_id,status,body,updated_at) VALUES($1,$2,$3,$4,now())
		ON CONFLICT(id) DO UPDATE SET status=$3,body=$4,updated_at=now()
		WHERE (repository_changes.body->>'version')::int=$5`, change.ID, change.ProjectID, change.Status, body, change.Version-1)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) RepositoryChange(ctx context.Context, id string) (domain.RepositoryChange, error) {
	var body []byte
	err := s.Pool.QueryRow(ctx, `SELECT body FROM repository_changes WHERE id=$1`, id).Scan(&body)
	var change domain.RepositoryChange
	if err == nil {
		err = json.Unmarshal(body, &change)
	}
	return change, mapErr(err)
}

func (s *Store) RepositoryChanges(ctx context.Context, projectID string) ([]domain.RepositoryChange, error) {
	var rows pgx.Rows
	var err error
	if projectID == "" {
		rows, err = s.Pool.Query(ctx, `SELECT body FROM repository_changes WHERE status IN ('pending','waiting') ORDER BY updated_at`)
	} else {
		rows, err = s.Pool.Query(ctx, `SELECT body FROM repository_changes WHERE project_id=$1 ORDER BY updated_at DESC`, projectID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RepositoryChange{}
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var change domain.RepositoryChange
		if err = json.Unmarshal(body, &change); err != nil {
			return nil, err
		}
		out = append(out, change)
	}
	return out, rows.Err()
}
