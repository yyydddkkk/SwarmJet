package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectRole string

const (
	RoleViewer   ProjectRole = "viewer"
	RoleOperator ProjectRole = "operator"
	RoleOwner    ProjectRole = "owner"
)

func roleRank(role ProjectRole) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleOwner:
		return 3
	default:
		return 0
	}
}

func (s *Store) ProjectRole(ctx context.Context, userID, projectID string) (ProjectRole, error) {
	var rank int
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(rank),0) FROM (
		SELECT CASE role WHEN 'owner' THEN 3 WHEN 'operator' THEN 2 ELSE 1 END AS rank
		FROM project_grants WHERE user_id=$1 AND project_id=$2
		UNION ALL
		SELECT CASE g.role WHEN 'owner' THEN 3 WHEN 'operator' THEN 2 ELSE 1 END
		FROM team_project_grants g JOIN team_members m ON m.team_id=g.team_id
		WHERE m.user_id=$1 AND g.project_id=$2
	) grants WHERE EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active')`, userID, projectID).Scan(&rank)
	if err != nil {
		return "", err
	}
	switch rank {
	case 1:
		return RoleViewer, nil
	case 2:
		return RoleOperator, nil
	case 3:
		return RoleOwner, nil
	default:
		return "", nil
	}
}

func (s *Store) VisibleProjectIDs(ctx context.Context, userID string) (map[string]ProjectRole, error) {
	rows, err := s.Pool.Query(ctx, `SELECT project_id,MAX(rank) FROM (
		SELECT project_id,CASE role WHEN 'owner' THEN 3 WHEN 'operator' THEN 2 ELSE 1 END AS rank
		FROM project_grants WHERE user_id=$1
		UNION ALL
		SELECT g.project_id,CASE g.role WHEN 'owner' THEN 3 WHEN 'operator' THEN 2 ELSE 1 END
		FROM team_project_grants g JOIN team_members m ON m.team_id=g.team_id WHERE m.user_id=$1
	) grants WHERE EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active') GROUP BY project_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProjectRole{}
	for rows.Next() {
		var id string
		var rank int
		if err := rows.Scan(&id, &rank); err != nil {
			return nil, err
		}
		switch rank {
		case 1:
			out[id] = RoleViewer
		case 2:
			out[id] = RoleOperator
		case 3:
			out[id] = RoleOwner
		}
	}
	return out, rows.Err()
}

type ProjectMember struct {
	UserID      string      `json:"userId"`
	Email       string      `json:"email"`
	DisplayName string      `json:"displayName"`
	Role        ProjectRole `json:"role"`
}

func (s *Store) ProjectMembers(ctx context.Context, projectID string) ([]ProjectMember, error) {
	rows, err := s.Pool.Query(ctx, `SELECT g.user_id,u.email,u.display_name,g.role FROM project_grants g JOIN users u ON u.id=g.user_id WHERE g.project_id=$1 ORDER BY u.email`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectMember{}
	for rows.Next() {
		var member ProjectMember
		if err := rows.Scan(&member.UserID, &member.Email, &member.DisplayName, &member.Role); err != nil {
			return nil, err
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

func (s *Store) SetProjectGrant(ctx context.Context, projectID, userID string, role ProjectRole) error {
	if roleRank(role) == 0 {
		return errors.New("invalid project role")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := checkOwnerChange(ctx, tx, projectID, userID, role); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `INSERT INTO project_grants(project_id,user_id,role)
		SELECT $1,id,$3 FROM users WHERE id=$2 AND status='active'
		ON CONFLICT(project_id,user_id) DO UPDATE SET role=EXCLUDED.role`, projectID, userID, role)
	if err != nil {
		return mapErr(err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteProjectGrant(ctx context.Context, projectID, userID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := checkOwnerChange(ctx, tx, projectID, userID, ""); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2`, projectID, userID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func checkOwnerChange(ctx context.Context, tx pgx.Tx, projectID, userID string, nextRole ProjectRole) error {
	// Serialize membership changes to the same project so two owners cannot
	// concurrently remove each other and leave a project without an owner.
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, projectID).Scan(&locked); err != nil {
		return mapErr(err)
	}
	var current ProjectRole
	err := tx.QueryRow(ctx, `SELECT role FROM project_grants WHERE project_id=$1 AND user_id=$2`, projectID, userID).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if current != RoleOwner || nextRole == RoleOwner {
		return nil
	}
	var other bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_grants g JOIN users u ON u.id=g.user_id
		WHERE g.project_id=$1 AND g.user_id<>$2 AND g.role='owner' AND u.status='active')`, projectID, userID).Scan(&other); err != nil {
		return err
	}
	if !other {
		return ErrConflict
	}
	return nil
}

type TeamMember struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

type TeamProject struct {
	ProjectID string      `json:"projectId"`
	Role      ProjectRole `json:"role"`
}

type TeamView struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Members  []TeamMember  `json:"members"`
	Projects []TeamProject `json:"projects"`
}

func (s *Store) Teams(ctx context.Context) ([]TeamView, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name FROM teams ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := []TeamView{}
	for rows.Next() {
		var team TeamView
		if err = rows.Scan(&team.ID, &team.Name); err != nil {
			break
		}
		team.Members = []TeamMember{}
		team.Projects = []TeamProject{}
		out = append(out, team)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		members, err := s.Pool.Query(ctx, `SELECT m.user_id,u.email,u.display_name FROM team_members m JOIN users u ON u.id=m.user_id WHERE m.team_id=$1 ORDER BY u.email`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for members.Next() {
			var member TeamMember
			if err = members.Scan(&member.UserID, &member.Email, &member.DisplayName); err != nil {
				break
			}
			out[i].Members = append(out[i].Members, member)
		}
		if err == nil {
			err = members.Err()
		}
		members.Close()
		if err != nil {
			return nil, err
		}
		projects, err := s.Pool.Query(ctx, `SELECT project_id,role FROM team_project_grants WHERE team_id=$1 ORDER BY project_id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for projects.Next() {
			var project TeamProject
			if err = projects.Scan(&project.ProjectID, &project.Role); err != nil {
				break
			}
			out[i].Projects = append(out[i].Projects, project)
		}
		if err == nil {
			err = projects.Err()
		}
		projects.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) CreateTeam(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return errors.New("invalid team name")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO teams(id,name) VALUES($1,$2)`, id, name)
	return mapErr(err)
}

func (s *Store) AddTeamMember(ctx context.Context, teamID, userID string) error {
	command, err := s.Pool.Exec(ctx, `INSERT INTO team_members(team_id,user_id)
		SELECT $1,id FROM users WHERE id=$2 AND status='active' ON CONFLICT DO NOTHING`, teamID, userID)
	if err != nil {
		return mapErr(err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteTeamMember(ctx context.Context, teamID, userID string) error {
	command, err := s.Pool.Exec(ctx, `DELETE FROM team_members WHERE team_id=$1 AND user_id=$2`, teamID, userID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetTeamProjectGrant(ctx context.Context, teamID, projectID string, role ProjectRole) error {
	if roleRank(role) == 0 {
		return errors.New("invalid project role")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO team_project_grants(team_id,project_id,role) VALUES($1,$2,$3)
		ON CONFLICT(team_id,project_id) DO UPDATE SET role=EXCLUDED.role`, teamID, projectID, role)
	return mapErr(err)
}

func (s *Store) DeleteTeamProjectGrant(ctx context.Context, teamID, projectID string) error {
	command, err := s.Pool.Exec(ctx, `DELETE FROM team_project_grants WHERE team_id=$1 AND project_id=$2`, teamID, projectID)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type AuditEvent struct {
	ID         int64           `json:"id"`
	ActorID    *string         `json:"actorId,omitempty"`
	ActorType  string          `json:"actorType"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Details    json.RawMessage `json:"details"`
	CreatedAt  time.Time       `json:"createdAt"`
}

func (s *Store) AuditEvents(ctx context.Context, projectID string, limit int) ([]AuditEvent, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	var rows pgx.Rows
	var err error
	if projectID == "" {
		rows, err = s.Pool.Query(ctx, `SELECT id,actor_user_id,actor_type,action,target_type,target_id,details,created_at FROM audit_events ORDER BY id DESC LIMIT $1`, limit)
	} else {
		rows, err = s.Pool.Query(ctx, `SELECT id,actor_user_id,actor_type,action,target_type,target_id,details,created_at FROM audit_events WHERE target_type='project' AND target_id=$1 ORDER BY id DESC LIMIT $2`, projectID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var event AuditEvent
		if err = rows.Scan(&event.ID, &event.ActorID, &event.ActorType, &event.Action, &event.TargetType, &event.TargetID, &event.Details, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}
