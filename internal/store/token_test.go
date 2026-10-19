package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestTokenStoresHashOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	plaintext, token, err := s.CreateToken(ctx, "local agent", domain.TokenDefault)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext == "" || token.ID == "" || token.Scope != domain.TokenDefault {
		t.Fatalf("unexpected token: %+v", token)
	}
	var stored string
	if err = s.Pool.QueryRow(ctx, `SELECT token_hash FROM mcp_tokens WHERE id=$1`, token.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(plaintext))
	if stored != hex.EncodeToString(sum[:]) {
		t.Fatalf("stored hash is not sha256(plaintext): %q", stored)
	}
	var matches int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_tokens WHERE token_hash=$1`, plaintext).Scan(&matches); err != nil || matches != 0 {
		t.Fatalf("plaintext recoverable from storage: %d %v", matches, err)
	}
	got, err := s.TokenByPlaintext(ctx, plaintext)
	if err != nil || got.ID != token.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = s.TokenByPlaintext(ctx, "wrong"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("wrong plaintext accepted: %v", err)
	}
	other, _, err := s.CreateToken(ctx, "admin", domain.TokenAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if other == plaintext {
		t.Fatal("two tokens share a plaintext")
	}
	fetched, err := s.TokenByPlaintext(ctx, other)
	if err != nil || fetched.Scope != domain.TokenAdmin {
		t.Fatalf("%+v %v", fetched, err)
	}
	_, defaulted, err := s.CreateToken(ctx, "defaulted", "")
	if err != nil || defaulted.Scope != domain.TokenDefault {
		t.Fatalf("%+v %v", defaulted, err)
	}
}

func TestAgentTokenLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/token")
	a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, token, err := s.CreateAgentToken(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext == "" || token.ID == "" || token.Scope != domain.TokenAgent || token.AgentID != a.ID {
		t.Fatalf("unexpected agent token: %+v", token)
	}
	var storedAgent, storedScope string
	var revokedAt *string
	if err = s.Pool.QueryRow(ctx, `SELECT agent_id,scope,revoked_at::text FROM mcp_tokens WHERE id=$1`, token.ID).
		Scan(&storedAgent, &storedScope, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if storedAgent != a.ID || storedScope != string(domain.TokenAgent) || revokedAt != nil {
		t.Fatalf("stored token agent=%q scope=%q revoked=%v", storedAgent, storedScope, revokedAt)
	}
	var plaintextStored int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM mcp_tokens WHERE token_hash=$1`, plaintext).Scan(&plaintextStored); err != nil || plaintextStored != 0 {
		t.Fatalf("plaintext recoverable: %d %v", plaintextStored, err)
	}
	got, err := s.TokenByPlaintext(ctx, plaintext)
	if err != nil || got.AgentID != a.ID || got.Scope != domain.TokenAgent {
		t.Fatalf("%+v %v", got, err)
	}
	// Unrelated tokens are not affected by revocation.
	otherPlaintext, _, err := s.CreateToken(ctx, "local", domain.TokenDefault)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeAgentTokens(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TokenByPlaintext(ctx, plaintext); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoked token still resolves: %v", err)
	}
	if _, err = s.TokenByPlaintext(ctx, otherPlaintext); err != nil {
		t.Fatalf("revocation hit another token: %v", err)
	}
	if err = s.RevokeAgentTokens(ctx, a.ID); err != nil {
		t.Fatalf("repeat revocation: %v", err)
	}
	// A new token after revocation works and starts its own lifecycle.
	newPlaintext, newToken, err := s.CreateAgentToken(ctx, a.ID)
	if err != nil || newToken.AgentID != a.ID {
		t.Fatalf("%+v %v", newToken, err)
	}
	if _, err = s.TokenByPlaintext(ctx, newPlaintext); err != nil {
		t.Fatalf("new agent token: %v", err)
	}
	if _, _, err = s.CreateAgentToken(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("token for missing agent: %v", err)
	}
}
