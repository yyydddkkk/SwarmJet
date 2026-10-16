package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// newSecret returns 256 random bits as hex. Used for MCP token plaintexts.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// CreateToken stores only the token hash and returns the plaintext exactly
// once. An empty scope defaults to the unprivileged TokenDefault.
func (s *Store) CreateToken(ctx context.Context, name string, scope domain.TokenScope) (string, domain.Token, error) {
	if scope == "" {
		scope = domain.TokenDefault
	}
	plaintext, err := newSecret()
	if err != nil {
		return "", domain.Token{}, err
	}
	id, err := newOpaqueID()
	if err != nil {
		return "", domain.Token{}, err
	}
	now := time.Now().UTC()
	t := domain.Token{ID: id, Name: name, Scope: scope, CreatedAt: now}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO mcp_tokens(id,name,scope,token_hash,created_at) VALUES($1,$2,$3,$4,$5)`,
		id, name, string(scope), hashToken(plaintext), now); err != nil {
		return "", domain.Token{}, domainErr(err)
	}
	return plaintext, t, nil
}

func (s *Store) TokenByPlaintext(ctx context.Context, plaintext string) (domain.Token, error) {
	var t domain.Token
	var scope string
	err := s.Pool.QueryRow(ctx, `SELECT id,name,scope,created_at FROM mcp_tokens WHERE token_hash=$1`, hashToken(plaintext)).
		Scan(&t.ID, &t.Name, &scope, &t.CreatedAt)
	if err != nil {
		return domain.Token{}, domainErr(err)
	}
	t.Scope = domain.TokenScope(scope)
	return t, nil
}
