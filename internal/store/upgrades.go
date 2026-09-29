package store

import (
	"context"
	"encoding/json"
	"time"
)

// UpgradeRecord is the durable projection shared by the console and the
// upgrade coordinator. The body is versioned by the application, while these
// columns support global admission and one-at-a-time execution.
type UpgradeRecord struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Active      bool            `json:"active"`
	Maintenance bool            `json:"maintenance"`
	Body        json.RawMessage `json:"body"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

func (s *Store) CreateUpgrade(ctx context.Context, id, status string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO platform_upgrades(id,status,body) VALUES($1,$2,$3)`, id, status, raw)
	return mapErr(err)
}

func (s *Store) SaveUpgrade(ctx context.Context, id, status string, active, maintenance bool, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	command, err := s.Pool.Exec(ctx, `UPDATE platform_upgrades SET status=$2,active=$3,maintenance=$4,body=$5,updated_at=now() WHERE id=$1`, id, status, active, maintenance, raw)
	if err != nil {
		return mapErr(err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Upgrade(ctx context.Context, id string) (UpgradeRecord, error) {
	var record UpgradeRecord
	err := s.Pool.QueryRow(ctx, `SELECT id,status,active,maintenance,body,created_at,updated_at FROM platform_upgrades WHERE id=$1`, id).Scan(
		&record.ID, &record.Status, &record.Active, &record.Maintenance, &record.Body, &record.CreatedAt, &record.UpdatedAt)
	return record, mapErr(err)
}

func (s *Store) ActiveUpgrade(ctx context.Context) (UpgradeRecord, error) {
	var record UpgradeRecord
	err := s.Pool.QueryRow(ctx, `SELECT id,status,active,maintenance,body,created_at,updated_at FROM platform_upgrades WHERE active ORDER BY created_at DESC LIMIT 1`).Scan(
		&record.ID, &record.Status, &record.Active, &record.Maintenance, &record.Body, &record.CreatedAt, &record.UpdatedAt)
	return record, mapErr(err)
}

func (s *Store) Upgrades(ctx context.Context, limit int) ([]UpgradeRecord, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,status,active,maintenance,body,created_at,updated_at FROM platform_upgrades ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UpgradeRecord{}
	for rows.Next() {
		var record UpgradeRecord
		if err = rows.Scan(&record.ID, &record.Status, &record.Active, &record.Maintenance, &record.Body, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) UpgradeMaintenance(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_upgrades WHERE maintenance)`).Scan(&enabled)
	return enabled, err
}

// UpgradeDrain counts work that can still perform writes or run an Agent.
// Approval, reply and dependency waits remain stored across the upgrade.
func (s *Store) UpgradeDrain(ctx context.Context) (int, error) {
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM tasks WHERE `+activeProjectTask+`)
	 +(SELECT count(*) FROM initializations WHERE status IN ('pending','running') OR (status='ready' AND body#>>'{publication,status}'='pending'))
	 +(SELECT count(*) FROM leases WHERE name LIKE 'repository-change-%' AND expires_at>now())`).Scan(&count)
	return count, err
}
