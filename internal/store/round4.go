package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// applySchemaUpgrade9 adds the round-4 individual-capability columns. Version 8
// belongs to the methodology task and may or may not be present on a given
// branch; the guard only checks this upgrade's own version so reviews can merge
// the migrations in the documented order.
func applySchemaUpgrade9(ctx context.Context, tx pgx.Tx, version int) error {
	if version >= 9 {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade9); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(9) ON CONFLICT DO NOTHING`)
	return err
}
