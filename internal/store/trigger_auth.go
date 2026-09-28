package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"swarmjet/internal/domain"
)

func (s *Store) RecordExternalTriggerAudit(ctx context.Context, projectID, provider, source, kind string, eventID, externalID int64, decision, reason string) error {
	details, err := json.Marshal(map[string]any{"source": source, "kind": kind, "eventId": eventID, "externalId": externalID, "decision": decision, "reason": reason})
	if err != nil {
		return err
	}
	dedupe := fmt.Sprintf("trigger:%s:%s:%s:%s:%d", projectID, provider, source, kind, eventID)
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_events(actor_type,action,target_type,target_id,details,dedupe_key)
		VALUES($1,$2,'project',$3,$4,$5) ON CONFLICT DO NOTHING`, provider, "trigger."+kind+"."+decision, projectID, details, dedupe)
	return err
}

// AuthorizedGitLabActor resolves current direct and team project grants for a
// verified GitLab user ID. Disabled or unapproved accounts never authorize.
func (s *Store) AuthorizedGitLabActor(ctx context.Context, projectID, instance string, externalID int64, allowOperator bool) (bool, error) {
	if projectID == "" || externalID <= 0 {
		return false, nil
	}
	var allowed bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM external_identities e
		JOIN users u ON u.id=e.user_id
		WHERE e.provider='gitlab' AND e.instance=$1 AND e.external_id=$2
		AND u.status='active' AND (
			EXISTS (SELECT 1 FROM project_grants g WHERE g.user_id=u.id AND g.project_id=$3 AND
				(g.role='owner' OR ($4 AND g.role='operator')))
			OR EXISTS (SELECT 1 FROM team_members m JOIN team_project_grants g ON g.team_id=m.team_id
				WHERE m.user_id=u.id AND g.project_id=$3 AND (g.role='owner' OR ($4 AND g.role='operator')))
		)
	)`, domain.GitLabIdentity(instance), strconv.FormatInt(externalID, 10), projectID, allowOperator).Scan(&allowed)
	return allowed, err
}

func (s *Store) DeniedIssueLabelEvent(ctx context.Context, base string, issue domain.Issue, eventID int64) (bool, error) {
	var denied bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM denied_issue_label_events WHERE source=$1 AND event_id=$2)`, issueSource(base, issue), eventID).Scan(&denied)
	return denied, err
}

func (s *Store) RecordDeniedIssueLabelEvent(ctx context.Context, base string, issue domain.Issue, eventID int64, reason string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO denied_issue_label_events(source,event_id,reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, issueSource(base, issue), eventID, reason)
	return err
}

func (s *Store) DeniedIssueRunCommand(ctx context.Context, base string, issue domain.Issue, noteID int64) (bool, error) {
	var denied bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM denied_issue_run_commands WHERE source=$1 AND note_id=$2)`, issueSource(base, issue), noteID).Scan(&denied)
	return denied, err
}

func (s *Store) RecordDeniedIssueRunCommand(ctx context.Context, base string, issue domain.Issue, noteID int64, reason string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	source := issueSource(base, issue)
	if _, err = tx.Exec(ctx, `INSERT INTO denied_issue_run_commands(source,note_id,reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, source, noteID, reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM issue_run_commands WHERE source=$1 AND note_id=$2 AND claimed_task_id IS NULL`, source, noteID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
