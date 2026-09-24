package store

import (
	"context"
	"fmt"
)

func platformImageUsage(ctx context.Context, q imageUsageQuerier, reference string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT '节点 ' || id FROM nodes WHERE readiness->>'platformImage'=$1 ORDER BY 1`, reference)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := []string{}
	for rows.Next() {
		var label string
		if err = rows.Scan(&label); err != nil {
			return nil, err
		}
		usage = append(usage, label)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var building bool
	err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform_settings p, jsonb_array_elements(COALESCE(p.body->'builds','[]'::jsonb)) b WHERE p.name='image_build_state' AND b->>'status' IN ('building','cancelling'))`).Scan(&building)
	if err != nil {
		return nil, err
	}
	if building {
		usage = append(usage, "正在进行的镜像构建")
	}
	return usage, nil
}

func (s *Store) PlatformImageUsage(ctx context.Context, reference string) ([]string, error) {
	return platformImageUsage(ctx, s.Pool, reference)
}

func (s *Store) WithPlatformImageDeletionGuard(ctx context.Context, reference string, deleteImage func() error) ([]string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, imageGuardLock); err != nil {
		return nil, err
	}
	usage, err := platformImageUsage(ctx, tx, reference)
	if err != nil {
		return nil, err
	}
	if len(usage) != 0 {
		return usage, fmt.Errorf("%w: 平台镜像仍被使用", ErrConflict)
	}
	if err = deleteImage(); err != nil {
		return nil, err
	}
	return nil, tx.Commit(ctx)
}
