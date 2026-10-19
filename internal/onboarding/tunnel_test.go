package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
)

type fakeTunnelApps struct {
	app    *githubapp.App
	secret string
	err    error
}

func (f *fakeTunnelApps) App(context.Context) (*githubapp.App, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.app, nil
}

func (f *fakeTunnelApps) WebhookSecret(context.Context) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.secret == "" {
		return "", errors.New("GitHub App webhook secret 未配置")
	}
	return f.secret, nil
}

type hookPatch struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret"`
}

func TestTunnelReadsAddressAndSyncsWebhook(t *testing.T) {
	var mu sync.Mutex
	hostname := "first-trycloudflare.trycloudflare.com"
	metricsCalls := 0
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		metricsCalls++
		host := hostname
		mu.Unlock()
		if r.URL.Path != "/quicktunnel" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"hostname": host})
	}))
	defer metrics.Close()

	var patches []hookPatch
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/app/hook/config" {
			http.NotFound(w, r)
			return
		}
		var patch hookPatch
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		patches = append(patches, patch)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"url": patch.URL})
	}))
	defer github.Close()

	privateKey, _ := testPrivateKey(t)
	app, err := githubapp.NewWithHTTP(githubapp.Config{AppID: 77, PrivateKeyPEM: privateKey}, github.URL, github.Client())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	tunnel, err := NewTunnel(TunnelConfig{MetricsURL: metrics.URL, Apps: &fakeTunnelApps{app: app, secret: "very-secret-webhook"}, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if err = tunnel.SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := tunnel.Status()
	if !status.Enabled || status.URL != "https://first-trycloudflare.trycloudflare.com" || !status.WebhookSynced || status.LastError != "" {
		t.Fatalf("status = %+v", status)
	}
	mu.Lock()
	firstPatches := append([]hookPatch(nil), patches...)
	mu.Unlock()
	if len(firstPatches) != 1 {
		t.Fatalf("patches = %+v", firstPatches)
	}
	if firstPatches[0].URL != "https://first-trycloudflare.trycloudflare.com/api/webhooks/github" ||
		firstPatches[0].ContentType != "json" || firstPatches[0].Secret != "very-secret-webhook" {
		t.Fatalf("patch = %+v", firstPatches[0])
	}

	// The same address and App must not PATCH again.
	if err = tunnel.SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(patches) != 1 {
		t.Fatalf("duplicate sync produced %d patches", len(patches))
	}
	hostname = "second-trycloudflare.trycloudflare.com"
	mu.Unlock()
	if err = tunnel.SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(patches) != 2 || patches[1].URL != "https://second-trycloudflare.trycloudflare.com/api/webhooks/github" {
		t.Fatalf("address change patches = %+v", patches)
	}
	mu.Unlock()
	if strings.Contains(logs.String(), "very-secret-webhook") {
		t.Fatal("webhook secret leaked into logs")
	}
}

func TestTunnelSyncWithoutAppRecordsError(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hostname":"unit-test.trycloudflare.com"}`))
	}))
	defer metrics.Close()
	var logs bytes.Buffer
	tunnel, err := NewTunnel(TunnelConfig{
		MetricsURL: metrics.URL,
		Apps:       &fakeTunnelApps{err: ErrNotConfigured},
		Logger:     slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = tunnel.SyncNow(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("sync error = %v", err)
	}
	status := tunnel.Status()
	if status.WebhookSynced || !strings.Contains(status.LastError, "GitHub App 未配置") {
		t.Fatalf("status = %+v", status)
	}
}

func TestTunnelRejectsInvalidAddressAndMetrics(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hostname":"bad host"}`))
	}))
	defer bad.Close()
	tunnel, err := NewTunnel(TunnelConfig{MetricsURL: bad.URL, Apps: &fakeTunnelApps{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tunnel.Address(context.Background()); err == nil {
		t.Fatal("invalid tunnel hostname was accepted")
	}
	if !strings.Contains(tunnel.Status().LastError, "读取隧道地址失败") {
		t.Fatalf("last error = %q", tunnel.Status().LastError)
	}
	if _, err = NewTunnel(TunnelConfig{MetricsURL: "not-a-url", Apps: &fakeTunnelApps{}}); err == nil {
		t.Fatal("relative metrics URL was accepted")
	}
}

func TestTunnelRunSyncsPeriodically(t *testing.T) {
	var mu sync.Mutex
	patches := 0
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hostname":"run-test.trycloudflare.com"}`))
	}))
	defer metrics.Close()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		patches++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"url":"https://run-test.trycloudflare.com/api/webhooks/github"}`))
	}))
	defer github.Close()
	privateKey, _ := testPrivateKey(t)
	app, err := githubapp.NewWithHTTP(githubapp.Config{AppID: 88, PrivateKeyPEM: privateKey}, github.URL, github.Client())
	if err != nil {
		t.Fatal(err)
	}
	tunnel, err := NewTunnel(TunnelConfig{MetricsURL: metrics.URL, Interval: 10 * time.Millisecond, Apps: &fakeTunnelApps{app: app, secret: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tunnel.Run(ctx)
	for i := 0; i < 100; i++ {
		mu.Lock()
		done := patches > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if patches == 0 {
		t.Fatal("Run did not sync the webhook")
	}
}
