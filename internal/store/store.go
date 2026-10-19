package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

//go:embed schema.sql
var schema string

//go:embed schema_upgrade_2.sql
var schemaUpgrade2 string

//go:embed schema_upgrade_3.sql
var schemaUpgrade3 string

//go:embed schema_upgrade_4.sql
var schemaUpgrade4 string

//go:embed schema_upgrade_5.sql
var schemaUpgrade5 string

//go:embed schema_upgrade_6.sql
var schemaUpgrade6 string

var ErrConflict = errors.New("conflict: stale version or active task already exists")
var ErrNotFound = errors.New("not found")

// ErrMessageQueueUnavailable is returned when a message is written but the
// Store has no delivery queue configured. The transaction rolls back, so the
// message can never be silently stranded in pending.
var ErrMessageQueueUnavailable = errors.New("store: message delivery queue unavailable")

type Store struct {
	Pool *pgxpool.Pool
	Jobs jobs.Inserter
	// MessageSink is the extension point for pushing a delivered Inbox message
	// into the target CLI session. A nil sink leaves delivery at the database
	// boundary.
	MessageSink func(context.Context, domain.Message) error

	// listener guards the single shared LISTEN connection used by WaitInbox.
	listenerMu sync.Mutex
	listener   *inboxListener
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	p, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return &Store{Pool: p}, nil
}

func (s *Store) Close() {
	s.listenerMu.Lock()
	listener := s.listener
	s.listenerMu.Unlock()
	if listener != nil {
		listener.stop()
	}
	s.Pool.Close()
}

// Migrate applies the legacy baseline plus every upgrade in order. Old tables
// stay in place as history; round 3 no longer reads or writes them.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84261305)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow(ctx, `SELECT max(version) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version > 7 {
		return fmt.Errorf("database schema version %d is newer than this application supports", version)
	}
	if version < 2 {
		if _, err = tx.Exec(ctx, schemaUpgrade2); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(2) ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	if version < 3 {
		if _, err = tx.Exec(ctx, schemaUpgrade3); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(3) ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	if version < 4 {
		if _, err = tx.Exec(ctx, schemaUpgrade4); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(4) ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	if version < 5 {
		if _, err = tx.Exec(ctx, schemaUpgrade5); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(5) ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	if version < 6 {
		if _, err = tx.Exec(ctx, schemaUpgrade6); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(6) ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	if err = applySchemaUpgrade7(ctx, tx, version); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return jobs.Migrate(ctx, s.Pool)
}

func mapErr(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return ErrConflict
	}
	return e
}

// DeleteSession removes one administrator session. Logging out an already
// expired session is a no-op.
func (s *Store) DeleteSession(ctx context.Context, hash string) {
	_, _ = s.Pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", hash)
}
