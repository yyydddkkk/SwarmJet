package store

import (
	"context"
	"encoding/json"
	"fmt"
)

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
