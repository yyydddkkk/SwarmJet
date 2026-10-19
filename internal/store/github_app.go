package store

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed schema_upgrade_7.sql
var schemaUpgrade7 string

// applySchemaUpgrade7 installs the GitHub App onboarding tables. It is kept in
// this file so the round-3 change to Migrate is a single call.
func applySchemaUpgrade7(ctx context.Context, tx pgx.Tx, version int) error {
	if version >= 7 {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade7); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(7) ON CONFLICT DO NOTHING`)
	return err
}

// ErrManifestStateInvalid is returned when a GitHub App manifest state is
// unknown, expired or already consumed. The reason is deliberately opaque.
var ErrManifestStateInvalid = errors.New("GitHub App manifest state is invalid, expired or already used")

// GitHubAppCredential is the persisted, encrypted GitHub App identity. The
// plaintext private key and webhook secret live only in memory.
type GitHubAppCredential struct {
	AppID               int64
	Slug                string
	HTMLURL             string
	PrivateKeyCipher    string
	WebhookSecretCipher string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// SaveGitHubAppCredential replaces the current App. Recreating the App must not
// leave two active rows, so the singleton row is upserted.
func (s *Store) SaveGitHubAppCredential(ctx context.Context, credential GitHubAppCredential) error {
	if credential.AppID <= 0 || credential.PrivateKeyCipher == "" || credential.WebhookSecretCipher == "" {
		return errors.New("store: invalid GitHub App credential")
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO github_apps(singleton,app_id,slug,html_url,private_key_cipher,webhook_secret_cipher,created_at,updated_at)
		VALUES(true,$1,$2,$3,$4,$5,now(),now())
		ON CONFLICT(singleton) DO UPDATE SET
			app_id=EXCLUDED.app_id,
			slug=EXCLUDED.slug,
			html_url=EXCLUDED.html_url,
			private_key_cipher=EXCLUDED.private_key_cipher,
			webhook_secret_cipher=EXCLUDED.webhook_secret_cipher,
			updated_at=now()`,
		credential.AppID, credential.Slug, credential.HTMLURL, credential.PrivateKeyCipher, credential.WebhookSecretCipher)
	return err
}

// GitHubAppCredential reads the current encrypted App. It returns ErrNotFound
// when the App has not been created yet.
func (s *Store) GitHubAppCredential(ctx context.Context) (GitHubAppCredential, error) {
	var out GitHubAppCredential
	err := s.Pool.QueryRow(ctx, `
		SELECT app_id,slug,html_url,private_key_cipher,webhook_secret_cipher,created_at,updated_at
		FROM github_apps WHERE singleton=true`).
		Scan(&out.AppID, &out.Slug, &out.HTMLURL, &out.PrivateKeyCipher, &out.WebhookSecretCipher, &out.CreatedAt, &out.UpdatedAt)
	return out, mapErr(err)
}

// CreateManifestState stores only hashes for a one-time manifest flow. Expired
// rows older than a day are pruned opportunistically.
func (s *Store) CreateManifestState(ctx context.Context, stateHash, callbackHash string, expires time.Time) error {
	if stateHash == "" || callbackHash == "" {
		return errors.New("store: manifest state hashes are required")
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM github_app_manifest_states WHERE expires_at < now() - interval '1 day'`); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO github_app_manifest_states(state_hash,callback_hash,expires_at,created_at)
		VALUES($1,$2,$3,now())`, stateHash, callbackHash, expires.UTC())
	return mapErr(err)
}

// ConsumeManifestState atomically burns a state. The UPDATE is the single
// decision point, so two concurrent callbacks cannot both succeed.
func (s *Store) ConsumeManifestState(ctx context.Context, stateHash, callbackHash string, now time.Time) error {
	if stateHash == "" || callbackHash == "" {
		return ErrManifestStateInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE github_app_manifest_states SET used_at=$3
		WHERE state_hash=$1 AND callback_hash=$2 AND used_at IS NULL AND expires_at > $3`,
		stateHash, callbackHash, now.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrManifestStateInvalid
	}
	return nil
}

// ConsumeManifestStateAdmin burns a state when the caller proved the
// administrator session, for example when the browser did not carry the
// SameSite=Lax callback binding. The state itself remains the CSRF token.
func (s *Store) ConsumeManifestStateAdmin(ctx context.Context, stateHash string, now time.Time) error {
	if stateHash == "" {
		return ErrManifestStateInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE github_app_manifest_states SET used_at=$2
		WHERE state_hash=$1 AND used_at IS NULL AND expires_at > $2`, stateHash, now.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrManifestStateInvalid
	}
	return nil
}
