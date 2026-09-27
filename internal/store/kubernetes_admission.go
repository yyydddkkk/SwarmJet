package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"swarmjet/internal/domain"
)

var ErrExecutionAuthority = errors.New("kubernetes execution authority changed")

// KubernetesExecution is the durable fencing record. The caller must pass its
// authority version to every result or remote-write operation.
type KubernetesExecution struct {
	ExecutionID      string
	TaskID           string
	Role             string
	AuthorityVersion int64
	State            string
	AttemptID        string
	JobCreation      string
	CheckpointValid  bool
}

func (s *Store) CreateKubernetesTask(ctx context.Context, task domain.Task, pool string, resources domain.RunnerResources) (domain.Task, error) {
	task.Stage = "queued"
	task.AdmissionBackend = "kubernetes"
	task.ExecutionPool = pool
	task.RunnerResources = resources
	return s.createTask(ctx, task, false)
}

// AdmitKubernetesTasks serializes capacity checks, admission, and start-command
// insertion with task creation and cancellation. capacity is the pool's maximum
// admitted task count; a terminal task stops consuming it.
func (s *Store) AdmitKubernetesTasks(ctx context.Context, pool string, capacity, limit int) (int, error) {
	if pool == "" || capacity < 1 || limit < 1 {
		return 0, fmt.Errorf("invalid Kubernetes admission limits")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84261302)`); err != nil {
		return 0, err
	}
	var used int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM kubernetes_admissions a JOIN tasks t ON t.id=a.task_id WHERE a.pool=$1 AND ((a.state='admitted' AND NOT t.terminal) OR EXISTS(SELECT 1 FROM kubernetes_executions e WHERE e.task_id=a.task_id AND e.state IN ('pending','active','revoked')))`, pool).Scan(&used); err != nil {
		return 0, err
	}
	free := capacity - used
	if free <= 0 {
		return 0, tx.Commit(ctx)
	}
	if free > limit {
		free = limit
	}
	rows, err := tx.Query(ctx, `SELECT a.task_id,t.project_id FROM kubernetes_admissions a JOIN tasks t ON t.id=a.task_id WHERE a.pool=$1 AND a.state='waiting' AND t.stage='queued' ORDER BY a.created_at,a.task_id LIMIT $2`, pool, maxPendingTasks)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, project string }
	var waiting []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.project); err != nil {
			break
		}
		waiting = append(waiting, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range waiting {
		if count >= free {
			break
		}
		var active, started int
		if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE `+activeProjectTask+`),count(*) FILTER(WHERE NOT terminal AND EXISTS(SELECT 1 FROM commands c WHERE c.task_id=tasks.id AND c.id='start-'||tasks.id)) FROM tasks WHERE project_id=$1`, c.project).Scan(&active, &started); err != nil {
			return 0, err
		}
		if active >= maxActiveTasksPerProject || started >= maxStartedTasksPerProject {
			continue
		}
		var body []byte
		if err = tx.QueryRow(ctx, `SELECT body FROM tasks WHERE id=$1 FOR UPDATE`, c.id).Scan(&body); err != nil {
			return 0, err
		}
		var task domain.Task
		if err = json.Unmarshal(body, &task); err != nil {
			return 0, err
		}
		task.AdmissionStatus = "assigned"
		task.Message = "已准入 Kubernetes 执行池，等待启动"
		task.UpdatedAt = time.Now().UTC()
		body, err = json.Marshal(task)
		if err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `UPDATE tasks SET body=$2,updated_at=now() WHERE id=$1`, c.id, body); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `UPDATE kubernetes_admissions SET state='admitted',admitted_at=now() WHERE task_id=$1 AND state='waiting'`, c.id); err != nil {
			return 0, err
		}
		command := domain.Command{ID: "start-" + c.id, Kind: "start"}
		cb, _ := json.Marshal(command)
		if _, err = tx.Exec(ctx, `INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3)`, command.ID, c.id, cb); err != nil {
			return 0, mapErr(err)
		}
		count++
	}
	return count, tx.Commit(ctx)
}

// CancelKubernetesTask cancels only a waiting task. An admitted task must
// revoke and stop its Runner before being completed by the workflow.
func (s *Store) CancelKubernetesTask(ctx context.Context, taskID string) (bool, error) {
	return s.CancelPendingTask(ctx, taskID)
}

func lockKubernetesAdmission(ctx context.Context, tx pgx.Tx, taskID string) (string, bool, error) {
	var state string
	var terminal bool
	err := tx.QueryRow(ctx, `SELECT a.state,t.terminal FROM kubernetes_admissions a JOIN tasks t ON t.id=a.task_id WHERE a.task_id=$1 FOR UPDATE OF a,t`, taskID).Scan(&state, &terminal)
	return state, terminal, mapErr(err)
}

// FreezeKubernetesAdmission prevents a concurrent Begin, Acquire or Job create
// from gaining execution rights while cancellation is being resolved. A zero
// ExecutionID means no execution was ever created. The returned execution is
// observational; Stop must refresh it before changing its fenced state.
func (s *Store) FreezeKubernetesAdmission(ctx context.Context, taskID string) (KubernetesExecution, error) {
	var out KubernetesExecution
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, _, err := lockKubernetesAdmission(ctx, tx, taskID)
	if err != nil {
		return out, err
	}
	if state != "cancelled" {
		if _, err = tx.Exec(ctx, `UPDATE kubernetes_admissions SET state='cancelled' WHERE task_id=$1`, taskID); err != nil {
			return out, err
		}
	}
	err = tx.QueryRow(ctx, `SELECT execution_id,task_id,role,authority_version,state,attempt_id,job_creation,checkpoint_valid FROM kubernetes_executions WHERE task_id=$1 ORDER BY CASE WHEN state IN ('pending','active','revoked','stopped') THEN 0 ELSE 1 END,created_at DESC,execution_id DESC LIMIT 1`, taskID).Scan(&out.ExecutionID, &out.TaskID, &out.Role, &out.AuthorityVersion, &out.State, &out.AttemptID, &out.JobCreation, &out.CheckpointValid)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// BeginKubernetesExecution makes the supplied ID stable across Pod replacement.
// A single unfinished execution per task prevents concurrent Agent writers.
func (s *Store) BeginKubernetesExecution(ctx context.Context, taskID, role, executionID string) (KubernetesExecution, error) {
	var out KubernetesExecution
	if taskID == "" || role == "" || executionID == "" {
		return out, fmt.Errorf("execution identity is required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, terminal, err := lockKubernetesAdmission(ctx, tx, taskID)
	if err != nil {
		return out, err
	}
	if state != "admitted" || terminal {
		return out, ErrExecutionAuthority
	}
	_, err = tx.Exec(ctx, `INSERT INTO kubernetes_executions(execution_id,task_id,role,state) VALUES($1,$2,$3,'pending') ON CONFLICT (execution_id) DO NOTHING`, executionID, taskID, role)
	if err != nil {
		return out, mapErr(err)
	}
	if err = tx.QueryRow(ctx, `SELECT execution_id,task_id,role,authority_version,state,attempt_id,job_creation,checkpoint_valid FROM kubernetes_executions WHERE execution_id=$1`, executionID).Scan(&out.ExecutionID, &out.TaskID, &out.Role, &out.AuthorityVersion, &out.State, &out.AttemptID, &out.JobCreation, &out.CheckpointValid); err != nil {
		return out, err
	}
	if out.TaskID != taskID || out.Role != role {
		return out, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET body=jsonb_set(body,'{executionId}',to_jsonb($2::text),true),updated_at=now() WHERE id=$1`, taskID, executionID); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) KubernetesExecution(ctx context.Context, executionID string) (KubernetesExecution, error) {
	var out KubernetesExecution
	err := s.Pool.QueryRow(ctx, `SELECT execution_id,task_id,role,authority_version,state,attempt_id,job_creation,checkpoint_valid FROM kubernetes_executions WHERE execution_id=$1`, executionID).Scan(&out.ExecutionID, &out.TaskID, &out.Role, &out.AuthorityVersion, &out.State, &out.AttemptID, &out.JobCreation, &out.CheckpointValid)
	return out, mapErr(err)
}

// AcquireKubernetesExecution fences a newly created Job/Pod. Only a pending
// execution can gain its first version; replacement uses TakeOver below.
func (s *Store) AcquireKubernetesExecution(ctx context.Context, executionID, attemptID string) (int64, error) {
	if attemptID == "" {
		return 0, fmt.Errorf("runner attempt ID is required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var taskID string
	if err = tx.QueryRow(ctx, `SELECT task_id FROM kubernetes_executions WHERE execution_id=$1`, executionID).Scan(&taskID); err != nil {
		return 0, mapErr(err)
	}
	state, terminal, err := lockKubernetesAdmission(ctx, tx, taskID)
	if err != nil {
		return 0, err
	}
	if state != "admitted" || terminal {
		return 0, ErrExecutionAuthority
	}
	var version int64
	err = tx.QueryRow(ctx, `UPDATE kubernetes_executions SET state='active',attempt_id=$2,authority_version=authority_version+1,job_creation='none',updated_at=now() WHERE execution_id=$1 AND state='pending' AND job_creation='none' RETURNING authority_version`, executionID, attemptID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrExecutionAuthority
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET body=jsonb_set(jsonb_set(body,'{executionId}',to_jsonb($2::text),true),'{authorityVersion}',to_jsonb($3::bigint),true),updated_at=now() WHERE id=$1`, taskID, executionID, version); err != nil {
		return 0, err
	}
	return version, tx.Commit(ctx)
}

// MarkKubernetesJobCreating is the durable barrier immediately before the
// Kubernetes create request. Once marked, cancellation must treat an empty
// Kubernetes observation as ambiguous until creation is reconciled.
func (s *Store) MarkKubernetesJobCreating(ctx context.Context, executionID string, version int64, attemptID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var taskID string
	if err = tx.QueryRow(ctx, `SELECT task_id FROM kubernetes_executions WHERE execution_id=$1`, executionID).Scan(&taskID); err != nil {
		return mapErr(err)
	}
	state, terminal, err := lockKubernetesAdmission(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if state != "admitted" || terminal {
		return ErrExecutionAuthority
	}
	tag, err := tx.Exec(ctx, `UPDATE kubernetes_executions SET job_creation='creating',updated_at=now() WHERE execution_id=$1 AND authority_version=$2 AND attempt_id=$3 AND state='active' AND job_creation IN ('none','creating')`, executionID, version, attemptID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionAuthority
	}
	return tx.Commit(ctx)
}

// MarkKubernetesJobCreated records a confirmed or reconciled Job. A create
// response may arrive after revocation; recording that fact lets cancellation
// subsequently delete and observe the known attempt.
func (s *Store) MarkKubernetesJobCreated(ctx context.Context, executionID string, version int64, attemptID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE kubernetes_executions SET job_creation='created',updated_at=now() WHERE execution_id=$1 AND authority_version=$2 AND attempt_id=$3 AND state IN ('active','revoked') AND job_creation IN ('creating','created')`, executionID, version, attemptID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionAuthority
	}
	return nil
}

// CancelPendingKubernetesExecution wins only before an attempt acquires its
// first authority version. No Job create can then pass MarkJobCreating.
func (s *Store) CancelPendingKubernetesExecution(ctx context.Context, executionID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE kubernetes_executions SET state='cancelled',updated_at=now() WHERE execution_id=$1 AND state='pending' AND authority_version=0 AND job_creation='none'`, executionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionAuthority
	}
	return nil
}

func (s *Store) RevokeKubernetesExecution(ctx context.Context, executionID string, version int64, attemptID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE kubernetes_executions SET state='revoked',checkpoint_valid=false,updated_at=now() WHERE execution_id=$1 AND authority_version=$2 AND attempt_id=$3 AND state='active'`, executionID, version, attemptID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionAuthority
	}
	return nil
}

// Caller must observe that the old Pod is fully stopped and validate the
// workspace checkpoint before setting checkpointValid=true.
func (s *Store) ConfirmKubernetesPodStopped(ctx context.Context, executionID string, version int64, attemptID string, checkpointValid bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE kubernetes_executions SET state='stopped',checkpoint_valid=$4,updated_at=now() WHERE execution_id=$1 AND authority_version=$2 AND attempt_id=$3 AND state IN ('revoked','stopped') AND job_creation<>'creating'`, executionID, version, attemptID, checkpointValid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionAuthority
	}
	return nil
}

func (s *Store) TakeOverKubernetesExecution(ctx context.Context, executionID string, version int64, newAttemptID string) (int64, error) {
	if newAttemptID == "" {
		return 0, fmt.Errorf("runner attempt ID is required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var taskID string
	if err = tx.QueryRow(ctx, `SELECT task_id FROM kubernetes_executions WHERE execution_id=$1`, executionID).Scan(&taskID); err != nil {
		return 0, mapErr(err)
	}
	state, terminal, err := lockKubernetesAdmission(ctx, tx, taskID)
	if err != nil {
		return 0, err
	}
	if state != "admitted" || terminal {
		return 0, ErrExecutionAuthority
	}
	var next int64
	err = tx.QueryRow(ctx, `UPDATE kubernetes_executions SET state='active',attempt_id=$3,authority_version=authority_version+1,job_creation='none',checkpoint_valid=false,updated_at=now() WHERE execution_id=$1 AND authority_version=$2 AND state='stopped' AND checkpoint_valid AND job_creation<>'creating' AND attempt_id<>$3 RETURNING authority_version`, executionID, version, newAttemptID).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrExecutionAuthority
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET body=jsonb_set(body,'{authorityVersion}',to_jsonb($2::bigint),true),updated_at=now() WHERE id=$1`, taskID, next); err != nil {
		return 0, err
	}
	return next, tx.Commit(ctx)
}

// SaveKubernetesExecutionResult accepts a Runner result only from the current
// holder and persists it with completion in one transaction.
func (s *Store) SaveKubernetesExecutionResult(ctx context.Context, executionID string, version int64, attemptID string, result domain.Execution) error {
	if result.ID != executionID {
		return ErrExecutionAuthority
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var taskID, role, state, holder string
	var current int64
	if err = tx.QueryRow(ctx, `SELECT task_id FROM kubernetes_executions WHERE execution_id=$1`, executionID).Scan(&taskID); err != nil {
		return mapErr(err)
	}
	admission, terminal, err := lockKubernetesAdmission(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if admission != "admitted" || terminal {
		return ErrExecutionAuthority
	}
	if err = tx.QueryRow(ctx, `SELECT role,state,attempt_id,authority_version FROM kubernetes_executions WHERE execution_id=$1 FOR UPDATE`, executionID).Scan(&role, &state, &holder, &current); err != nil {
		return mapErr(err)
	}
	if state != "active" || current != version || holder != attemptID {
		return ErrExecutionAuthority
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks SET body=jsonb_set(body,'{executions}',coalesce(nullif(body->'executions','null'::jsonb),'{}'::jsonb)||jsonb_build_object($2::text,$3::jsonb),true),updated_at=now() WHERE id=$1`, taskID, role, data); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE kubernetes_executions SET state='completed',updated_at=now() WHERE execution_id=$1`, executionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CompleteKubernetesExecution(ctx context.Context, executionID string, version int64, attemptID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE kubernetes_executions SET state='completed',updated_at=now() WHERE execution_id=$1 AND authority_version=$2 AND attempt_id=$3 AND state='active'`, executionID, version, attemptID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionAuthority
	}
	return nil
}

// CheckKubernetesAuthority is the gate for accepting Runner results and
// dispatching trusted platform writes. It must be checked in the same database
// transaction as any persisted result or write intent.
func (s *Store) CheckKubernetesAuthority(ctx context.Context, executionID string, version int64, attemptID string) (bool, error) {
	var valid bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kubernetes_executions e JOIN tasks t ON t.id=e.task_id JOIN kubernetes_admissions a ON a.task_id=t.id WHERE e.execution_id=$1 AND e.authority_version=$2 AND e.attempt_id=$3 AND e.state='active' AND a.state='admitted' AND NOT t.terminal)`, executionID, version, attemptID).Scan(&valid)
	return valid, err
}

// WithKubernetesAuthority holds the execution row lock while fn performs one
// external mutation. Revocation and takeover wait for fn to finish, so a
// successful recheck cannot be invalidated during that mutation. The callback
// should have a bounded context deadline and must be safe to retry after an
// ambiguous network response.
func (s *Store) WithKubernetesAuthority(ctx context.Context, taskID, executionID string, version int64, attemptID string, fn func(context.Context) error) error {
	if fn == nil {
		return fmt.Errorf("authority callback is required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	admission, terminal, err := lockKubernetesAdmission(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if admission != "admitted" || terminal {
		return ErrExecutionAuthority
	}
	var state string
	var gotVersion int64
	var gotAttemptID string
	if err = tx.QueryRow(ctx, `SELECT state,authority_version,attempt_id FROM kubernetes_executions WHERE execution_id=$1 AND task_id=$2 FOR UPDATE`, executionID, taskID).Scan(&state, &gotVersion, &gotAttemptID); err != nil {
		return mapErr(err)
	}
	if state != "active" || gotVersion != version || gotAttemptID != attemptID {
		return ErrExecutionAuthority
	}
	if err = fn(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
