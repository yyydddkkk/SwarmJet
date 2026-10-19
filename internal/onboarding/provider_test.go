package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type fakeCredentialStore struct {
	mu     sync.Mutex
	record store.GitHubAppCredential
	err    error
}

func (f *fakeCredentialStore) GitHubAppCredential(context.Context) (store.GitHubAppCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.GitHubAppCredential{}, f.err
	}
	if f.record.AppID == 0 {
		return store.GitHubAppCredential{}, store.ErrNotFound
	}
	return f.record, nil
}

func (f *fakeCredentialStore) set(record store.GitHubAppCredential) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record = record
}

func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func testPrivateKey(t *testing.T) ([]byte, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), "app.pem")
	if err = os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	return encoded, path
}

func encryptedCredential(t *testing.T, box *secretbox.Box, appID int64, privateKey, webhookSecret, slug string) store.GitHubAppCredential {
	t.Helper()
	keyCipher, err := box.Seal(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	secretCipher, err := box.Seal(webhookSecret)
	if err != nil {
		t.Fatal(err)
	}
	return store.GitHubAppCredential{AppID: appID, Slug: slug, PrivateKeyCipher: keyCipher, WebhookSecretCipher: secretCipher}
}

func TestProviderEnvironmentWins(t *testing.T) {
	box := testBox(t)
	privateKey, path := testPrivateKey(t)
	credentialStore := &fakeCredentialStore{}
	credentialStore.set(encryptedCredential(t, box, 999, string(privateKey), "database-secret", "database-app"))
	provider := NewProvider(ProviderConfig{
		Env:   EnvConfig{AppID: "123", PrivateKeyFile: path, WebhookSecret: "env-secret"},
		Store: credentialStore,
		Box:   box,
	})
	credentials, err := provider.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AppID != 123 || credentials.Source != SourceEnv || credentials.WebhookSecret != "env-secret" {
		t.Fatalf("env credentials = %+v", credentials)
	}
	app, err := provider.App(context.Background())
	if err != nil || app.ID() != 123 {
		t.Fatalf("env app = %v, %v", app, err)
	}
}

func TestProviderDatabaseFallbackAndDynamicReplacement(t *testing.T) {
	box := testBox(t)
	firstKey, _ := testPrivateKey(t)
	secondKey, _ := testPrivateKey(t)
	credentialStore := &fakeCredentialStore{}
	credentialStore.set(encryptedCredential(t, box, 456, string(firstKey), "first-secret", "first-app"))
	provider := NewProvider(ProviderConfig{Store: credentialStore, Box: box})

	credentials, err := provider.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AppID != 456 || credentials.Source != SourceDatabase || credentials.Slug != "first-app" || credentials.WebhookSecret != "first-secret" {
		t.Fatalf("database credentials = %+v", credentials)
	}
	app, err := provider.App(context.Background())
	if err != nil || app.ID() != 456 {
		t.Fatalf("first app = %v, %v", app, err)
	}

	// Replacing the row (the setup callback does this) must be visible without
	// restarting or invalidating manually.
	credentialStore.set(encryptedCredential(t, box, 789, string(secondKey), "second-secret", "second-app"))
	credentials, err = provider.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AppID != 789 || credentials.Slug != "second-app" || credentials.WebhookSecret != "second-secret" {
		t.Fatalf("replaced credentials = %+v", credentials)
	}
	if slug := provider.Slug(context.Background()); slug != "second-app" {
		t.Fatalf("replaced slug = %q", slug)
	}
	app, err = provider.App(context.Background())
	if err != nil || app.ID() != 789 {
		t.Fatalf("replaced app = %v, %v", app, err)
	}
	secret, err := provider.WebhookSecret(context.Background())
	if err != nil || secret != "second-secret" {
		t.Fatalf("replaced webhook secret = %q, %v", secret, err)
	}
}

func TestProviderNotConfigured(t *testing.T) {
	provider := NewProvider(ProviderConfig{Store: &fakeCredentialStore{}})
	if _, err := provider.Credentials(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing App error = %v", err)
	}
	if _, err := provider.WebhookSecret(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing webhook secret error = %v", err)
	}
}

func TestProviderRequiresMasterKeyForDatabaseCredentials(t *testing.T) {
	box := testBox(t)
	privateKey, _ := testPrivateKey(t)
	credentialStore := &fakeCredentialStore{}
	credentialStore.set(encryptedCredential(t, box, 1, string(privateKey), "secret", "app"))
	provider := NewProvider(ProviderConfig{Store: credentialStore})
	_, err := provider.Credentials(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SWARMJET_MASTER_KEY") {
		t.Fatalf("missing master key error = %v", err)
	}
}

func TestProviderRejectsInvalidEnvironmentAppID(t *testing.T) {
	provider := NewProvider(ProviderConfig{Env: EnvConfig{AppID: "not-a-number"}})
	if _, err := provider.Credentials(context.Background()); err == nil {
		t.Fatal("invalid GITHUB_APP_ID was accepted")
	}
}

func TestProviderDecryptErrorsDoNotLeakCiphertext(t *testing.T) {
	box := testBox(t)
	credentialStore := &fakeCredentialStore{}
	credentialStore.set(store.GitHubAppCredential{AppID: 5, PrivateKeyCipher: "not-ciphertext", WebhookSecretCipher: "also-not"})
	provider := NewProvider(ProviderConfig{Store: credentialStore, Box: box})
	_, err := provider.Credentials(context.Background())
	if err == nil {
		t.Fatal("corrupt ciphertext was accepted")
	}
	if strings.Contains(err.Error(), "not-ciphertext") || strings.Contains(err.Error(), "also-not") {
		t.Fatalf("error leaked ciphertext: %v", err)
	}
}

func TestProviderSlugFromEnvironmentInfoIsCached(t *testing.T) {
	_, path := testPrivateKey(t)
	var mu sync.Mutex
	infoCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		infoCalls++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":314,"slug":"env-app","html_url":"https://github.com/apps/env-app"}`))
	}))
	defer server.Close()
	provider := NewProvider(ProviderConfig{
		Env:        EnvConfig{AppID: "314", PrivateKeyFile: path, WebhookSecret: "env-secret"},
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if slug := provider.Slug(context.Background()); slug != "env-app" {
		t.Fatalf("slug = %q", slug)
	}
	if slug := provider.Slug(context.Background()); slug != "env-app" {
		t.Fatalf("cached slug = %q", slug)
	}
	mu.Lock()
	defer mu.Unlock()
	if infoCalls != 1 {
		t.Fatalf("GET /app calls = %d, want 1", infoCalls)
	}
}
