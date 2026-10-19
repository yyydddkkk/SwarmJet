package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

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

// imageGuardLock once guarded Agent image deletion. Project, task, admission and
// environment writes still take it, so dropping it is a separate concurrency change.
const imageGuardLock = `SELECT pg_advisory_xact_lock(84261305)`
var ErrNoNode = errors.New("no execution node with spare capacity")
var ErrAdmissionQueueFull = errors.New("task admission queue is full")

// Pending Tasks are durable, but have no start command until a node is assigned.
// Limits bound stored demand and the number of live workflows per project.
const (
	maxPendingTasks           = 1000
	maxPendingTasksPerProject = 100
	maxActiveTasksPerProject  = 4
	maxStartedTasksPerProject = 16
)

const activeNodeTask = `NOT terminal AND node_id IS NOT NULL AND NOT COALESCE((body->>'paused')::boolean,false) AND stage NOT IN ('waiting_dependencies','waiting_approval','waiting_manual_merge','partial_merge','blocked')`
const activeProjectTask = `NOT terminal AND NOT COALESCE((body->>'paused')::boolean,false) AND stage NOT IN ('waiting_dependencies','waiting_approval','waiting_manual_merge','partial_merge','blocked') AND (node_id IS NOT NULL OR EXISTS(SELECT 1 FROM commands c WHERE c.task_id=tasks.id AND c.id='start-'||tasks.id))`

type Store struct {
	Pool *pgxpool.Pool
	Jobs jobs.Inserter
	// MessageSink is the extension point for pushing a delivered Inbox message
	// into the target CLI session. This round only flips the message state and
	// wakes waiters; the Cell runtime installs a sink here next round. A nil
	// sink leaves delivery at the database boundary.
	MessageSink func(context.Context, domain.Message) error

	// listener guards the single shared LISTEN connection used by WaitInbox.
	listenerMu sync.Mutex
	listener   *inboxListener
}

func (s *Store) HasSource(ctx context.Context, base string, issue domain.Issue) (bool, error) {
	var exists bool
	e := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tasks WHERE source=$1)", issueSource(base, issue)).Scan(&exists)
	return exists, e
}

func issueSource(base string, issue domain.Issue) string {
	return domain.IssueSource(base, issue)
}

func (s *Store) FirstTaskCreatedAt(ctx context.Context, base string, issue domain.Issue) (time.Time, error) {
	var at time.Time
	e := s.Pool.QueryRow(ctx, `SELECT COALESCE((body->>'createdAt')::timestamptz,updated_at) FROM tasks WHERE source=$1 ORDER BY COALESCE((body->>'createdAt')::timestamptz,updated_at),id LIMIT 1`, issueSource(base, issue)).Scan(&at)
	return at, mapErr(e)
}

func (s *Store) IssueRoundEnabledAt(ctx context.Context) (time.Time, error) {
	var at time.Time
	e := s.Pool.QueryRow(ctx, `SELECT enabled_at FROM feature_epochs WHERE name='issue_round_comments'`).Scan(&at)
	return at, e
}

func (s *Store) RecordIssueRunCommand(ctx context.Context, base string, issue domain.Issue, noteID int64, body string, created time.Time) error {
	_, e := s.Pool.Exec(ctx, `INSERT INTO issue_run_commands(source,note_id,body,created_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, issueSource(base, issue), noteID, body, created)
	return e
}

func (s *Store) PendingIssueRunCommands(ctx context.Context, base string, issue domain.Issue) (bool, error) {
	var pending bool
	e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue_run_commands WHERE source=$1 AND claimed_task_id IS NULL)`, issueSource(base, issue)).Scan(&pending)
	return pending, e
}

func (s *Store) LatestTaskForIssue(ctx context.Context, base string, issue domain.Issue) (domain.Task, error) {
	var b []byte
	e := s.Pool.QueryRow(ctx, `SELECT body FROM tasks WHERE source=$1 ORDER BY COALESCE((body->>'createdAt')::timestamptz,updated_at) DESC,id DESC LIMIT 1`, issueSource(base, issue)).Scan(&b)
	var t domain.Task
	if e == nil {
		e = json.Unmarshal(b, &t)
	}
	return t, mapErr(e)
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
	if version > 6 {
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
	return s.saveProject(ctx, p, "")
}

// SaveProjectWithOwner creates the project and its first direct owner in one
// transaction, so a failed grant cannot leave a newly created private project
// without its selected owner.
func (s *Store) SaveProjectWithOwner(ctx context.Context, p domain.Project, ownerID string) (domain.Project, error) {
	if ownerID == "" {
		return p, ErrNotFound
	}
	return s.saveProject(ctx, p, ownerID)
}

func (s *Store) saveProject(ctx context.Context, p domain.Project, ownerID string) (domain.Project, error) {
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
	if _, e = tx.Exec(ctx, imageGuardLock); e != nil {
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
			if !other.Enabled || other.HostingProvider() != p.HostingProvider() || domain.GitLabIdentity(other.HostingURL()) != domain.GitLabIdentity(p.HostingURL()) {
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
	if version == 0 && p.AutoMerge == nil {
		enabled := true
		p.AutoMerge = &enabled
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
	if ownerID != "" {
		if version != 0 {
			return p, ErrConflict
		}
		var granted string
		e = tx.QueryRow(ctx, `INSERT INTO project_grants(project_id,user_id,role)
			SELECT $1,id,'owner' FROM users WHERE id=$2 AND status='active'
			RETURNING user_id`, p.ID, ownerID).Scan(&granted)
		if e != nil {
			return p, mapErr(e)
		}
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
	if _, e = tx.Exec(ctx, imageGuardLock); e != nil {
		return t, e
	}
	if t.Project.HostingProvider() == domain.ProviderGitHub {
		t.Issue.Provider = domain.ProviderGitHub
	}
	source := issueSource(t.Project.HostingURL(), t.Issue)
	var previous string
	var rounds int64
	if e = tx.QueryRow(ctx, `SELECT count(*),COALESCE((SELECT id FROM tasks WHERE source=$1 ORDER BY COALESCE((body->>'createdAt')::timestamptz,updated_at) DESC,id DESC LIMIT 1),'') FROM tasks WHERE source=$1`, source).Scan(&rounds, &previous); e != nil {
		return t, e
	}
	t.Round = int(rounds) + 1
	t.PreviousTaskID = previous
	if t.TriggerNoteIDs != nil {
		rows, err := tx.Query(ctx, `SELECT note_id,body FROM issue_run_commands WHERE source=$1 AND claimed_task_id IS NULL ORDER BY created_at,note_id FOR UPDATE`, source)
		if err != nil {
			return t, err
		}
		t.TriggerNoteIDs = nil
		parts := []string{}
		for rows.Next() {
			var id int64
			var body string
			if err = rows.Scan(&id, &body); err != nil {
				rows.Close()
				return t, err
			}
			t.TriggerNoteIDs = append(t.TriggerNoteIDs, id)
			parts = append(parts, body)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return t, err
		}
		if len(t.TriggerNoteIDs) == 0 {
			return t, ErrNotFound
		}
		t.TriggerText = strings.Join(parts, "\n\n")
	}
	managed := false
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_configs)`).Scan(&managed); e != nil {
		return t, e
	}
	distributed := requireNode || managed || t.Project.Nodes.Mode == "selected"
	admitted := false
	if distributed {
		t.AdmissionBackend = "node"
	} else {
		t.AdmissionBackend = "local"
	}
	var active, started int
	if e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE `+activeProjectTask+`),count(*) FILTER(WHERE NOT terminal AND EXISTS(SELECT 1 FROM commands c WHERE c.task_id=tasks.id AND c.id='start-'||tasks.id)) FROM tasks WHERE project_id=$1`, t.Project.ID).Scan(&active, &started); e != nil {
		return t, e
	}
	var olderPending bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE project_id=$1 AND stage='queued' AND node_id IS NULL AND NOT EXISTS(SELECT 1 FROM commands WHERE commands.task_id=tasks.id AND commands.id='start-'||tasks.id))`, t.Project.ID).Scan(&olderPending); e != nil {
		return t, e
	}
	if active < maxActiveTasksPerProject && started < maxStartedTasksPerProject && !olderPending {
		var node string
		if node, admitted, e = pickTaskNode(ctx, tx, &t); e != nil {
			return t, e
		}
		if admitted {
			t.NodeID = node
			t.AdmissionBackend = "node"
		} else if !distributed {
			admitted = true
			t.Message = ""
		}
	}
	if !admitted {
		var global, project int
		if e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE project_id=$1) FROM tasks WHERE stage='queued' AND node_id IS NULL AND NOT EXISTS(SELECT 1 FROM commands WHERE commands.task_id=tasks.id AND commands.id='start-'||tasks.id)`, t.Project.ID).Scan(&global, &project); e != nil {
			return t, e
		}
		if global >= maxPendingTasks || project >= maxPendingTasksPerProject {
			return t, ErrAdmissionQueueFull
		}
		t.AdmissionStatus = "waiting"
		if started >= maxStartedTasksPerProject {
			t.Message = "等待项目工作流容量"
		} else if active >= maxActiveTasksPerProject {
			t.Message = "等待项目并发容量"
		} else if t.Message == "" {
			t.Message = "等待可用执行节点"
		}
	} else {
		t.AdmissionStatus = "assigned"
	}
	b, _ := json.Marshal(t)
	var nodeID any
	if t.NodeID != "" {
		nodeID = t.NodeID
	}
	_, e = tx.Exec(ctx, "INSERT INTO tasks(id,project_id,source,stage,node_id,body) VALUES($1,$2,$3,$4,$5,$6)", t.ID, t.Project.ID, source, t.Stage, nodeID, b)
	if e != nil {
		return t, mapErr(e)
	}
	if len(t.TriggerNoteIDs) > 0 {
		if _, e = tx.Exec(ctx, `UPDATE issue_run_commands SET claimed_task_id=$2 WHERE source=$1 AND note_id=ANY($3) AND claimed_task_id IS NULL`, source, t.ID, t.TriggerNoteIDs); e != nil {
			return t, e
		}
	}
	if admitted {
		c := domain.Command{ID: "start-" + t.ID, Kind: "start"}
		cb, _ := json.Marshal(c)
		_, e = tx.Exec(ctx, "INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3)", c.ID, t.ID, cb)
		if e != nil {
			return t, e
		}
		if e = s.insertCommandJob(ctx, tx, c.ID, t.ID); e != nil {
			return t, e
		}
	}
	return t, tx.Commit(ctx)
}

// AdmitPendingTasks assigns durable queued tasks and creates their start
// commands in the same transaction. The global allocation lock also covers
// task creation and node configuration changes.
func (s *Store) AdmitPendingTasks(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(84261302)"); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, imageGuardLock); err != nil {
		return 0, err
	}
	// Interleave projects while retaining FIFO within each project.
	rows, err := tx.Query(ctx, `WITH waiting AS (
 SELECT t.body,t.project_id,t.updated_at,t.id,row_number() OVER(PARTITION BY t.project_id ORDER BY t.updated_at,t.id) AS project_rank
 FROM tasks t WHERE t.stage='queued' AND t.node_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM commands c WHERE c.task_id=t.id AND c.id='start-'||t.id)
 ) SELECT t.body FROM waiting t
 LEFT JOIN (SELECT project_id,count(*) AS running FROM tasks WHERE `+activeProjectTask+` GROUP BY project_id) r ON r.project_id=t.project_id
 ORDER BY t.project_rank,COALESCE(r.running,0),t.updated_at,t.id LIMIT $1`, maxPendingTasks)
	if err != nil {
		return 0, err
	}
	queued := []domain.Task{}
	for rows.Next() {
		var body []byte
		var t domain.Task
		if err = rows.Scan(&body); err == nil {
			err = json.Unmarshal(body, &t)
		}
		if err != nil {
			break
		}
		queued = append(queued, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	admitted := 0
	admittedIDs := make([]string, 0, limit)
	for _, t := range queued {
		if admitted >= limit {
			break
		}
		var active, started int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE `+activeProjectTask+`),count(*) FILTER(WHERE NOT terminal AND EXISTS(SELECT 1 FROM commands c WHERE c.task_id=tasks.id AND c.id='start-'||tasks.id)) FROM tasks WHERE project_id=$1`, t.Project.ID).Scan(&active, &started); err != nil {
			return 0, err
		}
		if active >= maxActiveTasksPerProject || started >= maxStartedTasksPerProject {
			continue
		}
		var node string
		var found bool
		if t.AdmissionBackend == "local" {
			found = true
		} else {
			if node, found, err = pickTaskNode(ctx, tx, &t); err != nil {
				return 0, err
			}
		}
		if !found {
			continue
		}
		t.NodeID = node
		t.AdmissionStatus = "assigned"
		if node == "" {
			t.Message = "等待本地工作流启动"
		} else {
			t.Message = "已分配执行节点，等待启动"
		}
		t.UpdatedAt = time.Now().UTC()
		body, marshalErr := json.Marshal(t)
		if marshalErr != nil {
			return 0, marshalErr
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE tasks SET node_id=$2,body=$3,updated_at=now() WHERE id=$1 AND stage='queued' AND node_id IS NULL`, t.ID, node, body)
		if updateErr != nil {
			return 0, updateErr
		}
		if tag.RowsAffected() != 1 {
			continue
		}
		command := domain.Command{ID: "start-" + t.ID, Kind: "start"}
		cb, _ := json.Marshal(command)
		if _, err = tx.Exec(ctx, `INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3)`, command.ID, t.ID, cb); err != nil {
			return 0, mapErr(err)
		}
		if err = s.insertCommandJob(ctx, tx, command.ID, t.ID); err != nil {
			return 0, err
		}
		admitted++
		admittedIDs = append(admittedIDs, t.ID)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	for _, id := range admittedIDs {
		slog.Info("task admitted", "task_id", id, "backend", "local_or_node", "command_id", "start-"+id)
	}
	return admitted, nil
}

// CancelPendingTask completes a task that has not been assigned or started.
// It serializes with admission so no start command can appear after cancel.
func (s *Store) CancelPendingTask(ctx context.Context, taskID string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(84261302)"); err != nil {
		return false, err
	}
	var body []byte
	var stage string
	var nodeID *string
	if err = tx.QueryRow(ctx, `SELECT body,stage,node_id FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&body, &stage, &nodeID); err != nil {
		return false, mapErr(err)
	}
	var t domain.Task
	if err = json.Unmarshal(body, &t); err != nil {
		return false, err
	}
	var hasStart bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commands WHERE id=$1)`, "start-"+taskID).Scan(&hasStart); err != nil {
		return false, err
	}
	if stage != "queued" || t.AdmissionStatus != "waiting" || nodeID != nil || hasStart {
		return false, tx.Commit(ctx)
	}
	t.Stage = "cancelled"
	t.AdmissionStatus = "cancelled"
	t.Message = "等待准入期间已取消"
	t.UpdatedAt = time.Now().UTC()
	updated, err := json.Marshal(t)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET stage='cancelled',terminal=true,body=$2,updated_at=now() WHERE id=$1`, taskID, updated); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE commands SET delivered=true WHERE task_id=$1 AND NOT delivered`, taskID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
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

type GitLabSyncTask struct {
	Task      domain.Task
	UpdatedAt time.Time
}

func (s *Store) PendingGitLabSync(ctx context.Context) ([]GitLabSyncTask, error) {
	rows, e := s.Pool.Query(ctx, `SELECT body,updated_at FROM tasks WHERE (gitlab_synced_at IS NULL OR updated_at>gitlab_synced_at) AND (gitlab_sync_retry_at IS NULL OR gitlab_sync_retry_at<=now()) ORDER BY updated_at,id LIMIT 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []GitLabSyncTask{}
	for rows.Next() {
		var item GitLabSyncTask
		var body []byte
		if e = rows.Scan(&body, &item.UpdatedAt); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(body, &item.Task); e != nil {
			return nil, e
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CompleteGitLabSync(ctx context.Context, id string, updatedAt time.Time) error {
	_, e := s.Pool.Exec(ctx, `UPDATE tasks SET gitlab_synced_at=$2,gitlab_sync_retry_at=NULL WHERE id=$1 AND updated_at=$2`, id, updatedAt)
	return e
}

func (s *Store) DelayGitLabSync(ctx context.Context, id string) error {
	_, e := s.Pool.Exec(ctx, `UPDATE tasks SET gitlab_sync_retry_at=now()+interval '1 minute' WHERE id=$1`, id)
	return e
}
func (s *Store) SaveTask(ctx context.Context, t domain.Task) error {
	t.UpdatedAt = time.Now().UTC()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, imageGuardLock); e != nil {
		return e
	}
	b, _ := json.Marshal(t)
	tag, e := tx.Exec(ctx, "UPDATE tasks SET stage=$2,terminal=$3,body=$4,updated_at=now() WHERE id=$1", t.ID, t.Stage, domain.Terminal(t.Stage), b)
	if e == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM task_mrs WHERE task_id=$1`, t.ID); e != nil {
		return e
	}
	if t.Project.HostingProvider() == domain.ProviderGitLab {
		for _, revision := range t.Workspace.Revisions {
			if revision.MRIID < 1 || revision.ProjectID < 1 {
				continue
			}
			if _, e = tx.Exec(ctx, `INSERT INTO task_mrs(instance,gitlab_project_id,mr_iid,task_id,repository_id) VALUES($1,$2,$3,$4,$5)`, domain.GitLabIdentity(t.Project.GitLabURL), revision.ProjectID, revision.MRIID, t.ID, revision.RepositoryID); e != nil {
				return mapErr(e)
			}
		}
	}
	return tx.Commit(ctx)
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
	if c.Kind == "note" {
		handled, err := s.enqueueStoredNote(ctx, taskID, c)
		if handled || err != nil {
			return err
		}
	}
	b, _ := json.Marshal(c)
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", c.ID, taskID, b)
	if e != nil {
		return mapErr(e)
	}
	if tag.RowsAffected() == 0 {
		var old []byte
		var tid string
		e = tx.QueryRow(ctx, "SELECT task_id,body FROM commands WHERE id=$1", c.ID).Scan(&tid, &old)
		var oc domain.Command
		_ = json.Unmarshal(old, &oc)
		if e != nil {
			return e
		}
		ob, _ := json.Marshal(oc)
		if tid != taskID || string(ob) != string(b) {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if e = s.insertCommandJob(ctx, tx, c.ID, taskID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Store) insertCommandJob(ctx context.Context, tx pgx.Tx, id, taskID string) error {
	if s.Jobs == nil {
		return nil
	}
	return s.Jobs.InsertCommand(ctx, tx, jobs.Command{ID: id, TaskID: taskID})
}

// EnqueueAutoMerge serializes the operator's decision with SaveTask's stage
// update. A request accepted before the merging stage is visible to the merge
// activity even if command delivery to Temporal is delayed.
func (s *Store) EnqueueAutoMerge(ctx context.Context, taskID string, c domain.Command) error {
	if c.Kind != "set_auto_merge" || c.AutoMerge == nil {
		return ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var stage string
	if err = tx.QueryRow(ctx, "SELECT stage FROM tasks WHERE id=$1 FOR UPDATE", taskID).Scan(&stage); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var oldTaskID string
	var oldBody []byte
	err = tx.QueryRow(ctx, "SELECT task_id,body FROM commands WHERE id=$1", c.ID).Scan(&oldTaskID, &oldBody)
	if err == nil {
		var old domain.Command
		if json.Unmarshal(oldBody, &old) != nil || oldTaskID != taskID || !reflect.DeepEqual(old, c) {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if domain.Terminal(stage) || stage == "merging" || stage == "partial_merge" || stage == "finalizing" {
		return ErrConflict
	}
	var manualMergeRequested bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commands WHERE task_id=$1 AND body->>'kind'='merge_round')`, taskID).Scan(&manualMergeRequested); err != nil {
		return err
	}
	if manualMergeRequested {
		return ErrConflict
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3)", c.ID, taskID, body); err != nil {
		return mapErr(err)
	}
	if err = s.insertCommandJob(ctx, tx, c.ID, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) LatestAutoMergeRequest(ctx context.Context, taskID string) (*bool, error) {
	var body []byte
	err := s.Pool.QueryRow(ctx, "SELECT body FROM commands WHERE task_id=$1 AND body->>'kind'='set_auto_merge' ORDER BY created_at DESC,id DESC LIMIT 1", taskID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var command domain.Command
	if err = json.Unmarshal(body, &command); err != nil {
		return nil, err
	}
	return command.AutoMerge, nil
}

type PendingCommand struct {
	TaskID  string
	Command domain.Command
}

func (s *Store) Pending(ctx context.Context) ([]PendingCommand, error) {
	rows, e := s.Pool.Query(ctx, `SELECT c.task_id,c.body FROM commands c WHERE NOT c.delivered AND
 (c.body->>'kind'='start' OR EXISTS(SELECT 1 FROM commands start_command WHERE start_command.id='start-'||c.task_id AND start_command.delivered))
 ORDER BY c.created_at,c.id LIMIT 100`)
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
	tag, e := tx.Exec(ctx, `INSERT INTO nodes(id,address,capacity,version,protocol_version,last_seen,readiness) VALUES($1,$2,$3,$4,$5,now(),$6)
 ON CONFLICT(id) DO UPDATE SET address=excluded.address,capacity=excluded.capacity,version=excluded.version,protocol_version=excluded.protocol_version,last_seen=now(),readiness=excluded.readiness
 WHERE nodes.address=excluded.address OR nodes.last_seen<now()-interval '30 seconds'`, n.ID, n.Address, n.Capacity, n.Version, n.ProtocolVersion, b)
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
	e := s.Pool.QueryRow(ctx, "SELECT id,address,capacity,version,protocol_version,last_seen,readiness FROM nodes WHERE id=$1", id).Scan(&n.ID, &n.Address, &n.Capacity, &n.Version, &n.ProtocolVersion, &n.LastSeen, &readiness)
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
	rows, e := s.Pool.Query(ctx, "SELECT id,address,capacity,version,protocol_version,last_seen,readiness FROM nodes ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Node{}
	for rows.Next() {
		var n domain.Node
		var readiness []byte
		if e = rows.Scan(&n.ID, &n.Address, &n.Capacity, &n.Version, &n.ProtocolVersion, &n.LastSeen, &readiness); e != nil {
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
	rows, e := s.Pool.Query(ctx, `SELECT n.id,n.address,n.capacity,n.version,n.protocol_version,n.last_seen,n.readiness,
		COALESCE(r.running,0), (n.last_seen > now() - $1::interval) AS online
		FROM nodes n
		LEFT JOIN (
			SELECT node_id,count(*) AS running FROM (
 SELECT node_id FROM tasks WHERE `+activeNodeTask+`
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
		if e = rows.Scan(&v.Node.ID, &v.Node.Address, &v.Node.Capacity, &v.Node.Version, &v.Node.ProtocolVersion, &v.Node.LastSeen, &readiness, &v.Running, &v.Online); e != nil {
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
