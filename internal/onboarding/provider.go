// Package onboarding implements the round-3 setup flow: creating a GitHub App
// from the console, storing its credentials encrypted, and keeping the App
// webhook pointed at the current tunnel address.
package onboarding

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// ErrNotConfigured is the user-facing error whenever a GitHub operation is
// attempted before the App exists. Foragers surface this text instead of
// failing the whole serve process.
var ErrNotConfigured = errors.New("GitHub App 未配置，请在接入页创建")

// Source says where the active App came from. Environment variables win over
// the database, matching the round-3 contract.
type Source string

const (
	SourceEnv      Source = "env"
	SourceDatabase Source = "database"
)

// EnvConfig is the manual round-2 configuration. It remains supported for
// deployments that cannot run the manifest flow.
type EnvConfig struct {
	AppID          string
	PrivateKeyFile string
	WebhookSecret  string
}

// EnvConfigFromEnvironment reads the optional GITHUB_APP_* variables.
func EnvConfigFromEnvironment() EnvConfig {
	return EnvConfig{
		AppID:          os.Getenv("GITHUB_APP_ID"),
		PrivateKeyFile: os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"),
		WebhookSecret:  os.Getenv("GITHUB_APP_WEBHOOK_SECRET"),
	}
}

// CredentialStore persists the encrypted App. *store.Store implements it.
type CredentialStore interface {
	GitHubAppCredential(ctx context.Context) (store.GitHubAppCredential, error)
}

// Credentials is a decrypted App identity. It stays in memory only.
type Credentials struct {
	AppID         int64
	PrivateKeyPEM []byte
	WebhookSecret string
	Slug          string
	HTMLURL       string
	Source        Source
}

// ProviderConfig wires the environment, database and encryption box.
type ProviderConfig struct {
	Env        EnvConfig
	Store      CredentialStore
	Box        *secretbox.Box
	BaseURL    string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Provider resolves the active GitHub App on every call. Environment variables
// take precedence; otherwise the encrypted database row is used. Because the
// database is consulted each time, an App created from the setup page works
// without restarting the process.
type Provider struct {
	env    EnvConfig
	store  CredentialStore
	box    *secretbox.Box
	base   string
	client *http.Client
	logger *slog.Logger

	mu     sync.Mutex
	cached *cachedApp

	// slugMu guards the optional GET /app lookup used by environment-configured
	// Apps, whose slug is not stored locally.
	slugMu    sync.Mutex
	slugValue string
	slugAppID int64
	slugAt    time.Time
}

type cachedApp struct {
	fingerprint string
	app         *githubapp.App
}

// NewProvider builds a provider. A nil Store simply disables the database
// source; a nil Box makes database credentials unreadable but does not affect
// environment credentials.
func NewProvider(cfg ProviderConfig) *Provider {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Provider{
		env:    cfg.Env,
		store:  cfg.Store,
		box:    cfg.Box,
		base:   cfg.BaseURL,
		client: cfg.HTTPClient,
		logger: logger,
	}
}

// Credentials returns the active App identity, preferring environment
// configuration. It never includes plaintext secrets in an error.
func (p *Provider) Credentials(ctx context.Context) (Credentials, error) {
	if strings.TrimSpace(p.env.AppID) != "" {
		return p.envCredentials()
	}
	if p.store == nil {
		return Credentials{}, ErrNotConfigured
	}
	record, err := p.store.GitHubAppCredential(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return Credentials{}, ErrNotConfigured
	}
	if err != nil {
		p.logger.Warn("failed to read GitHub App credentials", "error", err)
		return Credentials{}, errors.New("读取数据库中的 GitHub App 凭证失败")
	}
	if p.box == nil {
		return Credentials{}, errors.New("SWARMJET_MASTER_KEY 未配置，无法解密 GitHub App 凭证")
	}
	privateKey, err := p.box.Open(record.PrivateKeyCipher)
	if err != nil {
		return Credentials{}, errors.New("无法解密 GitHub App 私钥；请确认 SWARMJET_MASTER_KEY 未改变")
	}
	webhookSecret, err := p.box.Open(record.WebhookSecretCipher)
	if err != nil {
		return Credentials{}, errors.New("无法解密 GitHub App webhook secret；请确认 SWARMJET_MASTER_KEY 未改变")
	}
	return Credentials{
		AppID:         record.AppID,
		PrivateKeyPEM: []byte(privateKey),
		WebhookSecret: webhookSecret,
		Slug:          record.Slug,
		HTMLURL:       record.HTMLURL,
		Source:        SourceDatabase,
	}, nil
}

func (p *Provider) envCredentials() (Credentials, error) {
	rawID := strings.TrimSpace(p.env.AppID)
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return Credentials{}, errors.New("GITHUB_APP_ID must be a positive integer")
	}
	file := strings.TrimSpace(p.env.PrivateKeyFile)
	if file == "" {
		return Credentials{}, errors.New("GITHUB_APP_PRIVATE_KEY_FILE is required when GITHUB_APP_ID is set")
	}
	privateKey, err := os.ReadFile(file)
	if err != nil {
		return Credentials{}, fmt.Errorf("unable to read GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
	}
	return Credentials{
		AppID:         id,
		PrivateKeyPEM: privateKey,
		WebhookSecret: p.env.WebhookSecret,
		Source:        SourceEnv,
	}, nil
}

// App returns the active App client. The cache is keyed by the credential
// fingerprint, so replacing the database row (or rotating the key) takes effect
// immediately.
func (p *Provider) App(ctx context.Context) (*githubapp.App, error) {
	credentials, err := p.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(credentials.PrivateKeyPEM)
	fingerprint := fmt.Sprintf("%s|%d|%x", credentials.Source, credentials.AppID, sum[:8])
	p.mu.Lock()
	if p.cached != nil && p.cached.fingerprint == fingerprint {
		app := p.cached.app
		p.mu.Unlock()
		return app, nil
	}
	p.mu.Unlock()

	app, err := githubapp.NewWithHTTP(githubapp.Config{AppID: credentials.AppID, PrivateKeyPEM: credentials.PrivateKeyPEM}, p.base, p.client)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.cached = &cachedApp{fingerprint: fingerprint, app: app}
	p.mu.Unlock()
	return app, nil
}

// WebhookSecret returns the current webhook secret for signature verification.
func (p *Provider) WebhookSecret(ctx context.Context) (string, error) {
	credentials, err := p.Credentials(ctx)
	if err != nil {
		return "", err
	}
	if credentials.WebhookSecret == "" {
		return "", errors.New("GitHub App webhook secret 未配置")
	}
	return credentials.WebhookSecret, nil
}

// Slug returns the active App slug for Router platform-event detection. A
// database App stores it, so the common path is one metadata read and no
// decryption. An environment App is asked once via GET /app and cached, so each
// webhook does not make a network call. It returns an empty string when the App
// is unavailable; the Router then falls back to branch ownership.
func (p *Provider) Slug(ctx context.Context) string {
	if strings.TrimSpace(p.env.AppID) != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(p.env.AppID), 10, 64)
		if err != nil || id <= 0 {
			return ""
		}
		return p.cachedInfoSlug(ctx, id)
	}
	if p.store == nil {
		return ""
	}
	record, err := p.store.GitHubAppCredential(ctx)
	if err != nil {
		return ""
	}
	if record.Slug != "" {
		return record.Slug
	}
	return p.cachedInfoSlug(ctx, record.AppID)
}

// cachedInfoSlug reads GET /app at most once per App per ten minutes.
func (p *Provider) cachedInfoSlug(ctx context.Context, appID int64) string {
	p.slugMu.Lock()
	if p.slugAppID == appID && time.Since(p.slugAt) < 10*time.Minute {
		slug := p.slugValue
		p.slugMu.Unlock()
		return slug
	}
	p.slugMu.Unlock()

	app, err := p.App(ctx)
	if err != nil {
		return ""
	}
	infoCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	info, err := app.Info(infoCtx)
	cancel()
	if err != nil {
		return ""
	}
	p.slugMu.Lock()
	p.slugValue = info.Slug
	p.slugAppID = appID
	p.slugAt = time.Now()
	slug := p.slugValue
	p.slugMu.Unlock()
	return slug
}

// Invalidate drops the cached App after the setup callback replaces it.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	p.cached = nil
	p.mu.Unlock()
}

// DockerAvailable reports whether the Docker socket needed by Cells is visible
// in this process. The base compose file deliberately does not mount it.
func DockerAvailable() bool {
	path := "/var/run/docker.sock"
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		switch {
		case strings.HasPrefix(host, "unix://"):
			path = strings.TrimPrefix(host, "unix://")
		case strings.HasPrefix(host, "unix:"):
			path = strings.TrimPrefix(host, "unix:")
		default:
			return true
		}
	}
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
