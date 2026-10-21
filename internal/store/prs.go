package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

// PRSnapshot is the last observed GitHub state. Closed PRs remain available
// after the GitHub request cache expires.
type PRSnapshot struct {
	HiveID                     string
	Number                     int64
	Title, State, URL, HeadSHA string
	Merged, Ready              bool
	ClosedAt, UpdatedAt        time.Time
	ObservedAt                 time.Time
	// ReadStartedAt guards a read that began before a newer webhook write.
	ReadStartedAt time.Time
	Reasons       []string
}

func (s *Store) PRSnapshots(ctx context.Context, hiveID string) (map[int64]PRSnapshot, error) {
	rows, err := s.Pool.Query(ctx, `SELECT number,title,state,merged,closed_at,url,head_sha,updated_at,ready,reasons,observed_at FROM pr_snapshots WHERE hive_id=$1`, hiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]PRSnapshot{}
	for rows.Next() {
		var p PRSnapshot
		var closedAt, updatedAt *time.Time
		var reasons []byte
		p.HiveID = hiveID
		if err := rows.Scan(&p.Number, &p.Title, &p.State, &p.Merged, &closedAt, &p.URL, &p.HeadSHA, &updatedAt, &p.Ready, &reasons, &p.ObservedAt); err != nil {
			return nil, err
		}
		if closedAt != nil {
			p.ClosedAt = *closedAt
		}
		if updatedAt != nil {
			p.UpdatedAt = *updatedAt
		}
		if err := json.Unmarshal(reasons, &p.Reasons); err != nil {
			return nil, err
		}
		out[p.Number] = p
	}
	return out, rows.Err()
}

func (s *Store) SavePRSnapshot(ctx context.Context, p PRSnapshot) (bool, error) {
	reasons := p.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	encoded, err := json.Marshal(reasons)
	if err != nil {
		return false, err
	}
	var closedAt, updatedAt, readStartedAt any
	if !p.ClosedAt.IsZero() {
		closedAt = p.ClosedAt.UTC()
	}
	if !p.UpdatedAt.IsZero() {
		updatedAt = p.UpdatedAt.UTC()
	}
	if !p.ReadStartedAt.IsZero() {
		readStartedAt = p.ReadStartedAt.UTC()
	}
	var number int64
	err = s.Pool.QueryRow(ctx, `INSERT INTO pr_snapshots(hive_id,number,title,state,merged,closed_at,url,head_sha,updated_at,ready,reasons)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	 ON CONFLICT(hive_id,number) DO UPDATE SET title=EXCLUDED.title,state=EXCLUDED.state,merged=EXCLUDED.merged,
	 closed_at=EXCLUDED.closed_at,url=EXCLUDED.url,head_sha=EXCLUDED.head_sha,updated_at=EXCLUDED.updated_at,
	 ready=EXCLUDED.ready,reasons=EXCLUDED.reasons,observed_at=now()
	 WHERE (pr_snapshots.title,pr_snapshots.state,pr_snapshots.merged,pr_snapshots.closed_at,pr_snapshots.url,
	 pr_snapshots.head_sha,pr_snapshots.updated_at,pr_snapshots.ready,pr_snapshots.reasons) IS DISTINCT FROM
	 (EXCLUDED.title,EXCLUDED.state,EXCLUDED.merged,EXCLUDED.closed_at,EXCLUDED.url,EXCLUDED.head_sha,
	 EXCLUDED.updated_at,EXCLUDED.ready,EXCLUDED.reasons)
	 AND (pr_snapshots.updated_at IS NULL OR
	      EXCLUDED.updated_at > pr_snapshots.updated_at OR
	      (EXCLUDED.updated_at = pr_snapshots.updated_at AND EXCLUDED.state = pr_snapshots.state))
	 AND ($12::timestamptz IS NULL OR pr_snapshots.observed_at <= $12)
	 RETURNING number`, p.HiveID, p.Number, p.Title, p.State, p.Merged, closedAt, p.URL, p.HeadSHA, updatedAt, p.Ready, encoded, readStartedAt).Scan(&number)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// SaveWebhookPR updates the same persisted read view from a routed GitHub PR
// delivery. Reopened PRs replace their former terminal snapshot.
func (s *Store) SaveWebhookPR(ctx context.Context, hiveID string, number int64, title, state string, merged bool, closedAt time.Time, url, headSHA string, updatedAt time.Time) error {
	if state == "" {
		state = "open"
	}
	if state == "open" {
		closedAt = time.Time{}
		merged = false
	}
	if state != "open" && closedAt.IsZero() {
		closedAt = updatedAt
	}
	var closed, updated any
	if !closedAt.IsZero() {
		closed = closedAt.UTC()
	}
	if !updatedAt.IsZero() {
		updated = updatedAt.UTC()
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO pr_snapshots(hive_id,number,title,state,merged,closed_at,url,head_sha,updated_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
	 ON CONFLICT(hive_id,number) DO UPDATE SET title=EXCLUDED.title,state=EXCLUDED.state,merged=EXCLUDED.merged,
	 closed_at=EXCLUDED.closed_at,url=EXCLUDED.url,head_sha=EXCLUDED.head_sha,updated_at=EXCLUDED.updated_at,
	 ready=CASE WHEN pr_snapshots.head_sha=EXCLUDED.head_sha AND EXCLUDED.state='open' THEN pr_snapshots.ready ELSE false END,
	 reasons=CASE WHEN pr_snapshots.head_sha=EXCLUDED.head_sha AND EXCLUDED.state='open' THEN pr_snapshots.reasons ELSE '[]'::jsonb END,
	 observed_at=now()
	 WHERE pr_snapshots.updated_at IS NULL OR
	       (EXCLUDED.updated_at IS NOT NULL AND EXCLUDED.updated_at >= pr_snapshots.updated_at)`,
		hiveID, number, title, state, merged, closed, url, headSHA, updated)
	return err
}

// ProjectPR is a PR associated with an Agent or attestation in this Hive.
type ProjectPR struct {
	Number  int64
	OwnerID string
	HeadSHA string
}

// ProjectPRs includes historical owners; verifier revisions never outrank an owner.
func (s *Store) ProjectPRs(ctx context.Context, hiveID string) ([]ProjectPR, error) {
	rows, err := s.Pool.Query(ctx, `WITH numbers AS (
 SELECT pr_number FROM agents WHERE hive_id=$1 AND pr_number>0
 UNION SELECT pr_number FROM attestations WHERE hive_id=$1
 ) SELECT n.pr_number,
 COALESCE((SELECT id FROM agents WHERE hive_id=$1 AND pr_number=n.pr_number
 ORDER BY (revision<>''),created_at,id LIMIT 1),''),
 COALESCE((SELECT head_sha FROM attestations WHERE hive_id=$1 AND pr_number=n.pr_number
 ORDER BY created_at DESC,id DESC LIMIT 1),'')
 FROM numbers n ORDER BY n.pr_number DESC`, hiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectPR{}
	for rows.Next() {
		var pr ProjectPR
		if err := rows.Scan(&pr.Number, &pr.OwnerID, &pr.HeadSHA); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// RecordWebPRMerge records the authenticated administrator's request before any
// external mutation, so audit storage failure cannot produce an unaudited merge.
func (s *Store) RecordWebPRMerge(ctx context.Context, hiveID string, pr int64, headSHA, administrator string) error {
	details, err := json.Marshal(map[string]any{"hiveId": hiveID, "prNumber": pr, "headSha": headSHA, "administrator": administrator, "source": "web"})
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_events(actor_type,action,target_type,target_id,details)
 VALUES('web_administrator','pr.merge.request','pull_request',$1,$2)`, fmt.Sprintf("%s/%d", hiveID, pr), details)
	return err
}
