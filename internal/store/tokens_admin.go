package store

import (
	"context"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// ListTokens returns live, user-facing MCP tokens. Agent tokens are minted by
// the platform for a Cell and are never listed in the console.
func (s *Store) ListTokens(ctx context.Context) ([]domain.Token, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,scope,created_at FROM mcp_tokens
		WHERE revoked_at IS NULL AND scope <> $1 ORDER BY created_at DESC,id`, string(domain.TokenAgent))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []domain.Token{}
	for rows.Next() {
		var token domain.Token
		var scope string
		if err = rows.Scan(&token.ID, &token.Name, &scope, &token.CreatedAt); err != nil {
			return nil, err
		}
		token.Scope = domain.TokenScope(scope)
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// RevokeToken revokes one token by ID. Revoking an already revoked or unknown
// token reports not found so the caller can distinguish a stale console row.
func (s *Store) RevokeToken(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE mcp_tokens SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
