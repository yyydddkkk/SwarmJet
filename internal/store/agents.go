package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func (s *Store) EnabledAgents(ctx context.Context) ([]string, error) {
	var body []byte
	err := s.Pool.QueryRow(ctx, "SELECT body FROM platform_settings WHERE name='enabled_agents'").Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	err = json.Unmarshal(body, &ids)
	return ids, err
}

func (s *Store) SetEnabledAgents(ctx context.Context, ids []string) error {
	if err := domain.ValidateEnabledAgents(ids); err != nil {
		return err
	}
	if ids == nil {
		ids = []string{}
	}
	body, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, "INSERT INTO platform_settings(name,body) VALUES('enabled_agents',$1) ON CONFLICT(name) DO UPDATE SET body=EXCLUDED.body", body)
	return err
}
