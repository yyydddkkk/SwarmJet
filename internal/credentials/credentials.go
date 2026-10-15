// Package credentials resolves a project's GitLab token from either a saved,
// encrypted connection or a deployment environment variable. Only references
// are persisted in Project and Task snapshots.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
)

// Secrets reads connection metadata and the sealed payload. Connection metadata
// is needed to bind a saved token to the GitLab address it was verified against.
type Secrets interface {
	ConnectionSecrets(ctx context.Context, id string) (string, error)
	Connection(ctx context.Context, id string) (domain.Connection, error)
}

// Payload is what actually gets encrypted: the primary token and an optional
// separate merge token.
type Payload struct {
	Token      string `json:"token"`
	MergeToken string `json:"mergeToken,omitempty"`
}

// Resolver turns a credential reference into a token. A missing master key
// disables connection storage while environment credentials keep working.
type Resolver struct {
	Store Secrets
	Box   *secretbox.Box
	Env   func(string) string
}

func (r *Resolver) Enabled() bool { return r != nil && r.Box != nil && r.Store != nil }

// Seal encrypts a credential payload.
func (r *Resolver) Seal(p Payload) (string, error) {
	if !r.Enabled() {
		return "", errors.New("connection storage is not enabled")
	}
	if strings.TrimSpace(p.Token) == "" {
		return "", errors.New("token is required")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return r.Box.Seal(string(body))
}

// UseMerge reports whether ref selects the connection's optional merge token.
func UseMerge(ref string) (string, bool) {
	if strings.HasPrefix(ref, domain.CredentialMergePrefix) {
		return strings.TrimPrefix(ref, domain.CredentialMergePrefix), true
	}
	if strings.HasPrefix(ref, domain.CredentialConnectionPrefix) {
		return strings.TrimPrefix(ref, domain.CredentialConnectionPrefix), false
	}
	return "", false
}

// Resolve returns the token for a reference without an address binding. It is
// only safe for deployment environment variables; saved connections must use
// ResolveFor so the token cannot be sent to a different GitLab address.
func (r *Resolver) Resolve(ref string) (string, error) {
	return r.resolve(ref, "")
}

// ResolveFor returns the token for a reference and rejects a saved connection
// whose verified GitLab address does not match the expected project address.
func (r *Resolver) ResolveFor(ref, expectedGitLabURL string) (string, error) {
	if _, merge := UseMerge(ref); merge || strings.HasPrefix(strings.TrimSpace(ref), domain.CredentialConnectionPrefix) {
		if strings.TrimSpace(expectedGitLabURL) == "" {
			return "", errors.New("saved connection requires the project GitLab address")
		}
	}
	return r.resolve(ref, expectedGitLabURL)
}

func (r *Resolver) resolve(ref, expectedGitLabURL string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("credential reference is empty")
	}
	if id, merge := UseMerge(ref); id != "" {
		if !r.Enabled() {
			return "", errors.New("connection storage is not enabled; configure SWARMJET_MASTER_KEY and recreate the credential")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if expectedGitLabURL != "" {
			meta, err := r.Store.Connection(ctx, id)
			if err != nil {
				return "", errors.New("saved connection is unavailable")
			}
			if domain.GitLabIdentity(meta.GitLabURL) != domain.GitLabIdentity(expectedGitLabURL) {
				return "", errors.New("saved connection does not match the project GitLab address")
			}
		}
		sealed, err := r.Store.ConnectionSecrets(ctx, id)
		if err != nil {
			return "", errors.New("saved connection credential is unavailable")
		}
		plain, err := r.Box.Open(sealed)
		if err != nil {
			return "", err
		}
		var payload Payload
		if json.Unmarshal([]byte(plain), &payload) != nil || payload.Token == "" {
			return "", errors.New("saved connection credential is invalid")
		}
		if merge && payload.MergeToken != "" {
			return payload.MergeToken, nil
		}
		// Single-token mode: the same token performs merges. There is no
		// independent permission isolation in this mode.
		return payload.Token, nil
	}
	env := r.Env
	if env == nil {
		env = os.Getenv
	}
	if value := env(ref); value != "" {
		return value, nil
	}
	return "", errors.New("credential unavailable: " + ref)
}
