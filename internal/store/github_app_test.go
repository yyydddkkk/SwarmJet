package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGitHubAppCredentialRoundTripAndReplacement(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	first := GitHubAppCredential{AppID: 100, Slug: "first", HTMLURL: "https://github.com/apps/first", PrivateKeyCipher: "key-cipher-1", WebhookSecretCipher: "secret-cipher-1"}
	if err := s.SaveGitHubAppCredential(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err := s.GitHubAppCredential(ctx)
	if err != nil || got.AppID != 100 || got.Slug != "first" || got.PrivateKeyCipher != "key-cipher-1" || got.WebhookSecretCipher != "secret-cipher-1" {
		t.Fatalf("first read = %+v, %v", got, err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM github_apps`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("row count = %d, %v", count, err)
	}
	second := GitHubAppCredential{AppID: 200, Slug: "second", PrivateKeyCipher: "key-cipher-2", WebhookSecretCipher: "secret-cipher-2"}
	if err = s.SaveGitHubAppCredential(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, err = s.GitHubAppCredential(ctx)
	if err != nil || got.AppID != 200 || got.Slug != "second" || got.PrivateKeyCipher != "key-cipher-2" {
		t.Fatalf("replacement read = %+v, %v", got, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM github_apps`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replacement row count = %d, %v", count, err)
	}
	if err = s.SaveGitHubAppCredential(ctx, GitHubAppCredential{AppID: 0}); err == nil {
		t.Fatal("invalid credential was saved")
	}
}

func TestManifestStateIsOneTimeExpiringAndBound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateManifestState(ctx, "state-hash", "callback-hash", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeManifestState(ctx, "state-hash", "callback-hash", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeManifestState(ctx, "state-hash", "callback-hash", now); !errors.Is(err, ErrManifestStateInvalid) {
		t.Fatalf("second consume error = %v", err)
	}
	if err := s.CreateManifestState(ctx, "bound", "callback-hash", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeManifestState(ctx, "bound", "other-callback", now); !errors.Is(err, ErrManifestStateInvalid) {
		t.Fatalf("wrong binding error = %v", err)
	}
	if err := s.CreateManifestState(ctx, "expired", "callback-hash", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeManifestState(ctx, "expired", "callback-hash", now); !errors.Is(err, ErrManifestStateInvalid) {
		t.Fatalf("expired error = %v", err)
	}
	if err := s.CreateManifestState(ctx, "admin", "callback-hash", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeManifestStateAdmin(ctx, "admin", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeManifestStateAdmin(ctx, "admin", now); !errors.Is(err, ErrManifestStateInvalid) {
		t.Fatalf("second admin consume error = %v", err)
	}
	if err := s.CreateManifestState(ctx, "", "callback-hash", now.Add(time.Minute)); err == nil {
		t.Fatal("empty state hash was accepted")
	}
}

func TestManifestStateConcurrentConsumeOnlyOneWins(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateManifestState(ctx, "race", "callback", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ConsumeManifestState(ctx, "race", "callback", now); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful consumes = %d, want 1", successes)
	}
}

func TestMigrateCreatesGitHubAppTables(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, table := range []string{"github_apps", "github_app_manifest_states"} {
		var exists bool
		if err := s.Pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s exists=%v err=%v", table, exists, err)
		}
	}
	var version int
	if err := s.Pool.QueryRow(ctx, `SELECT max(version) FROM schema_version`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}
