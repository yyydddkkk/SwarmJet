package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"swarmjet/internal/domain"
)

type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"displayName"`
	PasswordHash  string    `json:"-"`
	Status        string    `json:"status"`
	PlatformAdmin bool      `json:"platformAdmin"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

var ErrEmailCooldown = errors.New("email request cooldown")

type Principal struct {
	UserID        string `json:"userId,omitempty"`
	Email         string `json:"email,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	PlatformAdmin bool   `json:"platformAdmin"`
	Breakglass    bool   `json:"breakglass"`
}

func (s *Store) RegisterUser(ctx context.Context, u User) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,status) VALUES($1,$2,$3,$4,'pending_email')`, u.ID, u.Email, u.DisplayName, u.PasswordHash)
	return mapErr(err)
}

func (s *Store) DeleteUnverifiedUser(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1 AND status='pending_email'`, id)
	return err
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.Status, &u.PlatformAdmin, &u.CreatedAt, &u.UpdatedAt)
	return u, mapErr(err)
}

const userFields = `id,email,display_name,password_hash,status,platform_admin,created_at,updated_at`

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userFields+` FROM users WHERE email=$1`, email))
}

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userFields+` FROM users WHERE id=$1`, id))
}

func (s *Store) ListUsers(ctx context.Context, status string) ([]User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+userFields+` FROM users WHERE ($1='' OR status=$1) ORDER BY created_at,id`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) IssueEmailToken(ctx context.Context, userID, kind, hash string, expiry time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&locked); err != nil {
		return mapErr(err)
	}
	var latest time.Time
	err = tx.QueryRow(ctx, `SELECT created_at FROM email_tokens WHERE user_id=$1 AND kind=$2 ORDER BY created_at DESC LIMIT 1`, userID, kind).Scan(&latest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && time.Since(latest) < time.Minute {
		return ErrEmailCooldown
	}
	if _, err = tx.Exec(ctx, `DELETE FROM email_tokens WHERE user_id=$1 AND kind=$2`, userID, kind); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO email_tokens(token_hash,user_id,kind,expires_at) VALUES($1,$2,$3,$4)`, hash, userID, kind, expiry); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) VerifyEmailToken(ctx context.Context, hash, passwordHash, displayName string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID string
	err = tx.QueryRow(ctx, `UPDATE email_tokens SET consumed_at=now() WHERE token_hash=$1 AND kind='verify' AND consumed_at IS NULL AND expires_at>now() RETURNING user_id`, hash).Scan(&userID)
	if err != nil {
		return mapErr(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE users SET status='pending_approval',password_hash=$2,display_name=$3,updated_at=now() WHERE id=$1 AND status='pending_email'`, userID, passwordHash, displayName)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) ResetPasswordWithToken(ctx context.Context, hash, newHash string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID string
	err = tx.QueryRow(ctx, `UPDATE email_tokens SET consumed_at=now() WHERE token_hash=$1 AND kind='reset' AND consumed_at IS NULL AND expires_at>now() RETURNING user_id`, hash).Scan(&userID)
	if err != nil {
		return mapErr(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE users SET password_hash=$2,updated_at=now() WHERE id=$1 AND status IN ('active','pending_approval')`, userID, newHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SetUserStatus(ctx context.Context, id, status, actorID string) error {
	var from string
	switch status {
	case "active", "disabled", "rejected":
	default:
		return ErrConflict
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&from)
	if err != nil {
		return mapErr(err)
	}
	if status == "active" && from != "pending_approval" {
		return ErrConflict
	}
	if status == "rejected" && from != "pending_approval" {
		return ErrConflict
	}
	if status == "disabled" && from != "active" {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE users SET status=$2,updated_at=now() WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if status != "active" {
		if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
	}
	actorType := "user"
	var actor any = actorID
	if actorID == "" {
		actorType = "breakglass"
		actor = nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,actor_type,action,target_type,target_id,details) VALUES($1,$2,$3,'user',$4,$5)`, actor, actorType, "user."+status, id, json.RawMessage(`{"previousStatus":"`+from+`"}`))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SetPlatformAdmin(ctx context.Context, id string, enabled bool, actorID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE users SET platform_admin=$2,updated_at=now() WHERE id=$1 AND status='active'`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	actorType := "user"
	var actor any = actorID
	if actorID == "" {
		actorType = "breakglass"
		actor = nil
	}
	details, _ := json.Marshal(map[string]bool{"enabled": enabled})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,actor_type,action,target_type,target_id,details) VALUES($1,$2,'user.platform-admin','user',$3,$4)`, actor, actorType, id, details); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AddUserSession(ctx context.Context, hash, userID string) error {
	tag, err := s.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,expires_at,user_id,principal_type) SELECT $1,now()+interval '12 hours',id,'user' FROM users WHERE id=$2 AND status='active'`, hash, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AddBreakglassSession(ctx context.Context, hash string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,expires_at,principal_type) VALUES($1,now()+interval '12 hours','breakglass')`, hash)
	return err
}

func (s *Store) SessionPrincipal(ctx context.Context, hash string) (Principal, error) {
	var p Principal
	var typ string
	err := s.Pool.QueryRow(ctx, `SELECT x.principal_type,coalesce(u.id,''),coalesce(u.email,''),coalesce(u.display_name,''),coalesce(u.platform_admin,false) FROM sessions x LEFT JOIN users u ON u.id=x.user_id WHERE x.token_hash=$1 AND x.expires_at>now() AND (x.principal_type='breakglass' OR (x.principal_type='user' AND u.status='active'))`, hash).Scan(&typ, &p.UserID, &p.Email, &p.DisplayName, &p.PlatformAdmin)
	if err != nil {
		return Principal{}, mapErr(err)
	}
	p.Breakglass = typ == "breakglass"
	if p.Breakglass {
		p.PlatformAdmin = true
	}
	return p, nil
}

func (s *Store) RevokeUserSessions(ctx context.Context, userID string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}

func (s *Store) Audit(ctx context.Context, actor Principal, action, targetType, targetID string, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	var actorID any = actor.UserID
	if actor.UserID == "" {
		actorID = nil
	}
	actorType := "user"
	if actor.Breakglass {
		actorType = "breakglass"
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_events(actor_user_id,actor_type,action,target_type,target_id,details) VALUES($1,$2,$3,$4,$5,$6)`, actorID, actorType, action, targetType, targetID, b)
	return err
}

var ErrInvalidCredentials = errors.New("invalid credentials")

func (s *Store) UpsertExternalIdentity(ctx context.Context, userID, provider, instance, externalID, username, sealedSecret string) error {
	if provider == "gitlab" {
		instance = domain.GitLabIdentity(instance)
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO external_identities(user_id,provider,instance,external_id,username,secret) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id,provider,instance) DO UPDATE SET external_id=excluded.external_id,username=excluded.username,secret=excluded.secret,updated_at=now()`, userID, provider, instance, externalID, username, sealedSecret)
	return mapErr(err)
}

func (s *Store) ExternalIdentitySecret(ctx context.Context, userID, provider, instance string) (string, string, string, error) {
	if provider == "gitlab" {
		instance = domain.GitLabIdentity(instance)
	}
	var externalID, username, secret string
	err := s.Pool.QueryRow(ctx, `SELECT external_id,username,secret FROM external_identities WHERE user_id=$1 AND provider=$2 AND instance=$3`, userID, provider, instance).Scan(&externalID, &username, &secret)
	return externalID, username, secret, mapErr(err)
}

func (s *Store) DeleteExternalIdentity(ctx context.Context, userID, provider, instance string) error {
	if provider == "gitlab" {
		instance = domain.GitLabIdentity(instance)
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM external_identities WHERE user_id=$1 AND provider=$2 AND instance=$3`, userID, provider, instance)
	return err
}
