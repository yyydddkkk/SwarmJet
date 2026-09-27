package store

import (
	"context"
	"strconv"
	"time"
)

// WithRepositoryScanLock makes periodic reconciliation and queue deliveries
// serialize scans for the same provider and repository across API instances.
func (s *Store) WithRepositoryScanLock(ctx context.Context, provider string, repositoryID int64, scan func() error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	key := "repository-scan:" + provider + ":" + strconv.FormatInt(repositoryID, 10)
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", key); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", key); unlockErr != nil {
			conn.Conn().Close(unlockCtx)
		}
	}()
	return scan()
}
