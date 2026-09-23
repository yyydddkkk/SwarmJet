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
var ErrNoNode = errors.New("no execution node with spare capacity")

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
	_, e := s.createTask(ctx, t, false)
	return e
}

// CreateTaskScheduled stores a task and pins it to an online execution node
// with spare capacity. Assignment happens once, under an advisory lock, and is
// never recomputed: later phases route to the node recorded here.
func (s *Store) CreateTaskScheduled(ctx context.Context, t domain.Task, requireNode bool) (domain.Task, error) {
	return s.createTask(ctx, t, requireNode)
}

func (s *Store) createTask(ctx context.Context, t domain.Task, requireNode bool) (domain.Task, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return t, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(84261302)"); e != nil {
		return t, e
	}
	node, found, e := pickTaskNode(ctx, tx, &t)
	if e != nil {
		return t, e
	}
	if found {
		t.NodeID = node
	} else {
		managed := false
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_configs)`).Scan(&managed); e != nil {
			return t, e
		}
		if requireNode || managed || t.Project.Nodes.Mode == "selected" {
			return t, fmt.Errorf("%w: %s", ErrNoNode, t.Message)
		}
	}
	b, _ := json.Marshal(t)
	source := fmt.Sprintf("%s/%d/%d", domain.GitLabIdentity(t.Project.GitLabURL), t.Issue.ProjectID, t.Issue.IID)
	var nodeID any
	if t.NodeID != "" {
		nodeID = t.NodeID
	}
	_, e = tx.Exec(ctx, "INSERT INTO tasks(id,project_id,source,stage,node_id,body) VALUES($1,$2,$3,$4,$5,$6)", t.ID, t.Project.ID, source, t.Stage, nodeID, b)
	if e != nil {
		return t, mapErr(e)
	}
	c := domain.Command{ID: "start-" + t.ID, Kind: "start"}
	cb, _ := json.Marshal(c)
	_, e = tx.Exec(ctx, "INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3)", c.ID, t.ID, cb)
	if e != nil {
		return t, e
	}
	return t, tx.Commit(ctx)
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

// HeartbeatNode registers or refreshes one execution node. A node that stops
// heartbeating simply stops receiving new tasks; its existing assignments stay.
func (s *Store) HeartbeatNode(ctx context.Context, n domain.Node) error {
	if n.Capacity < 1 {
		n.Capacity = 1
	}
	b, e := json.Marshal(n.Readiness)
	if e != nil {
		return e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `INSERT INTO nodes(id,address,capacity,version,last_seen,readiness) VALUES($1,$2,$3,$4,now(),$5)
 ON CONFLICT(id) DO UPDATE SET address=excluded.address,capacity=excluded.capacity,version=excluded.version,last_seen=now(),readiness=excluded.readiness
 WHERE nodes.address=excluded.address OR nodes.last_seen<now()-interval '30 seconds'`, n.ID, n.Address, n.Capacity, n.Version, b)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 此节点 ID 正在另一个地址发送心跳", ErrConflict)
	}
	if n.Version != "" {
		c := domain.NodeConfig{Version: 1, Name: n.ID, Capacity: n.Capacity, Network: domain.NetworkChoice{Mode: "default"}}
		raw, e := json.Marshal(c)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO node_configs(id,version,body) VALUES($1,1,$2) ON CONFLICT(id) DO NOTHING`, n.ID, raw); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) Node(ctx context.Context, id string) (domain.Node, error) {
	var n domain.Node
	var readiness []byte
	e := s.Pool.QueryRow(ctx, "SELECT id,address,capacity,version,last_seen,readiness FROM nodes WHERE id=$1", id).Scan(&n.ID, &n.Address, &n.Capacity, &n.Version, &n.LastSeen, &readiness)
	if e == nil {
		e = json.Unmarshal(readiness, &n.Readiness)
	}
	return n, mapErr(e)
}

// NodeAddress returns the base URL used to reach a registered node.
func (s *Store) NodeAddress(ctx context.Context, id string) (string, error) {
	n, e := s.Node(ctx, id)
	return n.Address, e
}

func (s *Store) Nodes(ctx context.Context) ([]domain.Node, error) {
	rows, e := s.Pool.Query(ctx, "SELECT id,address,capacity,version,last_seen,readiness FROM nodes ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Node{}
	for rows.Next() {
		var n domain.Node
		var readiness []byte
		if e = rows.Scan(&n.ID, &n.Address, &n.Capacity, &n.Version, &n.LastSeen, &readiness); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(readiness, &n.Readiness); e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) NodeStatuses(ctx context.Context) ([]domain.NodeStatus, error) {
	rows, e := s.Pool.Query(ctx, `SELECT n.id,n.address,n.capacity,n.version,n.last_seen,n.readiness,
		COALESCE(r.running,0), (n.last_seen > now() - $1::interval) AS online
		FROM nodes n
		LEFT JOIN (
			SELECT node_id,count(*) AS running FROM (
 SELECT node_id FROM tasks WHERE NOT terminal AND node_id IS NOT NULL
 UNION ALL SELECT body->>'nodeId' FROM initializations WHERE status NOT IN ('ready','cancelled') AND COALESCE(body->>'nodeId','')<>''
 ) work GROUP BY node_id
		) r ON r.node_id = n.id
		ORDER BY n.id`, fmt.Sprintf("%f seconds", domain.NodeOnlineAfter.Seconds()))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.NodeStatus{}
	for rows.Next() {
		var v domain.NodeStatus
		var readiness []byte
		if e = rows.Scan(&v.Node.ID, &v.Node.Address, &v.Node.Capacity, &v.Node.Version, &v.Node.LastSeen, &readiness, &v.Running, &v.Online); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(readiness, &v.Node.Readiness); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	rows.Close()
	for i := range out {
		c, err := s.NodeConfig(ctx, out[i].Node.ID)
		if err == nil {
			out[i].Config = &c
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return out, nil
}
