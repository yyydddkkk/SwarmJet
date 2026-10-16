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
