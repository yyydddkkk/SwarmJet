package store

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5"
)

//go:embed schema_upgrade_15.sql
var schemaUpgrade15 string

func applySchemaUpgrade15(ctx context.Context, tx pgx.Tx) error {
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=15)`).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade15); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(15) ON CONFLICT DO NOTHING`)
	return err
}
