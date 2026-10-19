package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

//go:embed schema_upgrade_10.sql
var schemaUpgrade10 string

// applySchemaUpgrade10 installs the attestation table. The round-4 tasks share
// one migration chain; this helper keeps the change to Migrate a single call.
func applySchemaUpgrade10(ctx context.Context, tx pgx.Tx, version int) error {
	if version >= 10 {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade10); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(10) ON CONFLICT DO NOTHING`)
	return err
}

// attestationColumns derives Superseded from the nullable timestamp so the
// domain type stays a plain value.
const attestationColumns = `id,hive_id,pr_number,head_sha,attester_id,author_id,is_author,result,summary,findings,comment_url,(superseded_at IS NOT NULL),created_at`

func scanAttestation(row pgx.Row) (domain.Attestation, error) {
	var a domain.Attestation
	var result string
	var findings []byte
	err := row.Scan(&a.ID, &a.HiveID, &a.PRNumber, &a.HeadSHA, &a.AttesterID, &a.AuthorID,
		&a.IsAuthor, &result, &a.Summary, &findings, &a.CommentURL, &a.Superseded, &a.CreatedAt)
	if err != nil {
		return domain.Attestation{}, domainErr(err)
	}
	a.Result = domain.AttestResult(result)
	if len(findings) > 0 {
		if err = json.Unmarshal(findings, &a.Findings); err != nil {
			return domain.Attestation{}, err
		}
	}
	return a, nil
}

func (s *Store) attestationByUnique(ctx context.Context, hiveID string, pr int64, headSHA, attesterID string) (domain.Attestation, error) {
	return scanAttestation(s.Pool.QueryRow(ctx, `SELECT `+attestationColumns+` FROM attestations
		WHERE hive_id=$1 AND pr_number=$2 AND head_sha=$3 AND attester_id=$4`, hiveID, pr, headSHA, attesterID))
}

// RecordAttestation writes one conclusion. The (hive, PR, head, attester)
// unique key makes repeated writes idempotent: a duplicate returns the stored
// record unchanged with created=false, so a retry never overwrites history.
func (s *Store) RecordAttestation(ctx context.Context, a domain.Attestation) (domain.Attestation, bool, error) {
	if a.ID == "" {
		id, err := newOpaqueID()
		if err != nil {
			return domain.Attestation{}, false, err
		}
		a.ID = id
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Findings == nil {
		a.Findings = []domain.Finding{}
	}
	findings, err := json.Marshal(a.Findings)
	if err != nil {
		return domain.Attestation{}, false, err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO attestations(id,hive_id,pr_number,head_sha,attester_id,author_id,is_author,result,summary,findings,comment_url,superseded_at,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,CASE WHEN $12 THEN now() ELSE NULL END,$13)
		ON CONFLICT (hive_id,pr_number,head_sha,attester_id) DO NOTHING`,
		a.ID, a.HiveID, a.PRNumber, a.HeadSHA, a.AttesterID, a.AuthorID, a.IsAuthor, string(a.Result),
		a.Summary, findings, a.CommentURL, a.Superseded, a.CreatedAt)
	if err != nil {
		return domain.Attestation{}, false, domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		stored, err := s.attestationByUnique(ctx, a.HiveID, a.PRNumber, a.HeadSHA, a.AttesterID)
		return stored, false, err
	}
	return a, true, nil
}

// SetAttestationComment records the PR comment URL the platform published.
func (s *Store) SetAttestationComment(ctx context.Context, id, url string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE attestations SET comment_url=$2 WHERE id=$1`, id, url)
	if err != nil {
		return domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// LatestAttestation returns the newest non-superseded conclusion for a PR.
// A conclusion for an older head is only returned if it was not superseded,
// which lets the merge gate report the stale SHA as a concrete reason.
func (s *Store) LatestAttestation(ctx context.Context, hiveID string, pr int64) (domain.Attestation, error) {
	return scanAttestation(s.Pool.QueryRow(ctx, `SELECT `+attestationColumns+` FROM attestations
		WHERE hive_id=$1 AND pr_number=$2 AND superseded_at IS NULL
		ORDER BY created_at DESC,id DESC LIMIT 1`, hiveID, pr))
}

// Attestations lists every conclusion for a PR, superseded ones included,
// newest first.
func (s *Store) Attestations(ctx context.Context, hiveID string, pr int64) ([]domain.Attestation, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+attestationColumns+` FROM attestations
		WHERE hive_id=$1 AND pr_number=$2 ORDER BY created_at DESC,id DESC`, hiveID, pr)
	if err != nil {
		return nil, domainErr(err)
	}
	defer rows.Close()
	var out []domain.Attestation
	for rows.Next() {
		a, err := scanAttestation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SupersedeAttestations invalidates every conclusion for a PR whose head is not
// keepSHA. Already-superseded rows keep their original timestamp.
func (s *Store) SupersedeAttestations(ctx context.Context, hiveID string, pr int64, keepSHA string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE attestations SET superseded_at=now()
		WHERE hive_id=$1 AND pr_number=$2 AND head_sha<>$3 AND superseded_at IS NULL`, hiveID, pr, keepSHA)
	return domainErr(err)
}

// PRAuthor returns the Agent that owns a PR branch, including a terminal one.
// The authoritative author does not disappear when the Forager finishes, so an
// attestation can still record whether its author was the attester.
func (s *Store) PRAuthor(ctx context.Context, hiveID string, pr int64) (domain.Agent, error) {
	return scanAgent(s.Pool.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE hive_id=$1 AND role='forager' AND pr_number=$2
		ORDER BY created_at DESC,id DESC LIMIT 1`, hiveID, pr))
}
