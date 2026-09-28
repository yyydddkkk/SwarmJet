package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAccountLifecycleAndSessionRevocation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	u := User{ID: "11111111-1111-4111-8111-111111111111", Email: "member@example.com", DisplayName: "Member", PasswordHash: "hash"}
	if err := s.RegisterUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueEmailToken(ctx, u.ID, "verify", "verify-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueEmailToken(ctx, u.ID, "verify", "too-soon", time.Now().Add(time.Hour)); !errors.Is(err, ErrEmailCooldown) {
		t.Fatalf("verification resend cooldown: %v", err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE email_tokens SET created_at=now()-interval '2 minutes' WHERE token_hash='verify-hash'`); err != nil {
		t.Fatal(err)
	}
	if err := s.IssueEmailToken(ctx, u.ID, "verify", "verify-hash-2", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmailToken(ctx, "verify-hash", "verified-hash", "Member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old verification token accepted: %v", err)
	}
	if err := s.VerifyEmailToken(ctx, "verify-hash-2", "verified-hash", "Member"); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyEmailToken(ctx, "verify-hash-2", "verified-hash", "Member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reused verification token: %v", err)
	}
	verified, err := s.UserByEmail(ctx, u.Email)
	if err != nil || verified.PasswordHash != "verified-hash" || verified.Status != "pending_approval" {
		t.Fatalf("verification did not set member credentials: %+v %v", verified, err)
	}
	if err := s.SetUserStatus(ctx, u.ID, "active", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUserSession(ctx, "session-hash", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlatformAdmin(ctx, u.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	p, err := s.SessionPrincipal(ctx, "session-hash")
	if err != nil || p.UserID != u.ID || !p.PlatformAdmin {
		t.Fatalf("principal: %+v %v", p, err)
	}
	if err := s.SetPlatformAdmin(ctx, u.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	p, err = s.SessionPrincipal(ctx, "session-hash")
	if err != nil || p.PlatformAdmin {
		t.Fatalf("platform role revocation: %+v %v", p, err)
	}
	if err := s.IssueEmailToken(ctx, u.ID, "reset", "reset-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPasswordWithToken(ctx, "reset-hash", "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionPrincipal(ctx, "session-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("password reset did not revoke session: %v", err)
	}
	updated, err := s.UserByEmail(ctx, u.Email)
	if err != nil || updated.PasswordHash != "new-hash" {
		t.Fatalf("password update: %+v %v", updated, err)
	}
	if err := s.AddUserSession(ctx, "session-2", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserStatus(ctx, u.ID, "disabled", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionPrincipal(ctx, "session-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled account retained session: %v", err)
	}
	if err := s.AddUserSession(ctx, "session-3", u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled account admitted: %v", err)
	}
}

func TestBreakglassSessionAndLegacyRevocation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.AddSession(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddBreakglassSession(ctx, "breakglass"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionPrincipal(ctx, "legacy"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy session retained: %v", err)
	}
	p, err := s.SessionPrincipal(ctx, "breakglass")
	if err != nil || !p.Breakglass || !p.PlatformAdmin {
		t.Fatalf("breakglass principal: %+v %v", p, err)
	}
}
