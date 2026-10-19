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
	var agentID *string
	err := s.Pool.QueryRow(ctx, `SELECT id,name,scope,agent_id,created_at FROM mcp_tokens WHERE token_hash=$1 AND revoked_at IS NULL`, hashToken(plaintext)).
		Scan(&t.ID, &t.Name, &scope, &agentID, &t.CreatedAt)
	if err != nil {
		return domain.Token{}, domainErr(err)
	}
	t.Scope = domain.TokenScope(scope)
	if agentID != nil {
		t.AgentID = *agentID
	}
	return t, nil
}

// CreateAgentToken mints one full-privilege token for an Agent. Its address is
// fixed by AgentID; the plaintext is returned once and never stored.
func (s *Store) CreateAgentToken(ctx context.Context, agentID string) (plaintext string, t domain.Token, err error) {
	var exists bool
	if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1)`, agentID).Scan(&exists); err != nil {
		return "", domain.Token{}, err
	}
	if !exists {
		return "", domain.Token{}, domain.ErrNotFound
	}
	plaintext, err = newSecret()
	if err != nil {
		return "", domain.Token{}, err
	}
	id, err := newOpaqueID()
	if err != nil {
		return "", domain.Token{}, err
	}
	now := time.Now().UTC()
	name := "agent:" + agentID
	t = domain.Token{ID: id, Name: name, Scope: domain.TokenAgent, AgentID: agentID, CreatedAt: now}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO mcp_tokens(id,name,scope,agent_id,token_hash,created_at) VALUES($1,$2,$3,$4,$5,$6)`,
		id, name, string(domain.TokenAgent), agentID, hashToken(plaintext), now); err != nil {
		return "", domain.Token{}, domainErr(err)
	}
	return plaintext, t, nil
}

// RevokeAgentTokens revokes every live token belonging to an Agent. It is
// idempotent and does not fail when the Agent has no tokens.
func (s *Store) RevokeAgentTokens(ctx context.Context, agentID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE mcp_tokens SET revoked_at=now() WHERE agent_id=$1 AND revoked_at IS NULL`, agentID)
	return err
}
