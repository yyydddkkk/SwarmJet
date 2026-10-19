package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// verdictColumns selects the stored verdict with its superseded flag derived
// from the nullable timestamp, so domain.Verdict stays a plain value type.
const verdictColumns = `id,hive_id,pr_number,head_sha,owner_id,verifier_id,result,summary,findings,comment_url,(superseded_at IS NOT NULL),created_at`

func scanVerdict(row pgx.Row) (domain.Verdict, error) {
	var v domain.Verdict
	var result string
	var findings []byte
	err := row.Scan(&v.ID, &v.HiveID, &v.PRNumber, &v.HeadSHA, &v.OwnerID, &v.VerifierID,
		&result, &v.Summary, &findings, &v.CommentURL, &v.Superseded, &v.CreatedAt)
	if err != nil {
		return domain.Verdict{}, domainErr(err)
	}
	v.Result = domain.VerdictResult(result)
	if len(findings) > 0 {
		if err = json.Unmarshal(findings, &v.Findings); err != nil {
			return domain.Verdict{}, err
		}
	}
	return v, nil
}

// RecordVerdict writes one verdict. The (hive, PR, head, verifier) unique key
// makes repeated writes idempotent: a duplicate returns the stored record with
// the same values as the first write.
func (s *Store) RecordVerdict(ctx context.Context, v domain.Verdict) (domain.Verdict, error) {
	if v.ID == "" {
		id, err := newOpaqueID()
		if err != nil {
			return domain.Verdict{}, err
		}
		v.ID = id
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	findings, err := json.Marshal(v.Findings)
	if err != nil {
		return domain.Verdict{}, err
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO verdicts(id,hive_id,pr_number,head_sha,owner_id,verifier_id,result,summary,findings,comment_url,superseded_at,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,CASE WHEN $11 THEN now() ELSE NULL END,$12)
		ON CONFLICT (hive_id,pr_number,head_sha,verifier_id) DO NOTHING`,
		v.ID, v.HiveID, v.PRNumber, v.HeadSHA, v.OwnerID, v.VerifierID, string(v.Result), v.Summary,
		findings, v.CommentURL, v.Superseded, v.CreatedAt)
	if err != nil {
		return domain.Verdict{}, domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		return s.verdictByUnique(ctx, v.HiveID, v.PRNumber, v.HeadSHA, v.VerifierID)
	}
	return v, nil
}

func (s *Store) verdictByUnique(ctx context.Context, hiveID string, pr int64, headSHA, verifierID string) (domain.Verdict, error) {
	return scanVerdict(s.Pool.QueryRow(ctx, `SELECT `+verdictColumns+` FROM verdicts
		WHERE hive_id=$1 AND pr_number=$2 AND head_sha=$3 AND verifier_id=$4`, hiveID, pr, headSHA, verifierID))
}

// SetVerdictComment records the PR comment URL the platform published.
func (s *Store) SetVerdictComment(ctx context.Context, id, url string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE verdicts SET comment_url=$2 WHERE id=$1`, id, url)
	if err != nil {
		return domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// LatestVerdict returns the newest non-superseded verdict for a PR.
func (s *Store) LatestVerdict(ctx context.Context, hiveID string, pr int64) (domain.Verdict, error) {
	return scanVerdict(s.Pool.QueryRow(ctx, `SELECT `+verdictColumns+` FROM verdicts
		WHERE hive_id=$1 AND pr_number=$2 AND superseded_at IS NULL
		ORDER BY created_at DESC,id DESC LIMIT 1`, hiveID, pr))
}

// VerdictByVerifier returns the newest verdict a verifier wrote, including a
// superseded one: the supervisor only needs to know that it submitted at all.
func (s *Store) VerdictByVerifier(ctx context.Context, verifierID string) (domain.Verdict, error) {
	return scanVerdict(s.Pool.QueryRow(ctx, `SELECT `+verdictColumns+` FROM verdicts
		WHERE verifier_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, verifierID))
}

// Verdicts lists every verdict for a PR, superseded ones included, newest first.
func (s *Store) Verdicts(ctx context.Context, hiveID string, pr int64) ([]domain.Verdict, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+verdictColumns+` FROM verdicts
		WHERE hive_id=$1 AND pr_number=$2 ORDER BY created_at DESC,id DESC`, hiveID, pr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var verdicts []domain.Verdict
	for rows.Next() {
		v, err := scanVerdict(rows)
		if err != nil {
			return nil, err
		}
		verdicts = append(verdicts, v)
	}
	return verdicts, rows.Err()
}

// SupersedeVerdicts invalidates every verdict for a PR whose head is not
// keepSHA. Already-superseded rows keep their original timestamp.
func (s *Store) SupersedeVerdicts(ctx context.Context, hiveID string, pr int64, keepSHA string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE verdicts SET superseded_at=now()
		WHERE hive_id=$1 AND pr_number=$2 AND head_sha<>$3 AND superseded_at IS NULL`, hiveID, pr, keepSHA)
	return err
}

// CountVerdicts counts a worker's verdicts of one result, superseded included.
func (s *Store) CountVerdicts(ctx context.Context, hiveID, ownerID string, result domain.VerdictResult) (int, error) {
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM verdicts WHERE hive_id=$1 AND owner_id=$2 AND result=$3`,
		hiveID, ownerID, string(result)).Scan(&count)
	if err != nil {
		return 0, domainErr(err)
	}
	return count, nil
}
