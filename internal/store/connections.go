package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"swarmjet/internal/domain"
)

// SaveConnection stores only connection metadata in the body column; the sealed
// credential lives in a separate column so listing endpoints cannot leak it.
func (s *Store) SaveConnection(ctx context.Context, c domain.Connection, secret string) (domain.Connection, error) {
	if err := domain.ValidateConnection(c); err != nil {
		return c, err
	}
	if secret == "" {
		return c, errors.New("encrypted connection secret is required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	var version int
	err = tx.QueryRow(ctx, "SELECT version FROM connections WHERE id=$1 FOR UPDATE", c.ID).Scan(&version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	if c.Version != version {
		return c, ErrConflict
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.Version = version + 1
	body, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if version == 0 {
		_, err = tx.Exec(ctx, "INSERT INTO connections(id,version,body,secret) VALUES($1,$2,$3,$4)", c.ID, c.Version, body, secret)
	} else {
		_, err = tx.Exec(ctx, "UPDATE connections SET version=$2,body=$3,secret=$4,updated_at=now() WHERE id=$1", c.ID, c.Version, body, secret)
	}
	if err != nil {
		return c, mapErr(err)
	}
	return c, tx.Commit(ctx)
}

func (s *Store) Connection(ctx context.Context, id string) (domain.Connection, error) {
	var body []byte
	err := s.Pool.QueryRow(ctx, "SELECT body FROM connections WHERE id=$1", id).Scan(&body)
	var c domain.Connection
	if err == nil {
		err = json.Unmarshal(body, &c)
	}
	return c, mapErr(err)
}

func (s *Store) Connections(ctx context.Context) ([]domain.Connection, error) {
	rows, err := s.Pool.Query(ctx, "SELECT body FROM connections ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Connection{}
	for rows.Next() {
		var body []byte
		var c domain.Connection
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConnectionSecrets returns the sealed credential payload only.
func (s *Store) ConnectionSecrets(ctx context.Context, id string) (string, error) {
	var secret string
	err := s.Pool.QueryRow(ctx, "SELECT secret FROM connections WHERE id=$1", id).Scan(&secret)
	return secret, mapErr(err)
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, "DELETE FROM connections WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
