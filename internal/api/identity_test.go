package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

func TestAccountPasswordHash(t *testing.T) {
	hash, err := hashAccountPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyAccountPassword(hash, "correct horse battery staple") {
		t.Fatal("valid password rejected")
	}
	if verifyAccountPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
	for _, invalid := range []string{"", "$argon2id$v=19$m=4294967295,t=3,p=2$a$b", "$argon2id$v=19$m=65536,t=3,p=2$bad$bad"} {
		if verifyAccountPassword(invalid, "password") {
			t.Fatalf("malformed hash accepted: %q", invalid)
		}
	}
}

func TestRegistrationCode(t *testing.T) {
	for range 100 {
		code, err := registrationCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 6 || code[0] < '0' || code[0] > '9' {
			t.Fatalf("invalid registration code: %q", code)
		}
		for _, digit := range code {
			if digit < '0' || digit > '9' {
				t.Fatalf("invalid registration code: %q", code)
			}
		}
	}
	base := registrationCodeHash("secret", "person@example.test", "123456")
	if base == registrationCodeHash("secret", "other@example.test", "123456") ||
		base == registrationCodeHash("secret", "person@example.test", "123457") ||
		base == registrationCodeHash("other", "person@example.test", "123456") {
		t.Fatal("registration code hash is not bound to secret, email, and code")
	}
}

func TestRegisterVerifyWithCode(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "api_code_test_" + strings.ReplaceAll(id, "-", "")
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	config := admin.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &store.Store{Pool: pool}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const email = "member@example.test"
	if err = s.RegisterUser(ctx, store.User{ID: id, Email: email}); err != nil {
		t.Fatal(err)
	}
	if err = s.IssueEmailToken(ctx, id, "verify", registrationCodeHash("admin-secret", email, "123456"), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	h := New(s, Config{Password: "admin-secret", AllowedOrigin: "https://console.test"})
	verify := func(code string) int {
		r := httptest.NewRequest("POST", "https://console.test/api/register/verify", strings.NewReader(`{"email":"member@example.test","code":"`+code+`","displayName":"Member","password":"a suitably long password"}`))
		r.Header.Set("Origin", "https://console.test")
		r.Header.Set("X-Requested-With", "swarmjet")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := verify("000000"); got != 400 {
		t.Fatalf("incorrect code status: %d", got)
	}
	if got := verify("123456"); got != 204 {
		t.Fatalf("valid code status: %d", got)
	}
	if got := verify("123456"); got != 400 {
		t.Fatalf("reused code status: %d", got)
	}
	u, err := s.UserByEmail(ctx, email)
	if err != nil || u.Status != "pending_approval" || u.DisplayName != "Member" || !verifyAccountPassword(u.PasswordHash, "a suitably long password") {
		t.Fatalf("verified account: %+v, %v", u, err)
	}
}
