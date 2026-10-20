package store

import (
	"context"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// CompletionPending is independent of Inbox batch size: a pending lifecycle
// completion must prevent publication even when earlier work fills the batch.
func (s *Store) CompletionPending(ctx context.Context, hiveID, agentID string) (bool, error) {
	var pending bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE hive_id=$1 AND to_addr=$2
  AND state IN ('pending','delivered') AND kind='control' AND lower(split_part(btrim(body),E'\n',1))='complete')`, hiveID, string(domain.ForagerAddress(agentID))).Scan(&pending)
	return pending, err
}
