package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

//go:embed schema_upgrade_12.sql
var schemaUpgrade12 string

func applySchemaUpgrade12(ctx context.Context, tx pgx.Tx, version int) error {
	if version >= 12 {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade12); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(12) ON CONFLICT DO NOTHING`)
	return err
}

// transitionMethodologyOutcome serializes terminal outcomes with methodology
// activation using the same Hive lock as PublishMethodology. The state change,
// rollback audit and notification either all commit, or all remain unchanged.
func (s *Store) transitionMethodologyOutcome(ctx context.Context, id string, from, to domain.AgentState) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hiveID, role string
	if err := tx.QueryRow(ctx, `SELECT hive_id,role FROM agents WHERE id=$1`, id).Scan(&hiveID, &role); err != nil {
		return domainErr(err)
	}
	// NO KEY UPDATE still serializes with activation while permitting the
	// Hive foreign-key checks of a concurrent SendMessage holding inbox lock.
	var queenEnabled bool
	if err := tx.QueryRow(ctx, `SELECT queen_enabled FROM hives WHERE id=$1 FOR NO KEY UPDATE`, hiveID).Scan(&queenEnabled); err != nil {
		return domainErr(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE agents SET state=$3,updated_at=clock_timestamp() WHERE id=$1 AND state=$2`, id, string(from), string(to))
	if err != nil {
		return domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConflict
	}
	if role == string(domain.RoleForager) && from != to {
		if err := s.recordMethodologyOutcome(ctx, tx, hiveID, to, queenEnabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) recordMethodologyOutcome(ctx context.Context, tx pgx.Tx, hiveID string, outcome domain.AgentState, queenEnabled bool) error {
	if outcome == domain.AgentCompleted {
		_, err := tx.Exec(ctx, `UPDATE methodologies SET consecutive_failures=0 WHERE hive_id=$1 AND active`, hiveID)
		return err
	}
	if outcome != domain.AgentFailed {
		return nil
	}
	var failedID string
	var failedVersion, failures int
	err := tx.QueryRow(ctx, `UPDATE methodologies SET consecutive_failures=consecutive_failures+1
		WHERE hive_id=$1 AND active AND auto_rolled_back_at IS NULL
		RETURNING id,version,consecutive_failures`, hiveID).Scan(&failedID, &failedVersion, &failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || failures < 3 {
		return err
	}
	var previousID string
	var previousVersion int
	err = tx.QueryRow(ctx, `SELECT id,version FROM methodologies
		WHERE hive_id=$1 AND id<>$2 AND activated_at IS NOT NULL AND auto_rolled_back_at IS NULL
		ORDER BY activated_at DESC,version DESC LIMIT 1`, hiveID, failedID).Scan(&previousID, &previousVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // The first installation has no earlier version to restore.
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE methodologies SET active=false,auto_rolled_back_at=clock_timestamp() WHERE id=$1`, failedID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE methodologies SET active=true WHERE id=$1`, previousID); err != nil {
		return err
	}
	note := fmt.Sprintf("Automatically rolled back methodology version %d to version %d after 3 consecutive Forager failures since activation.", failedVersion, previousVersion)
	if err = insertMethodologyEvent(ctx, tx, hiveID, methodology.ActionActivate, previousVersion, domain.AddressSystem, note); err != nil {
		return err
	}
	to := domain.AddressHive
	if queenEnabled {
		to = domain.AddressQueen
	}
	messageID, err := newMessageID()
	if err != nil {
		return err
	}
	// Match SendMessage's per-Hive ordering lock and delivery contract.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "inbox:"+hiveID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO messages(id,hive_id,from_addr,to_addr,kind,body,trust,state)
		VALUES($1,$2,'system',$3,'event',$4,'system','pending')`, messageID, hiveID, string(to), note); err != nil {
		return err
	}
	if err = s.insertMessageJob(ctx, tx, messageID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1,$2)`, inboxChannel, notifyPayload(hiveID, to))
	return err
}
