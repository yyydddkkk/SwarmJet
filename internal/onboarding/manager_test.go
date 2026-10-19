package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type fakeManifestState struct {
	callbackHash string
	expires      time.Time
	used         bool
}

// fakeOnboardingStore implements both the manifest state store and the
// credential store, so a callback can be observed through the Provider.
type fakeOnboardingStore struct {
	mu     sync.Mutex
	states map[string]fakeManifestState
	record store.GitHubAppCredential
	saves  int
}

func newFakeOnboardingStore() *fakeOnboardingStore {
	return &fakeOnboardingStore{states: map[string]fakeManifestState{}}
}

func (f *fakeOnboardingStore) CreateManifestState(_ context.Context, stateHash, callbackHash string, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[stateHash] = fakeManifestState{callbackHash: callbackHash, expires: expires}
	return nil
}

func (f *fakeOnboardingStore) ConsumeManifestState(_ context.Context, stateHash, callbackHash string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, ok := f.states[stateHash]
	if !ok || state.used || state.callbackHash != callbackHash || !state.expires.After(now) {
		return store.ErrManifestStateInvalid
	}
	state.used = true
	f.states[stateHash] = state
	return nil
}

func (f *fakeOnboardingStore) ConsumeManifestStateAdmin(_ context.Context, stateHash string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, ok := f.states[stateHash]
	if !ok || state.used || !state.expires.After(now) {
		return store.ErrManifestStateInvalid
	}
	state.used = true
	f.states[stateHash] = state
	return nil
}

func (f *fakeOnboardingStore) SaveGitHubAppCredential(_ context.Context, credential store.GitHubAppCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record = credential
	f.saves++
	return nil
}

func (f *fakeOnboardingStore) GitHubAppCredential(context.Context) (store.GitHubAppCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.record.AppID == 0 {
		return store.GitHubAppCredential{}, store.ErrNotFound
	}
	return f.record, nil
}

func newTestManager(t *testing.T, origin string, tunnel *Tunnel, baseURL string, client *http.Client, now func() time.Time) (*Manager, *fakeOnboardingStore, *Provider) {
	t.Helper()
	box := testBox(t)
	stateStore := newFakeOnboardingStore()
	provider := NewProvider(ProviderConfig{Store: stateStore, Box: box, BaseURL: baseURL, HTTPClient: client})
	manager, err := NewManager(ManagerConfig{
		Provider:     provider,
		Store:        stateStore,
		Box:          box,
		PublicOrigin: origin,
		Tunnel:       tunnel,
		HTTPClient:   client,
		BaseURL:      baseURL,
		Now:          now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, stateStore, provider
}

func stateFromAction(t *testing.T, action string) string {
	t.Helper()
	parsed, err := url.Parse(action)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("action %q has no state", action)
	}
	return state
}

func TestCreateManifestContents(t *testing.T) {
	manager, stateStore, _ := newTestManager(t, "https://console.example.com", nil, "", nil, nil)
	response, err := manager.CreateManifest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.Action, "https://github.com/settings/apps/new?state=") {
		t.Fatalf("personal action = %q", response.Action)
	}
	manifest := response.Manifest
	if !strings.HasPrefix(manifest.Name, "swarmjet-") || len(manifest.Name) > 34 {
		t.Fatalf("name = %q", manifest.Name)
	}
	if manifest.URL != "https://console.example.com" || manifest.RedirectURL != "https://console.example.com/api/setup/github/callback" {
		t.Fatalf("urls = %q %q", manifest.URL, manifest.RedirectURL)
	}
	if manifest.Public {
		t.Fatal("public must be false")
	}
	if !manifest.HookAttributes.Active || manifest.HookAttributes.URL != "https://console.example.com/api/webhooks/github" {
		t.Fatalf("hook attributes = %+v", manifest.HookAttributes)
	}
	wantPermissions := map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"}
	for permission, value := range wantPermissions {
		if manifest.Permissions[permission] != value {
			t.Fatalf("permission %s = %q", permission, manifest.Permissions[permission])
		}
	}
	wantEvents := []string{"pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment", "issues", "check_run"}
	if strings.Join(manifest.Events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("events = %v", manifest.Events)
	}
	state := stateFromAction(t, response.Action)
	if _, ok := stateStore.states[hashToken(state)]; !ok {
		t.Fatal("state was not stored hashed")
	}
	if _, ok := stateStore.states[state]; ok {
		t.Fatal("raw state was stored")
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), response.CallbackToken) {
		t.Fatal("callback token leaked into the JSON body")
	}
}

func TestCreateManifestOrganizationAndLocalOrigin(t *testing.T) {
	manager, _, _ := newTestManager(t, "http://localhost:8180", nil, "", nil, nil)
	response, err := manager.CreateManifest(context.Background(), "my-org")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.Action, "https://github.com/organizations/my-org/settings/apps/new?state=") {
		t.Fatalf("organization action = %q", response.Action)
	}
	if response.Manifest.HookAttributes.Active {
		t.Fatal("localhost webhook must start inactive")
	}
	if response.Manifest.HookAttributes.URL != "http://localhost:8180/api/webhooks/github" {
		t.Fatalf("local hook = %q", response.Manifest.HookAttributes.URL)
	}
	if _, err = manager.CreateManifest(context.Background(), "bad org"); !errors.Is(err, ErrInvalidOwner) {
		t.Fatalf("invalid owner error = %v", err)
	}
}

func TestCreateManifestUsesTunnelAddress(t *testing.T) {
	tunnelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/quicktunnel" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"hostname":"unit-test.trycloudflare.com"}`))
	}))
	defer tunnelServer.Close()
	box := testBox(t)
	stateStore := newFakeOnboardingStore()
	provider := NewProvider(ProviderConfig{Store: stateStore, Box: box})
	tunnel, err := NewTunnel(TunnelConfig{MetricsURL: tunnelServer.URL, Apps: provider})
	if err != nil {
		t.Fatal(err)
	}
	manager, _, _ := newTestManager(t, "http://localhost:8180", tunnel, "", nil, nil)
	response, err := manager.CreateManifest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !response.Manifest.HookAttributes.Active || response.Manifest.HookAttributes.URL != "https://unit-test.trycloudflare.com/api/webhooks/github" {
		t.Fatalf("tunnel hook attributes = %+v", response.Manifest.HookAttributes)
	}
}

func TestManifestCallbackEncryptsAndConsumesState(t *testing.T) {
	privateKey, _ := testPrivateKey(t)
	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/good-code/conversions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             4242,
			"slug":           "swarmjet-unit",
			"html_url":       "https://github.com/apps/swarmjet-unit",
			"pem":            string(privateKey),
			"webhook_secret": "conversion-secret",
		})
	}))
	defer githubServer.Close()
	now := time.Now()
	manager, stateStore, provider := newTestManager(t, "https://console.example.com", nil, githubServer.URL, githubServer.Client(), func() time.Time { return now })
	response, err := manager.CreateManifest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFromAction(t, response.Action)
	if err = manager.HandleCallback(context.Background(), "good-code", state, response.CallbackToken); err != nil {
		t.Fatal(err)
	}
	record, err := stateStore.GitHubAppCredential(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if record.AppID != 4242 || record.Slug != "swarmjet-unit" || record.PrivateKeyCipher == "" || record.WebhookSecretCipher == "" {
		t.Fatalf("record = %+v", record)
	}
	if record.PrivateKeyCipher == string(privateKey) || record.WebhookSecretCipher == "conversion-secret" {
		t.Fatal("credential was stored in plaintext")
	}
	credentials, err := provider.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AppID != 4242 || credentials.WebhookSecret != "conversion-secret" || string(credentials.PrivateKeyPEM) != string(privateKey) {
		t.Fatalf("provider credentials = %+v", credentials)
	}
	// One-time: the same state cannot be replayed.
	if err = manager.HandleCallback(context.Background(), "good-code", state, response.CallbackToken); !errors.Is(err, store.ErrManifestStateInvalid) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestManifestCallbackStateExpiryAndBinding(t *testing.T) {
	now := time.Now()
	manager, _, _ := newTestManager(t, "https://console.example.com", nil, "", nil, func() time.Time { return now })
	response, err := manager.CreateManifest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFromAction(t, response.Action)
	if err = manager.HandleCallback(context.Background(), "code", state, "wrong-callback-token"); !errors.Is(err, store.ErrManifestStateInvalid) {
		t.Fatalf("wrong binding error = %v", err)
	}
	now = now.Add(ManifestStateTTL + time.Second)
	if err = manager.HandleCallback(context.Background(), "code", state, response.CallbackToken); !errors.Is(err, store.ErrManifestStateInvalid) {
		t.Fatalf("expired state error = %v", err)
	}
}

func TestManifestCallbackConversionFailureDoesNotStore(t *testing.T) {
	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer githubServer.Close()
	manager, stateStore, _ := newTestManager(t, "https://console.example.com", nil, githubServer.URL, githubServer.Client(), nil)
	response, err := manager.CreateManifest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFromAction(t, response.Action)
	if err = manager.HandleCallback(context.Background(), "bad-code", state, response.CallbackToken); err == nil {
		t.Fatal("conversion failure was accepted")
	}
	if stateStore.saves != 0 {
		t.Fatalf("saved %d credentials after a failed conversion", stateStore.saves)
	}
}

func TestGitHubStatusEnvironmentSource(t *testing.T) {
	_, path := testPrivateKey(t)
	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       314,
				"slug":     "env-app",
				"html_url": "https://github.com/apps/env-app",
			})
		case "/app/installations":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 9, "account": map[string]string{"login": "acme"}, "suspended_at": nil}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer githubServer.Close()
	stateStore := newFakeOnboardingStore()
	provider := NewProvider(ProviderConfig{
		Env:        EnvConfig{AppID: "314", PrivateKeyFile: path, WebhookSecret: "env-secret"},
		Store:      stateStore,
		Box:        testBox(t),
		BaseURL:    githubServer.URL,
		HTTPClient: githubServer.Client(),
	})
	manager, err := NewManager(ManagerConfig{Provider: provider, Store: stateStore, PublicOrigin: "https://console.example.com", BaseURL: githubServer.URL, HTTPClient: githubServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	status := manager.GitHubStatus(context.Background())
	if !status.Configured || status.Source != string(SourceEnv) || status.AppID != 314 ||
		status.AppSlug != "env-app" || status.HTMLURL != "https://github.com/apps/env-app" ||
		status.InstallURL != "https://github.com/apps/env-app/installations/new" || status.Installations != 1 {
		t.Fatalf("environment status = %+v", status)
	}
}

func TestTunnelStatusReflectsManifestRead(t *testing.T) {
	tunnelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hostname":"status-test.trycloudflare.com"}`))
	}))
	defer tunnelServer.Close()
	box := testBox(t)
	stateStore := newFakeOnboardingStore()
	provider := NewProvider(ProviderConfig{Store: stateStore, Box: box})
	tunnel, err := NewTunnel(TunnelConfig{MetricsURL: tunnelServer.URL, Apps: provider})
	if err != nil {
		t.Fatal(err)
	}
	manager, _, _ := newTestManager(t, "http://localhost:8180", tunnel, "", nil, nil)
	if _, err = manager.CreateManifest(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	status := manager.TunnelStatus()
	if !status.Enabled || status.URL != "https://status-test.trycloudflare.com" {
		t.Fatalf("tunnel status = %+v", status)
	}
}
