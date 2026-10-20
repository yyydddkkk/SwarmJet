package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

//go:embed schema_upgrade_8.sql
var schemaUpgrade8 string

// applySchemaUpgrade8 installs the versioned methodology tables. It is kept in
// this file so the round-4 change to Migrate is a single call.
func applySchemaUpgrade8(ctx context.Context, tx pgx.Tx, version int) error {
	if version >= 8 {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade8); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(8) ON CONFLICT DO NOTHING`)
	return err
}

// Methodology version storage. A version is immutable once published; the
// single active row is moved in one transaction so concurrent publishes and
// activations can never leave a Hive without exactly one active version (or
// with two).

const methodologyColumns = `id,hive_id,version,active,source,manifest,files,digest,note,created_by,created_at`

func scanMethodology(row pgx.Row) (domain.Methodology, []domain.MethodologyFile, error) {
	var m domain.Methodology
	var source, manifest, files []byte
	var createdBy string
	if err := row.Scan(&m.ID, &m.HiveID, &m.Version, &m.Active, &source, &manifest, &files,
		&m.Digest, &m.Note, &createdBy, &m.CreatedAt); err != nil {
		return domain.Methodology{}, nil, domainErr(err)
	}
	m.CreatedBy = domain.Address(createdBy)
	if len(source) > 0 {
		var s domain.MethodologySource
		if err := json.Unmarshal(source, &s); err != nil {
			return domain.Methodology{}, nil, err
		}
		m.Source = &s
	}
	if len(manifest) > 0 {
		if err := json.Unmarshal(manifest, &m.Manifest); err != nil {
			return domain.Methodology{}, nil, err
		}
	}
	var versionFiles []domain.MethodologyFile
	if len(files) > 0 {
		if err := json.Unmarshal(files, &versionFiles); err != nil {
			return domain.Methodology{}, nil, err
		}
	}
	return m, versionFiles, nil
}

func insertMethodologyEvent(ctx context.Context, tx pgx.Tx, hiveID, action string, version int, actor domain.Address, note string) error {
	_, err := tx.Exec(ctx, `INSERT INTO methodology_events(hive_id,action,version,note,actor) VALUES($1,$2,$3,$4,$5)`,
		hiveID, action, version, note, string(actor))
	return domainErr(err)
}

// PublishMethodology validates one version, assigns the next version number and
// makes it active in the same transaction. The previous active version remains
// stored and can be activated again for a rollback.
func (s *Store) PublishMethodology(ctx context.Context, m domain.Methodology, files []domain.MethodologyFile) (domain.Methodology, error) {
	if m.HiveID == "" {
		return domain.Methodology{}, errors.New("store: methodology requires a hive id")
	}
	if err := methodology.Validate(m.Manifest, files); err != nil {
		return domain.Methodology{}, err
	}
	digest, err := methodology.Digest(m.Manifest, files)
	if err != nil {
		return domain.Methodology{}, err
	}
	m.Digest = digest
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.CreatedBy == "" {
		m.CreatedBy = domain.AddressSystem
	}
	manifest, err := json.Marshal(m.Manifest)
	if err != nil {
		return domain.Methodology{}, err
	}
	if files == nil {
		files = []domain.MethodologyFile{}
	}
	fileBody, err := json.Marshal(files)
	if err != nil {
		return domain.Methodology{}, err
	}
	var source []byte
	if m.Source != nil {
		if source, err = json.Marshal(m.Source); err != nil {
			return domain.Methodology{}, err
		}
	}
	id, err := newOpaqueID()
	if err != nil {
		return domain.Methodology{}, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Methodology{}, err
	}
	defer tx.Rollback(ctx)

	// Locking the Hive row serializes version allocation and the active flag,
	// so concurrent publishers cannot compute the same next version.
	var hiveID string
	if err = tx.QueryRow(ctx, `SELECT id FROM hives WHERE id=$1 FOR UPDATE`, m.HiveID).Scan(&hiveID); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	var version int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(version),0)+1 FROM methodologies WHERE hive_id=$1`, m.HiveID).Scan(&version); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE methodologies SET active=false WHERE hive_id=$1 AND active`, m.HiveID); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO methodologies(`+methodologyColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		id, m.HiveID, version, true, source, manifest, fileBody, m.Digest, m.Note, string(m.CreatedBy), m.CreatedAt); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	if err = insertMethodologyEvent(ctx, tx, m.HiveID, methodology.ActionPublish, version, m.CreatedBy, m.Note); err != nil {
		return domain.Methodology{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Methodology{}, err
	}

	m.ID = id
	m.Version = version
	m.Active = true
	return m, nil
}

// ActiveMethodology returns the Hive's active version and its files. A Hive
// with no published methodology yields domain.ErrNotFound so callers can fall
// back to methodology.Builtin.
func (s *Store) ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error) {
	return scanMethodology(s.Pool.QueryRow(ctx, `SELECT `+methodologyColumns+` FROM methodologies WHERE hive_id=$1 AND active`, hiveID))
}

// MethodologyVersion returns one version. version <= 0 selects the active one.
func (s *Store) MethodologyVersion(ctx context.Context, hiveID string, version int) (domain.Methodology, []domain.MethodologyFile, error) {
	if version <= 0 {
		return s.ActiveMethodology(ctx, hiveID)
	}
	return scanMethodology(s.Pool.QueryRow(ctx, `SELECT `+methodologyColumns+` FROM methodologies WHERE hive_id=$1 AND version=$2`, hiveID, version))
}

// ActivateMethodology switches the Hive's active version in one transaction and
// appends a change record. Activating the already active version is a no-op
// switch that still records the request.
func (s *Store) ActivateMethodology(ctx context.Context, hiveID string, version int, actor domain.Address, note string) (domain.Methodology, error) {
	if hiveID == "" || version <= 0 {
		return domain.Methodology{}, errors.New("store: activating a methodology requires a hive id and a positive version")
	}
	if actor == "" {
		actor = domain.AddressSystem
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Methodology{}, err
	}
	defer tx.Rollback(ctx)

	var lockedHive string
	if err = tx.QueryRow(ctx, `SELECT id FROM hives WHERE id=$1 FOR UPDATE`, hiveID).Scan(&lockedHive); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM methodologies WHERE hive_id=$1 AND version=$2`, hiveID, version).Scan(&id); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE methodologies SET active=false WHERE hive_id=$1 AND active`, hiveID); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE methodologies SET active=true WHERE id=$1`, id); err != nil {
		return domain.Methodology{}, domainErr(err)
	}
	if err = insertMethodologyEvent(ctx, tx, hiveID, methodology.ActionActivate, version, actor, note); err != nil {
		return domain.Methodology{}, err
	}
	active, _, err := scanMethodology(tx.QueryRow(ctx, `SELECT `+methodologyColumns+` FROM methodologies WHERE id=$1`, id))
	if err != nil {
		return domain.Methodology{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Methodology{}, err
	}
	return active, nil
}

// MethodologyVersions returns every version of a Hive from newest to oldest.
// Files are omitted; use MethodologyVersion to read a version's file contents.
func (s *Store) MethodologyVersions(ctx context.Context, hiveID string) ([]domain.Methodology, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,hive_id,version,active,source,manifest,NULL::jsonb,digest,note,created_by,created_at
		FROM methodologies WHERE hive_id=$1 ORDER BY version DESC`, hiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Methodology{}
	for rows.Next() {
		m, _, err := scanMethodology(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MethodologyChanges returns the append-only publish/activate history.
func (s *Store) MethodologyChanges(ctx context.Context, hiveID string) ([]methodology.Change, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,hive_id,action,version,note,actor,created_at
		FROM methodology_events WHERE hive_id=$1 ORDER BY id`, hiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []methodology.Change{}
	for rows.Next() {
		var change methodology.Change
		var actor string
		if err := rows.Scan(&change.ID, &change.HiveID, &change.Action, &change.Version,
			&change.Note, &actor, &change.CreatedAt); err != nil {
			return nil, domainErr(err)
		}
		change.Actor = domain.Address(actor)
		out = append(out, change)
	}
	return out, rows.Err()
}
