package store

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestListAndRevokeTokensExcludesAgentTokens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, userToken, err := s.CreateToken(ctx, "laptop", domain.TokenDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreateToken(ctx, "ops", domain.TokenAdmin); err != nil {
		t.Fatal(err)
	}
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "demo", Repository: "acme/demo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO agents(id,hive_id,role,state,cli) VALUES('agent1',$1,'forager','active','opencode')`, hive.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CreateAgentToken(ctx, "agent1"); err != nil {
		t.Fatal(err)
	}

	tokens, err := s.ListTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("listed %d tokens: %+v", len(tokens), tokens)
	}
	for _, token := range tokens {
		if token.Scope == domain.TokenAgent {
			t.Fatal("agent token listed")
		}
	}

	if err = s.RevokeToken(ctx, userToken.ID); err != nil {
		t.Fatal(err)
	}
	tokens, err = s.ListTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("revoked token still listed: %+v", tokens)
	}
	if err = s.RevokeToken(ctx, userToken.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v", err)
	}
	if err = s.RevokeToken(ctx, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown revoke = %v", err)
	}
}
