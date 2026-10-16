package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Personal identity secrets are sealed by the caller and never included in a
// project or task snapshot.
func (s *Store) SaveGitLabIdentity(ctx context.Context, instance, cipher, username string) error {
	instance = domain.GitLabIdentity(instance)
	if instance == "" || cipher == "" || username == "" {
		return ErrConflict
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO gitlab_identities(instance,username,secret) VALUES($1,$2,$3)
		ON CONFLICT(instance) DO UPDATE SET username=excluded.username,secret=excluded.secret,updated_at=now()`, instance, username, cipher)
	return err
}

func (s *Store) GitLabIdentity(ctx context.Context, instance string) (cipher, username string, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT secret,username FROM gitlab_identities WHERE instance=$1`, domain.GitLabIdentity(instance)).Scan(&cipher, &username)
	return cipher, username, mapErr(err)
}

func (s *Store) DeleteGitLabIdentity(ctx context.Context, instance string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM gitlab_identities WHERE instance=$1`, domain.GitLabIdentity(instance))
	return err
}

func (s *Store) TasksForIssue(ctx context.Context, base string, issue domain.Issue) ([]domain.Task, error) {
	rows, err := s.Pool.Query(ctx, `SELECT body FROM tasks WHERE source=$1 ORDER BY COALESCE((body->>'createdAt')::timestamptz,updated_at),id`, issueSource(base, issue))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Task{}
	for rows.Next() {
		var body []byte
		var task domain.Task
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &task); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// TasksForIssueInProject is the only issue-to-task lookup exposed through a
// project-scoped console view. Historical tasks from another SwarmJet project
// can share the same GitLab issue after repository reassignment.
func (s *Store) TasksForIssueInProject(ctx context.Context, projectID, base string, issue domain.Issue) ([]domain.Task, error) {
	rows, err := s.Pool.Query(ctx, `SELECT body FROM tasks WHERE project_id=$1 AND source=$2 ORDER BY COALESCE((body->>'createdAt')::timestamptz,updated_at),id`, projectID, issueSource(base, issue))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Task{}
	for rows.Next() {
		var body []byte
		var task domain.Task
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &task); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *Store) TaskForMR(ctx context.Context, gitlabURL string, gitlabProjectID, iid int64) (domain.Task, error) {
	var body []byte
	err := s.Pool.QueryRow(ctx, `SELECT t.body FROM task_mrs m JOIN tasks t ON t.id=m.task_id WHERE m.instance=$1 AND m.gitlab_project_id=$2 AND m.mr_iid=$3`, domain.GitLabIdentity(gitlabURL), gitlabProjectID, iid).Scan(&body)
	var task domain.Task
	if err == nil {
		err = json.Unmarshal(body, &task)
	}
	return task, mapErr(err)
}

func (s *Store) TaskForMRInProject(ctx context.Context, projectID, gitlabURL string, gitlabProjectID, iid int64) (domain.Task, error) {
	var body []byte
	err := s.Pool.QueryRow(ctx, `SELECT t.body FROM task_mrs m JOIN tasks t ON t.id=m.task_id WHERE t.project_id=$1 AND m.instance=$2 AND m.gitlab_project_id=$3 AND m.mr_iid=$4`, projectID, domain.GitLabIdentity(gitlabURL), gitlabProjectID, iid).Scan(&body)
	var task domain.Task
	if err == nil {
		err = json.Unmarshal(body, &task)
	}
	return task, mapErr(err)
}

type ManualAcceptanceCheck struct {
	Index     int  `json:"index"`
	Confirmed bool `json:"confirmed"`
}

type ManualAcceptanceRecord struct {
	Digest         string                  `json:"digest"`
	Checks         []ManualAcceptanceCheck `json:"checks"`
	Note           string                  `json:"note"`
	ExpectedDigest string                  `json:"expectedDigest,omitempty"`
}

func (s *Store) ManualAcceptance(ctx context.Context, taskID string) (ManualAcceptanceRecord, error) {
	var record ManualAcceptanceRecord
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT digest,checks,note FROM task_manual_acceptance WHERE task_id=$1`, taskID).Scan(&record.Digest, &raw, &record.Note)
	if err != nil {
		return record, mapErr(err)
	}
	err = json.Unmarshal(raw, &record.Checks)
	return record, err
}

// SaveManualAcceptance locks the Task row so a concurrent merge command cannot
// overtake the evidence write. Version binding includes every repository SHA.
func (s *Store) SaveManualAcceptance(ctx context.Context, taskID string, record ManualAcceptanceRecord) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var body []byte
	err = tx.QueryRow(ctx, `SELECT body FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&body)
	if err != nil {
		return mapErr(err)
	}
	var task domain.Task
	if err = json.Unmarshal(body, &task); err != nil {
		return err
	}
	if task.Project.HostingProvider() != domain.ProviderGitLab || task.Stage != "waiting_manual_merge" || task.Paused || task.ReviewDigest == "" || task.ReviewDigest != domain.Digest(task.Workspace.Revisions) || domain.ManualMergeEvidenceDigest(task) != record.Digest || strings.TrimSpace(record.Note) == "" {
		return ErrConflict
	}
	var mergePending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commands WHERE task_id=$1 AND body->>'kind'='merge_round')`, taskID).Scan(&mergePending); err != nil {
		return err
	}
	if mergePending {
		return ErrConflict
	}
	count := 0
	if task.HumanAcceptance != nil {
		count = len(task.HumanAcceptance.Steps) + len(task.HumanAcceptance.Prerequisites)
	}
	if len(record.Checks) != count {
		return ErrConflict
	}
	seen := make(map[int]bool, len(record.Checks))
	for _, c := range record.Checks {
		if c.Index < 0 || c.Index >= len(record.Checks) || seen[c.Index] || !c.Confirmed {
			return ErrConflict
		}
		seen[c.Index] = true
	}
	checks, err := json.Marshal(record.Checks)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_manual_acceptance(task_id,digest,checks,note) VALUES($1,$2,$3,$4) ON CONFLICT(task_id) DO UPDATE SET digest=excluded.digest,checks=excluded.checks,note=excluded.note,updated_at=now()`, taskID, record.Digest, checks, record.Note)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) EnqueueManualMerge(ctx context.Context, taskID string, command domain.Command) error {
	if command.Kind != "merge_round" {
		return ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var stage string
	var body []byte
	err = tx.QueryRow(ctx, `SELECT stage,body FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&stage, &body)
	if err != nil {
		return mapErr(err)
	}
	var task domain.Task
	if err = json.Unmarshal(body, &task); err != nil {
		return err
	}
	var existing string
	var oldBody []byte
	err = tx.QueryRow(ctx, `SELECT task_id,body FROM commands WHERE id=$1`, command.ID).Scan(&existing, &oldBody)
	if err == nil {
		var old domain.Command
		if existing != taskID || json.Unmarshal(oldBody, &old) != nil || !reflect.DeepEqual(old, command) {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if task.Project.HostingProvider() != domain.ProviderGitLab || stage != "waiting_manual_merge" || task.AutoMergeEnabled() && !task.AcceptanceIncomplete || task.Paused {
		return ErrConflict
	}
	var latestAuto []byte
	if autoErr := tx.QueryRow(ctx, `SELECT body FROM commands WHERE task_id=$1 AND body->>'kind'='set_auto_merge' ORDER BY created_at DESC,id DESC LIMIT 1`, taskID).Scan(&latestAuto); autoErr == nil {
		var auto domain.Command
		if json.Unmarshal(latestAuto, &auto) != nil || auto.AutoMerge != nil && *auto.AutoMerge {
			return ErrConflict
		}
	} else if !errors.Is(autoErr, pgx.ErrNoRows) {
		return autoErr
	}
	err = tx.QueryRow(ctx, `SELECT digest FROM task_manual_acceptance WHERE task_id=$1`, taskID).Scan(&existing)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if existing == "" || task.ReviewDigest != domain.Digest(task.Workspace.Revisions) || existing != domain.ManualMergeEvidenceDigest(task) {
		return ErrConflict
	}
	var pending bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commands WHERE task_id=$1 AND body->>'kind'='merge_round')`, taskID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO commands(id,task_id,body) VALUES($1,$2,$3)`, command.ID, taskID, encoded)
	if err != nil {
		return mapErr(err)
	}
	if err = s.insertCommandJob(ctx, tx, command.ID, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
