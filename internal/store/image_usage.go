package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const imageGuardLock = `SELECT pg_advisory_xact_lock(84261305)`

type imageUsageQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ImageUsage checks durable platform references, including paused work and
// disabled projects. Completed work remains in history but does not block it.
func imageUsage(ctx context.Context, q imageUsageQuerier, reference string) ([]string, error) {
	rows, err := q.Query(ctx, `
SELECT '节点 ' || id FROM node_configs WHERE body->>'image'=$1 OR body->'images' ? $1
UNION ALL SELECT '项目 ' || id FROM projects WHERE body#>>'{development,image}'=$1 OR body#>>'{review,image}'=$1
UNION ALL SELECT '任务 ' || id FROM tasks WHERE NOT terminal AND (body#>>'{project,development,image}'=$1 OR body#>>'{project,review,image}'=$1)
UNION ALL SELECT '初始化 ' || id FROM initializations WHERE status NOT IN ('ready','cancelled') AND (body->>'image'=$1 OR body#>>'{executionConfig,image}'=$1)
ORDER BY 1`, reference)
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
	rows.Close()
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

func (s *Store) ImageUsage(ctx context.Context, reference string) ([]string, error) {
	return imageUsage(ctx, s.Pool, reference)
}

// WithImageDeletionGuard serializes the final reference check and registry
// deletion with writes that could create a new platform reference.
func (s *Store) WithImageDeletionGuard(ctx context.Context, reference string, deleteImage func() error) ([]string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, imageGuardLock); err != nil {
		return nil, err
	}
	var repository string
	err = tx.QueryRow(ctx, `SELECT COALESCE(body#>>'{config,address}','') || '/' || COALESCE(body#>>'{config,repository}','') FROM platform_settings WHERE name='registry'`).Scan(&repository)
	if err != nil {
		return nil, mapErr(err)
	}
	expectedRepository, _, _ := strings.Cut(reference, "@")
	if repository != expectedRepository {
		return nil, fmt.Errorf("%w: 仓库配置已变化，请刷新后重试", ErrConflict)
	}
	usage, err := imageUsage(ctx, tx, reference)
	if err != nil {
		return nil, err
	}
	if len(usage) != 0 {
		return usage, fmt.Errorf("%w: 镜像仍被平台使用", ErrConflict)
	}
	if err = deleteImage(); err != nil {
		return nil, err
	}
	return nil, tx.Commit(ctx)
}
