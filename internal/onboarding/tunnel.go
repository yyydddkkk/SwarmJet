package onboarding

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
)

// TunnelAppSource is the slice of Provider the tunnel needs.
type TunnelAppSource interface {
	App(context.Context) (*githubapp.App, error)
	WebhookSecret(context.Context) (string, error)
}

// TunnelConfig configures the cloudflared metrics reader and webhook sync.
type TunnelConfig struct {
	MetricsURL string
	Interval   time.Duration
	HTTPClient *http.Client
	Apps       TunnelAppSource
	Logger     *slog.Logger
}

// TunnelStatus is the contract's tunnel block.
type TunnelStatus struct {
	Enabled       bool   `json:"enabled"`
	URL           string `json:"url"`
	WebhookSynced bool   `json:"webhookSynced"`
	LastError     string `json:"lastError"`
}

// Tunnel reads the public hostname from cloudflared's metrics server and keeps
// the App webhook in sync. cloudflared exposes GET /quicktunnel on the metrics
// listener, returning {"hostname":"<random>.trycloudflare.com"} for a quick
// tunnel.
type Tunnel struct {
	metricsURL string
	interval   time.Duration
	client     *http.Client
	apps       TunnelAppSource
	logger     *slog.Logger
	wake       chan struct{}

	mu            sync.Mutex
	hostname      string
	publicURL     string
	syncedKey     string
	webhookSynced bool
	lastError     string
}

var tunnelHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)

// NewTunnel validates the metrics address. An empty MetricsURL is an error;
// callers should only construct a Tunnel when SWARMJET_TUNNEL_URL is set.
func NewTunnel(cfg TunnelConfig) (*Tunnel, error) {
	raw := strings.TrimSpace(cfg.MetricsURL)
	if raw == "" {
		return nil, errors.New("tunnel metrics URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("SWARMJET_TUNNEL_URL must be an absolute http(s) URL")
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Apps == nil {
		return nil, errors.New("tunnel requires a GitHub App source")
	}
	return &Tunnel{
		metricsURL: strings.TrimRight(raw, "/"),
		interval:   interval,
		client:     client,
		apps:       cfg.Apps,
		logger:     logger,
		wake:       make(chan struct{}, 1),
	}, nil
}

// Enabled reports whether a tunnel was configured.
func (t *Tunnel) Enabled() bool { return t != nil }

// Wake asks the background loop to sync immediately, for example after a new
// App is created.
func (t *Tunnel) Wake() {
	if t == nil {
		return
	}
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// Status returns a snapshot for GET /api/setup/status.
func (t *Tunnel) Status() TunnelStatus {
	if t == nil {
		return TunnelStatus{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return TunnelStatus{
		Enabled:       true,
		URL:           t.publicURL,
		WebhookSynced: t.webhookSynced,
		LastError:     t.lastError,
	}
}

// Address returns the last known public hostname, fetching it on first use.
func (t *Tunnel) Address(ctx context.Context) (string, error) {
	if t == nil {
		return "", errors.New("tunnel is not configured")
	}
	t.mu.Lock()
	hostname := t.hostname
	t.mu.Unlock()
	if hostname != "" {
		return hostname, nil
	}
	return t.Refresh(ctx)
}

// Refresh reads /quicktunnel from cloudflared and records the public hostname.
func (t *Tunnel) Refresh(ctx context.Context) (string, error) {
	if t == nil {
		return "", errors.New("tunnel is not configured")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, t.metricsURL+"/quicktunnel", nil)
	if err != nil {
		return "", err
	}
	res, err := t.client.Do(req)
	if err != nil {
		t.setLastError("读取隧道地址失败：" + err.Error())
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		err = fmt.Errorf("cloudflared metrics returned HTTP %d", res.StatusCode)
		t.setLastError("读取隧道地址失败：" + err.Error())
		return "", err
	}
	var payload struct {
		Hostname string `json:"hostname"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&payload); err != nil {
		t.setLastError("读取隧道地址失败：响应格式无效")
		return "", errors.New("cloudflared metrics returned invalid JSON")
	}
	hostname := strings.ToLower(strings.TrimSpace(payload.Hostname))
	if !validTunnelHostname(hostname) {
		err = errors.New("cloudflared returned an invalid tunnel hostname")
		t.setLastError("读取隧道地址失败：" + err.Error())
		return "", err
	}
	t.mu.Lock()
	changed := t.hostname != hostname
	t.hostname = hostname
	t.publicURL = "https://" + hostname
	if changed {
		t.webhookSynced = false
	}
	t.lastError = ""
	t.mu.Unlock()
	return hostname, nil
}

func validTunnelHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 || !tunnelHostnamePattern.MatchString(hostname) {
		return false
	}
	return true
}

// SyncNow ensures the App webhook points at https://<tunnel>/api/webhooks/github
// with the current secret. It PATCHes only when the address or App changed.
func (t *Tunnel) SyncNow(ctx context.Context) error {
	if t == nil {
		return nil
	}
	hostname, err := t.Refresh(ctx)
	if err != nil {
		return err
	}
	app, err := t.apps.App(ctx)
	if err != nil {
		t.setLastError(err.Error())
		return err
	}
	secret, err := t.apps.WebhookSecret(ctx)
	if err != nil {
		t.setLastError(err.Error())
		return err
	}
	secretSum := sha256.Sum256([]byte(secret))
	key := fmt.Sprintf("%s|%d|%x", hostname, app.ID(), secretSum[:8])

	t.mu.Lock()
	synced := t.webhookSynced && t.syncedKey == key
	t.mu.Unlock()
	if synced {
		return nil
	}

	hookURL := "https://" + hostname + "/api/webhooks/github"
	if err = app.SetHookConfig(ctx, hookURL, secret); err != nil {
		t.setLastError("同步 GitHub webhook 失败：" + err.Error())
		return err
	}
	t.mu.Lock()
	t.syncedKey = key
	t.webhookSynced = true
	t.lastError = ""
	t.mu.Unlock()
	t.logger.Info("GitHub App webhook synchronized", "url", hookURL, "app_id", app.ID())
	return nil
}

// Run polls the tunnel address and syncs the webhook until ctx ends.
func (t *Tunnel) Run(ctx context.Context) {
	if t == nil {
		return
	}
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	t.trySync(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.wake:
			t.trySync(ctx)
		case <-ticker.C:
			t.trySync(ctx)
		}
	}
}

func (t *Tunnel) trySync(ctx context.Context) {
	syncCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := t.SyncNow(syncCtx); err != nil && ctx.Err() == nil {
		// The error text is stored in LastError for the setup page. It never
		// contains the webhook secret.
		t.logger.Warn("GitHub App webhook sync failed", "error", err)
	}
}

func (t *Tunnel) setLastError(message string) {
	t.mu.Lock()
	t.lastError = message
	t.mu.Unlock()
}
