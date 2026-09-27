package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// KubernetesWriteIntent identifies one fixed remote change. The intent is
// recorded before network I/O and can be reconciled after an ambiguous reply.
type KubernetesWriteIntent struct {
	ID               string
	TaskID           string
	ExecutionID      string
	AuthorityVersion int64
	Kind             string
	RepositoryID     string
	ProjectID        int64
	SourceBranch     string
	TargetBranch     string
	HeadSHA          string
	State            string
	RemoteID         int64
	CreatedAt        time.Time
	ConfirmedAt      *time.Time
}

var writeSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (w KubernetesWriteIntent) validate() error {
	if w.ID == "" || len(w.ID) > 200 || w.TaskID == "" || w.ExecutionID == "" || w.AuthorityVersion < 1 || w.RepositoryID == "" || w.ProjectID < 1 {
		return errors.New("incomplete write intent identity")
	}
	if w.Kind != "push" && w.Kind != "mr" && w.Kind != "merge" {
		return errors.New("invalid write intent kind")
	}
	if !writeSHA.MatchString(w.HeadSHA) || !safeWriteRef(w.SourceBranch) || !safeWriteRef(w.TargetBranch) || w.SourceBranch == w.TargetBranch {
		return errors.New("invalid write intent revision or branch")
	}
	return nil
}

func safeWriteRef(ref string) bool {
	if ref == "" || len(ref) > 240 || strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return false
	}
	for _, c := range ref {
		if c <= ' ' || c == 127 || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	return true
}

func scanKubernetesWrite(row pgx.Row) (KubernetesWriteIntent, error) {
	var w KubernetesWriteIntent
	err := row.Scan(&w.ID, &w.TaskID, &w.ExecutionID, &w.AuthorityVersion, &w.Kind, &w.RepositoryID, &w.ProjectID, &w.SourceBranch, &w.TargetBranch, &w.HeadSHA, &w.State, &w.RemoteID, &w.CreatedAt, &w.ConfirmedAt)
	return w, mapErr(err)
}

const writeIntentColumns = `id,task_id,execution_id,authority_version,kind,repository_id,project_id,source_branch,target_branch,head_sha,state,remote_id,created_at,confirmed_at`

// RecordKubernetesWriteIntent serializes with authority revocation. Reusing an
// ID with different targets is rejected rather than silently changing intent.
func (s *Store) RecordKubernetesWriteIntent(ctx context.Context, w KubernetesWriteIntent, attemptID string) (KubernetesWriteIntent, error) {
	if err := w.validate(); err != nil {
		return KubernetesWriteIntent{}, err
	}
	if attemptID == "" {
		return KubernetesWriteIntent{}, errors.New("attempt identity is required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return KubernetesWriteIntent{}, err
	}
	defer tx.Rollback(ctx)
	admission, terminal, err := lockKubernetesAdmission(ctx, tx, w.TaskID)
	if err != nil {
		return KubernetesWriteIntent{}, err
	}
	if admission != "admitted" || terminal {
		return KubernetesWriteIntent{}, ErrExecutionAuthority
	}
	var state, holder string
	var version int64
	err = tx.QueryRow(ctx, `SELECT state,attempt_id,authority_version FROM kubernetes_executions WHERE execution_id=$1 AND task_id=$2 FOR UPDATE`, w.ExecutionID, w.TaskID).Scan(&state, &holder, &version)
	if err != nil {
		return KubernetesWriteIntent{}, mapErr(err)
	}
	if state != "active" || holder != attemptID || version != w.AuthorityVersion {
		return KubernetesWriteIntent{}, ErrExecutionAuthority
	}
	_, err = tx.Exec(ctx, `INSERT INTO kubernetes_write_intents(id,task_id,execution_id,authority_version,kind,repository_id,project_id,source_branch,target_branch,head_sha) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (id) DO NOTHING`, w.ID, w.TaskID, w.ExecutionID, w.AuthorityVersion, w.Kind, w.RepositoryID, w.ProjectID, w.SourceBranch, w.TargetBranch, w.HeadSHA)
	if err != nil {
		return KubernetesWriteIntent{}, err
	}
	got, err := scanKubernetesWrite(tx.QueryRow(ctx, `SELECT `+writeIntentColumns+` FROM kubernetes_write_intents WHERE id=$1`, w.ID))
	if err != nil {
		return KubernetesWriteIntent{}, err
	}
	if got.TaskID != w.TaskID || got.ExecutionID != w.ExecutionID || got.Kind != w.Kind || got.RepositoryID != w.RepositoryID || got.ProjectID != w.ProjectID || got.SourceBranch != w.SourceBranch || got.TargetBranch != w.TargetBranch || got.HeadSHA != w.HeadSHA {
		return KubernetesWriteIntent{}, fmt.Errorf("write intent %s differs from recorded operation: %w", w.ID, ErrConflict)
	}
	if got.State == "pending" && got.AuthorityVersion != w.AuthorityVersion {
		if _, err = tx.Exec(ctx, `UPDATE kubernetes_write_intents SET authority_version=$2 WHERE id=$1 AND state='pending'`, w.ID, w.AuthorityVersion); err != nil {
			return KubernetesWriteIntent{}, err
		}
		got.AuthorityVersion = w.AuthorityVersion
	}
	return got, tx.Commit(ctx)
}

func (s *Store) KubernetesWriteIntent(ctx context.Context, id string) (KubernetesWriteIntent, error) {
	return scanKubernetesWrite(s.Pool.QueryRow(ctx, `SELECT `+writeIntentColumns+` FROM kubernetes_write_intents WHERE id=$1`, id))
}

// ConfirmKubernetesWriteIntent accepts confirmation only from the current
// holder. If authority was revoked after an ambiguous reply, the next holder
// must reconcile the remote fact before recording completion.
func (s *Store) ConfirmKubernetesWriteIntent(ctx context.Context, id, executionID, attemptID string, version, remoteID int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var taskID string
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
	var state, holder string
	var gotVersion int64
	err = tx.QueryRow(ctx, `SELECT state,attempt_id,authority_version FROM kubernetes_executions WHERE execution_id=$1 FOR UPDATE`, executionID).Scan(&state, &holder, &gotVersion)
	if err != nil {
		return mapErr(err)
	}
	if state != "active" || holder != attemptID || gotVersion != version {
		return ErrExecutionAuthority
	}
	tag, err := tx.Exec(ctx, `UPDATE kubernetes_write_intents SET state='confirmed',remote_id=$4,confirmed_at=now() WHERE id=$1 AND execution_id=$2 AND authority_version=$3 AND state='pending'`, id, executionID, version, remoteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		got, err := scanKubernetesWrite(tx.QueryRow(ctx, `SELECT `+writeIntentColumns+` FROM kubernetes_write_intents WHERE id=$1`, id))
		if err != nil {
			return err
		}
		if got.ExecutionID != executionID || got.AuthorityVersion != version || got.State != "confirmed" || got.RemoteID != remoteID {
			return ErrConflict
		}
	}
	return tx.Commit(ctx)
}
