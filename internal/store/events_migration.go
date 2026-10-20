package store

import (
	"context"
	_ "embed"
	"github.com/jackc/pgx/v5"
)

//go:embed schema_upgrade_14.sql
var schemaUpgrade14 string

func applySchemaUpgrade14(ctx context.Context, tx pgx.Tx, version int) error {
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=14)`).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade14); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(14) ON CONFLICT DO NOTHING`)
	return err
}
