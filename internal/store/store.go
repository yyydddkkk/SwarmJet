package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/domain"
)

//go:embed schema.sql
var schema string
var ErrConflict = errors.New("conflict: stale version or active task already exists")
var ErrNotFound = errors.New("not found")

type Store struct{ Pool *pgxpool.Pool }

func (s *Store) HasSource(ctx context.Context, base string, issue domain.Issue) (bool, error) {
	var exists bool
	source := fmt.Sprintf("%s/%d/%d", domain.GitLabIdentity(base), issue.ProjectID, issue.IID)
	e := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tasks WHERE source=$1)", source).Scan(&exists)
	return exists, e
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
	return &Store{p}, nil
}
func (s *Store) Close()                            { s.Pool.Close() }
func (s *Store) Migrate(ctx context.Context) error { _, e := s.Pool.Exec(ctx, schema); return e }
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
func (s *Store) Project(ctx context.Context, id string) (domain.Project, error) {
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT body FROM projects WHERE id=$1", id).Scan(&b)
	var p domain.Project
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	return p, mapErr(e)
}
func (s *Store) Projects(ctx context.Context) ([]domain.Project, error) {
	rows, e := s.Pool.Query(ctx, "SELECT body FROM projects ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Project{}
	for rows.Next() {
		var b []byte
		var p domain.Project
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &p); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) SaveProject(ctx context.Context, p domain.Project) (domain.Project, error) {
	if e := domain.ValidateProjectForSave(p); e != nil {
		return p, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	// Repository routing is unambiguous: one enabled Meta-Repo owns an Issue source.
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(84261301)"); e != nil {
		return p, e
	}
	if p.Enabled {
		rows, err := tx.Query(ctx, "SELECT body FROM projects WHERE id<>$1", p.ID)
		if err != nil {
			return p, err
		}
		for rows.Next() {
			var b []byte
			var other domain.Project
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return p, err
			}
			if err = json.Unmarshal(b, &other); err != nil {
				rows.Close()
				return p, err
			}
			if !other.Enabled || domain.GitLabIdentity(other.GitLabURL) != domain.GitLabIdentity(p.GitLabURL) {
				continue
			}
			for _, r := range p.Repositories {
				for _, or := range other.Repositories {
					if r.ProjectID == or.ProjectID {
						rows.Close()
						return p, fmt.Errorf("%w: repository %d already belongs to enabled project %s", ErrConflict, r.ProjectID, other.ID)
					}
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return p, err
		}
	}
	var version int
	e = tx.QueryRow(ctx, "SELECT version FROM projects WHERE id=$1 FOR UPDATE", p.ID).Scan(&version)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return p, e
	}
	if p.Version != version {
		return p, ErrConflict
	}
	p.Version = version + 1
	b, _ := json.Marshal(p)
	if version == 0 {
		_, e = tx.Exec(ctx, "INSERT INTO projects(id,version,body) VALUES($1,$2,$3)", p.ID, p.Version, b)
	} else {
		_, e = tx.Exec(ctx, "UPDATE projects SET version=$2,body=$3 WHERE id=$1", p.ID, p.Version, b)
	}
	if e != nil {
		return p, mapErr(e)
	}
	return p, tx.Commit(ctx)
}
func (s *Store) CreateTask(ctx context.Context, t domain.Task) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	b, _ := json.Marshal(t)
	source := fmt.Sprintf("%s/%d/%d", domain.GitLabIdentity(t.Project.GitLabURL), t.Issue.ProjectID, t.Issue.IID)
	_, e = tx.Exec(ctx, "INSERT INTO tasks(id,project_id,source,stage,body) VALUES($1,$2,$3,$4,$5)", t.ID, t.Project.ID, source, t.Stage, b)
	if e != nil {
		return mapErr(e)
	}
	c := domain.Command{ID: "start-" + t.ID, Kind: "start"}
	cb, _ := json.Marshal(c)
	_, e = tx.Exec(ctx, "INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3)", c.ID, t.ID, cb)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Task(ctx context.Context, id string) (domain.Task, error) {
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT body FROM tasks WHERE id=$1", id).Scan(&b)
	var t domain.Task
	if e == nil {
		e = json.Unmarshal(b, &t)
	}
	return t, mapErr(e)
}
func (s *Store) Tasks(ctx context.Context) ([]domain.Task, error) {
	rows, e := s.Pool.Query(ctx, "SELECT body FROM tasks ORDER BY updated_at DESC LIMIT 200")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Task{}
	for rows.Next() {
		var b []byte
		var t domain.Task
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &t); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) SaveTask(ctx context.Context, t domain.Task) error {
	t.UpdatedAt = time.Now().UTC()
	b, _ := json.Marshal(t)
	tag, e := s.Pool.Exec(ctx, "UPDATE tasks SET stage=$2,terminal=$3,body=$4,updated_at=now() WHERE id=$1", t.ID, t.Stage, domain.Terminal(t.Stage), b)
	if e == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return e
}

func (s *Store) Execution(ctx context.Context, taskID, role string, ex domain.Execution) error {
	b, e := json.Marshal(ex)
	if e != nil {
		return e
	}
	_, e = s.Pool.Exec(ctx, `UPDATE tasks SET body=jsonb_set(body,'{executions}',coalesce(nullif(body->'executions','null'::jsonb),'{}'::jsonb)||jsonb_build_object($2::text,$3::jsonb)),updated_at=now() WHERE id=$1`, taskID, role, b)
	return e
}
func (s *Store) Enqueue(ctx context.Context, taskID string, c domain.Command) error {
	b, _ := json.Marshal(c)
	tag, e := s.Pool.Exec(ctx, "INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", c.ID, taskID, b)
	if e != nil {
		return mapErr(e)
	}
	if tag.RowsAffected() == 0 {
		var old []byte
		var tid string
		e = s.Pool.QueryRow(ctx, "SELECT task_id,body FROM commands WHERE id=$1", c.ID).Scan(&tid, &old)
		var oc domain.Command
		_ = json.Unmarshal(old, &oc)
		if e != nil {
			return e
		}
		ob, _ := json.Marshal(oc)
		if tid != taskID || string(ob) != string(b) {
			return ErrConflict
		}
	}
	return nil
}

type PendingCommand struct {
	TaskID  string
	Command domain.Command
}

func (s *Store) Pending(ctx context.Context) ([]PendingCommand, error) {
	rows, e := s.Pool.Query(ctx, "SELECT task_id,body FROM commands WHERE NOT delivered ORDER BY created_at LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PendingCommand{}
	for rows.Next() {
		var p PendingCommand
		var b []byte
		if e = rows.Scan(&p.TaskID, &b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &p.Command); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Delivered(ctx context.Context, id string) error {
	_, e := s.Pool.Exec(ctx, "UPDATE commands SET delivered=true WHERE id=$1", id)
	return e
}
func (s *Store) Event(ctx context.Context, taskID, kind, message string) error {
	_, e := s.Pool.Exec(ctx, "INSERT INTO events(task_id,kind,message) VALUES($1,$2,$3)", taskID, kind, message)
	return e
}
func (s *Store) Events(ctx context.Context, taskID string, after int64) ([]domain.Event, error) {
	rows, e := s.Pool.Query(ctx, "SELECT id,task_id,kind,message,created_at FROM events WHERE task_id=$1 AND id>$2 ORDER BY id LIMIT 200", taskID, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Event{}
	for rows.Next() {
		var v domain.Event
		if e = rows.Scan(&v.ID, &v.TaskID, &v.Kind, &v.Message, &v.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AddSession(ctx context.Context, hash string) error {
	_, e := s.Pool.Exec(ctx, "INSERT INTO sessions(token_hash,expires_at) VALUES($1,now()+interval '12 hours')", hash)
	return e
}
func (s *Store) Session(ctx context.Context, hash string) bool {
	var ok bool
	e := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND expires_at>now())", hash).Scan(&ok)
	return e == nil && ok
}
func (s *Store) DeleteSession(ctx context.Context, hash string) {
	_, _ = s.Pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", hash)
}
func (s *Store) Lease(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	var got string
	e := s.Pool.QueryRow(ctx, "INSERT INTO leases(name,owner,expires_at) VALUES($1,$2,now()+$3::interval) ON CONFLICT(name) DO UPDATE SET owner=excluded.owner,expires_at=excluded.expires_at WHERE leases.owner=excluded.owner OR leases.expires_at<now() RETURNING owner", name, owner, fmt.Sprintf("%f seconds", ttl.Seconds())).Scan(&got)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	return got == owner, e
}
func (s *Store) ReleaseLeases(ctx context.Context, owner string) error {
	_, e := s.Pool.Exec(ctx, "DELETE FROM leases WHERE owner=$1", owner)
	return e
}
