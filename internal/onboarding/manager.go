package onboarding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

const (
	// ManifestStateTTL is how long the GitHub App creation page may sit open.
	ManifestStateTTL = 10 * time.Minute
	// CallbackCookieName binds a manifest flow to the browser that started it.
	CallbackCookieName = "swarmjet_github_app"
	// ManifestCallbackPath is both the redirect_url and the callback cookie path.
	ManifestCallbackPath = "/api/setup/github/callback"
)

var (
	ErrInvalidOrigin = errors.New("PUBLIC_ORIGIN must be an absolute http(s) URL")
	ErrInvalidOwner  = errors.New("GitHub organization must be a valid login")
	ErrMasterKey     = errors.New("SWARMJET_MASTER_KEY 未配置，无法加密保存 GitHub App 凭证")
)

// ManagerStore is the persistence the manifest flow needs.
type ManagerStore interface {
	CreateManifestState(ctx context.Context, stateHash, callbackHash string, expires time.Time) error
	ConsumeManifestState(ctx context.Context, stateHash, callbackHash string, now time.Time) error
	ConsumeManifestStateAdmin(ctx context.Context, stateHash string, now time.Time) error
	SaveGitHubAppCredential(ctx context.Context, credential store.GitHubAppCredential) error
}

// ManagerConfig assembles the onboarding dependencies.
type ManagerConfig struct {
	Provider     *Provider
	Store        ManagerStore
	Box          *secretbox.Box
	PublicOrigin string
	Tunnel       *Tunnel
	HTTPClient   *http.Client
	BaseURL      string
	Now          func() time.Time
	Logger       *slog.Logger
}

// Manager owns the GitHub App manifest flow and the setup status projection.
type Manager struct {
	provider     *Provider
	store        ManagerStore
	box          *secretbox.Box
	publicOrigin string
	tunnel       *Tunnel
	httpClient   *http.Client
	baseURL      string
	now          func() time.Time
	logger       *slog.Logger
}

// NewManager validates PUBLIC_ORIGIN up front. A missing or malformed origin is
// a deployment error, not a request error.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	origin, err := normalizeOrigin(cfg.PublicOrigin)
	if err != nil {
		return nil, err
	}
	if cfg.Provider == nil {
		return nil, errors.New("GitHub App provider is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("onboarding store is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Manager{
		provider:     cfg.Provider,
		store:        cfg.Store,
		box:          cfg.Box,
		publicOrigin: origin,
		tunnel:       cfg.Tunnel,
		httpClient:   cfg.HTTPClient,
		baseURL:      cfg.BaseURL,
		now:          now,
		logger:       logger,
	}, nil
}

func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInvalidOrigin
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", ErrInvalidOrigin
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidOrigin
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", ErrInvalidOrigin
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// HookAttributes is the manifest's webhook block.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// Manifest is the GitHub App Manifest document. It deliberately requests only
// the permissions the round-3 architecture needs.
type Manifest struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	RedirectURL    string            `json:"redirect_url"`
	Public         bool              `json:"public"`
	HookAttributes HookAttributes    `json:"hook_attributes"`
	Permissions    map[string]string `json:"permissions"`
	Events         []string          `json:"events"`
}

// ManifestResponse is returned by POST /api/setup/github/manifest. The callback
// token is carried in a short-lived SameSite=Lax cookie, never in the JSON body.
type ManifestResponse struct {
	Action        string   `json:"action"`
	Manifest      Manifest `json:"manifest"`
	CallbackToken string   `json:"-"`
}

var organizationPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// CreateManifest builds the manifest, stores a one-time state, and returns the
// GitHub form action for a personal or organization App. The caller must keep
// CallbackToken out of the response body.
func (m *Manager) CreateManifest(ctx context.Context, owner string) (ManifestResponse, error) {
	owner = strings.TrimSpace(owner)
	if owner != "" && !organizationPattern.MatchString(owner) {
		return ManifestResponse{}, ErrInvalidOwner
	}
	if m.box == nil {
		return ManifestResponse{}, ErrMasterKey
	}
	webhookURL := m.publicOrigin + "/api/webhooks/github"
	active := originIsPublic(m.publicOrigin)
	if m.tunnel != nil {
		if hostname, err := m.tunnel.Address(ctx); err == nil {
			webhookURL = "https://" + hostname + "/api/webhooks/github"
			active = true
		}
	}
	name, err := appName()
	if err != nil {
		return ManifestResponse{}, err
	}
	manifest := Manifest{
		Name:        name,
		URL:         m.publicOrigin,
		RedirectURL: m.publicOrigin + ManifestCallbackPath,
		Public:      false,
		HookAttributes: HookAttributes{
			URL:    webhookURL,
			Active: active,
		},
		Permissions: map[string]string{
			"contents":      "write",
			"pull_requests": "write",
			"issues":        "write",
			"checks":        "read",
			"metadata":      "read",
		},
		Events: []string{
			"pull_request",
			"pull_request_review",
			"pull_request_review_comment",
			"issue_comment",
			"issues",
			"check_run",
		},
	}
	state, callbackToken, err := newState()
	if err != nil {
		return ManifestResponse{}, err
	}
	expires := m.now().Add(ManifestStateTTL)
	if err = m.store.CreateManifestState(ctx, hashToken(state), hashToken(callbackToken), expires); err != nil {
		return ManifestResponse{}, err
	}
	action := "https://github.com/settings/apps/new?state=" + url.QueryEscape(state)
	if owner != "" {
		action = "https://github.com/organizations/" + url.PathEscape(owner) + "/settings/apps/new?state=" + url.QueryEscape(state)
	}
	return ManifestResponse{Action: action, Manifest: manifest, CallbackToken: callbackToken}, nil
}

// HandleCallback validates and burns the state, exchanges the code for
// credentials, and stores only ciphertext.
func (m *Manager) HandleCallback(ctx context.Context, code, state, callbackToken string) error {
	state = strings.TrimSpace(state)
	callbackToken = strings.TrimSpace(callbackToken)
	if state == "" || callbackToken == "" || len(state) > 200 || len(callbackToken) > 200 {
		return store.ErrManifestStateInvalid
	}
	if err := m.store.ConsumeManifestState(ctx, hashToken(state), hashToken(callbackToken), m.now()); err != nil {
		return err
	}
	return m.finishCallback(ctx, code)
}

// HandleCallbackAdmin completes the flow when the browser proved the
// administrator session instead of carrying the SameSite=Lax binding cookie
// (for example during a same-site test or a manual replay). The one-time state
// is still the CSRF token, so it is consumed exactly once.
func (m *Manager) HandleCallbackAdmin(ctx context.Context, code, state string) error {
	state = strings.TrimSpace(state)
	if state == "" || len(state) > 200 {
		return store.ErrManifestStateInvalid
	}
	if err := m.store.ConsumeManifestStateAdmin(ctx, hashToken(state), m.now()); err != nil {
		return err
	}
	return m.finishCallback(ctx, code)
}

// finishCallback exchanges the one-time code and persists only ciphertext.
func (m *Manager) finishCallback(ctx context.Context, code string) error {
	conversion, err := githubapp.ConvertManifest(ctx, m.httpClient, m.baseURL, code)
	if err != nil {
		return err
	}
	if m.box == nil {
		return ErrMasterKey
	}
	privateKeyCipher, err := m.box.Seal(conversion.PEM)
	if err != nil {
		return errors.New("无法加密 GitHub App 私钥")
	}
	webhookCipher, err := m.box.Seal(conversion.WebhookSecret)
	if err != nil {
		return errors.New("无法加密 GitHub App webhook secret")
	}
	if err = m.store.SaveGitHubAppCredential(ctx, store.GitHubAppCredential{
		AppID:               conversion.ID,
		Slug:                conversion.Slug,
		HTMLURL:             conversion.HTMLURL,
		PrivateKeyCipher:    privateKeyCipher,
		WebhookSecretCipher: webhookCipher,
	}); err != nil {
		return err
	}
	m.provider.Invalidate()
	if m.tunnel != nil {
		m.tunnel.Wake()
	}
	return nil
}

// Repositories lists every repository the App's installations can access.
func (m *Manager) Repositories(ctx context.Context) ([]githubapp.Repository, error) {
	app, err := m.provider.App(ctx)
	if err != nil {
		return nil, err
	}
	return app.InstallationRepositories(ctx)
}

// GitHubStatus is the contract's github block.
type GitHubStatus struct {
	Configured    bool   `json:"configured"`
	Source        string `json:"source"`
	AppID         int64  `json:"appId"`
	AppSlug       string `json:"appSlug"`
	HTMLURL       string `json:"htmlUrl"`
	InstallURL    string `json:"installUrl"`
	Installations int    `json:"installations"`
}

// GitHubStatus projects the active App for the setup page. Network failures
// degrade to zero installation counts rather than failing the whole page.
func (m *Manager) GitHubStatus(ctx context.Context) GitHubStatus {
	credentials, err := m.provider.Credentials(ctx)
	if err != nil {
		if !errors.Is(err, ErrNotConfigured) {
			m.logger.Warn("GitHub App setup status is unavailable", "error", err)
		}
		return GitHubStatus{}
	}
	status := GitHubStatus{
		Configured: true,
		Source:     string(credentials.Source),
		AppID:      credentials.AppID,
		AppSlug:    credentials.Slug,
		HTMLURL:    credentials.HTMLURL,
	}
	app, err := m.provider.App(ctx)
	if err != nil {
		return status
	}
	if status.AppSlug == "" || status.HTMLURL == "" {
		infoCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		info, infoErr := app.Info(infoCtx)
		cancel()
		if infoErr == nil {
			if status.AppSlug == "" {
				status.AppSlug = info.Slug
			}
			if status.HTMLURL == "" {
				status.HTMLURL = info.HTMLURL
			}
		}
	}
	if status.AppSlug != "" {
		status.InstallURL = "https://github.com/apps/" + url.PathEscape(status.AppSlug) + "/installations/new"
	}
	installCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if installations, listErr := app.ListInstallations(installCtx); listErr == nil {
		status.Installations = len(installations)
	}
	return status
}

// TunnelStatus returns the tunnel block; a disabled tunnel is zero-valued.
func (m *Manager) TunnelStatus() TunnelStatus {
	return m.tunnel.Status()
}

func appName() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "swarmjet-" + hex.EncodeToString(b), nil
}

func newState() (string, string, error) {
	state := make([]byte, 32)
	callback := make([]byte, 32)
	if _, err := rand.Read(state); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(callback); err != nil {
		return "", "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(state), enc.EncodeToString(callback), nil
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// originIsPublic decides whether GitHub could plausibly reach PUBLIC_ORIGIN.
// Localhost and private addresses leave the manifest webhook inactive.
func originIsPublic(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())
	}
	return strings.Contains(host, ".")
}

// CallbackPath is exported for the API layer's cookie path.
const CallbackPath = ManifestCallbackPath
