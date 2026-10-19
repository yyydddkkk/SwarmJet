package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/onboarding"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// fakeGitHub is a minimal GitHub API used by the setup tests. It never verifies
// JWTs; the production client signing path is covered by internal/githubapp.
type fakeGitHub struct {
	server        *httptest.Server
	privateKeyPEM string
	webhookSecret string

	mu       sync.Mutex
	hookURL  string
	hookJSON map[string]any
	patches  int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGitHub{
		privateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		webhookSecret: "unit-webhook-secret",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/app-manifests/test-code/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             777,
			"slug":           "swarmjet-test",
			"html_url":       "https://github.com/apps/swarmjet-test",
			"pem":            g.privateKeyPEM,
			"webhook_secret": g.webhookSecret,
		})
	})
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       777,
			"slug":     "swarmjet-test",
			"name":     "swarmjet-test",
			"html_url": "https://github.com/apps/swarmjet-test",
		})
	})
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 42, "account": map[string]string{"login": "acme"}, "suspended_at": nil}})
	})
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "test-installation-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 1,
			"repositories": []map[string]any{{
				"id": 900, "name": "demo", "full_name": "acme/demo", "private": true, "default_branch": "main",
			}},
		})
	})
	mux.HandleFunc("/app/hook/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.hookJSON = body
		if value, ok := body["url"].(string); ok {
			g.hookURL = value
		}
		g.patches++
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"url": body["url"]})
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

func newSetupAPITest(t *testing.T, githubURL string) (*store.Store, http.Handler, *http.Cookie, *secretbox.Box, *onboarding.Manager, *onboarding.Provider) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	root, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	id, err := testID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "api_setup_" + strings.ReplaceAll(id, "-", "")
	if _, err = root.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = root.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config := root.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := &store.Store{Pool: pool}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s.Jobs = &hiveTestJobs{}

	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	provider := onboarding.NewProvider(onboarding.ProviderConfig{Store: s, Box: box, BaseURL: githubURL})
	manager, err := onboarding.NewManager(onboarding.ManagerConfig{
		Provider:     provider,
		Store:        s,
		Box:          box,
		PublicOrigin: "https://console.test",
		BaseURL:      githubURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(s, Config{
		Username:        "admin",
		Password:        "secret",
		AllowedOrigin:   "https://console.test",
		SecureCookies:   true,
		Setup:           manager,
		CellsEnabled:    true,
		CellsImage:      "swarmjet-agent:test",
		CellsModel:      "test/model",
		DockerAvailable: func() bool { return true },
	})
	cookie := hiveTestLogin(t, handler)
	return s, handler, cookie, box, manager, provider
}

func doSetupRequest(handler http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://console.test"+path, strings.NewReader(body))
	for _, cookie := range cookies {
		if cookie != nil {
			r.AddCookie(cookie)
		}
	}
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", "https://console.test")
		r.Header.Set("X-Requested-With", "swarmjet")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

type setupStatusJSON struct {
	GitHub struct {
		Configured    bool   `json:"configured"`
		Source        string `json:"source"`
		AppID         int64  `json:"appId"`
		AppSlug       string `json:"appSlug"`
		HTMLURL       string `json:"htmlUrl"`
		InstallURL    string `json:"installUrl"`
		Installations int    `json:"installations"`
	} `json:"github"`
	Tunnel struct {
		Enabled       bool   `json:"enabled"`
		URL           string `json:"url"`
		WebhookSynced bool   `json:"webhookSynced"`
		LastError     string `json:"lastError"`
	} `json:"tunnel"`
	Cells struct {
		Enabled         bool   `json:"enabled"`
		Image           string `json:"image"`
		Model           string `json:"model"`
		DockerAvailable bool   `json:"dockerAvailable"`
	} `json:"cells"`
	CLILogin struct {
		Available bool   `json:"available"`
		LoggedIn  bool   `json:"loggedIn"`
		CLI       string `json:"cli"`
	} `json:"cliLogin"`
}

func TestSetupManifestCallbackAndStatus(t *testing.T) {
	github := newFakeGitHub(t)
	s, handler, cookie, box, _, provider := newSetupAPITest(t, github.server.URL)

	// Unconfigured state still reports Cells and the extension point.
	w := doSetupRequest(handler, http.MethodGet, "/api/setup/status", "", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	var before setupStatusJSON
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.GitHub.Configured || before.GitHub.Source != "" || before.Tunnel.Enabled {
		t.Fatalf("unexpected initial setup status: %+v", before)
	}
	if !before.Cells.Enabled || before.Cells.Image != "swarmjet-agent:test" || before.Cells.Model != "test/model" || !before.Cells.DockerAvailable {
		t.Fatalf("cells status = %+v", before.Cells)
	}
	if before.CLILogin.Available || before.CLILogin.CLI != "opencode" {
		t.Fatalf("cli login status = %+v", before.CLILogin)
	}

	// Start the manifest flow.
	w = doSetupRequest(handler, http.MethodPost, "/api/setup/github/manifest", `{"owner":""}`, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest: %d %s", w.Code, w.Body.String())
	}
	var manifest struct {
		Action   string              `json:"action"`
		Manifest onboarding.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(manifest.Action, "https://github.com/settings/apps/new?state=") {
		t.Fatalf("action = %q", manifest.Action)
	}
	if manifest.Manifest.URL != "https://console.test" || manifest.Manifest.RedirectURL != "https://console.test/api/setup/github/callback" || manifest.Manifest.Public {
		t.Fatalf("manifest = %+v", manifest.Manifest)
	}
	if !manifest.Manifest.HookAttributes.Active || manifest.Manifest.HookAttributes.URL != "https://console.test/api/webhooks/github" {
		t.Fatalf("hook = %+v", manifest.Manifest.HookAttributes)
	}
	var callbackCookie *http.Cookie
	for _, item := range w.Result().Cookies() {
		if item.Name == onboarding.CallbackCookieName {
			callbackCookie = item
		}
	}
	if callbackCookie == nil || callbackCookie.SameSite != http.SameSiteLaxMode || callbackCookie.Path != onboarding.CallbackPath || callbackCookie.HttpOnly == false {
		t.Fatalf("callback cookie = %+v", callbackCookie)
	}
	if strings.Contains(w.Body.String(), callbackCookie.Value) {
		t.Fatal("callback token leaked into the response body")
	}
	parsed, err := url.Parse(manifest.Action)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	stateHash := sha256.Sum256([]byte(state))
	var storedState string
	if err = s.Pool.QueryRow(context.Background(), `SELECT state_hash FROM github_app_manifest_states`).Scan(&storedState); err != nil {
		t.Fatal(err)
	}
	if storedState != hex.EncodeToString(stateHash[:]) {
		t.Fatalf("stored state hash = %q", storedState)
	}

	// GitHub redirects back without the SameSite=Strict session cookie.
	callback := httptest.NewRequest(http.MethodGet, "https://console.test/api/setup/github/callback?code=test-code&state="+url.QueryEscape(state), nil)
	callback.AddCookie(callbackCookie)
	callbackRecorder := httptest.NewRecorder()
	handler.ServeHTTP(callbackRecorder, callback)
	if callbackRecorder.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", callbackRecorder.Code, callbackRecorder.Body.String())
	}
	if location := callbackRecorder.Header().Get("Location"); location != "/#/setup?github=connected" {
		t.Fatalf("callback location = %q", location)
	}

	// Only ciphertext is stored; the plaintext survives through the Provider.
	var appID int64
	var slug, keyCipher, secretCipher string
	if err = s.Pool.QueryRow(context.Background(), `SELECT app_id,slug,private_key_cipher,webhook_secret_cipher FROM github_apps`).Scan(&appID, &slug, &keyCipher, &secretCipher); err != nil {
		t.Fatal(err)
	}
	if appID != 777 || slug != "swarmjet-test" {
		t.Fatalf("app row = %d %q", appID, slug)
	}
	if keyCipher == github.privateKeyPEM || secretCipher == github.webhookSecret || strings.Contains(keyCipher, "PRIVATE KEY") {
		t.Fatal("credential was stored in plaintext")
	}
	if plain, err := box.Open(keyCipher); err != nil || plain != github.privateKeyPEM {
		t.Fatalf("private key decryption failed: %v", err)
	}
	if plain, err := box.Open(secretCipher); err != nil || plain != github.webhookSecret {
		t.Fatalf("webhook secret decryption failed: %v", err)
	}
	credentials, err := provider.Credentials(context.Background())
	if err != nil || credentials.AppID != 777 || credentials.Source != onboarding.SourceDatabase || credentials.WebhookSecret != github.webhookSecret {
		t.Fatalf("provider credentials = %+v, %v", credentials, err)
	}

	// Status now reports the database App and its repository install.
	w = doSetupRequest(handler, http.MethodGet, "/api/setup/status", "", cookie)
	var after setupStatusJSON
	if err = json.Unmarshal(w.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if !after.GitHub.Configured || after.GitHub.Source != "database" || after.GitHub.AppID != 777 ||
		after.GitHub.AppSlug != "swarmjet-test" || after.GitHub.InstallURL != "https://github.com/apps/swarmjet-test/installations/new" ||
		after.GitHub.Installations != 1 {
		t.Fatalf("configured status = %+v", after.GitHub)
	}
	if strings.Contains(w.Body.String(), github.webhookSecret) || strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Fatal("status leaked credential material")
	}

	// Repositories for the new-Hive picker.
	w = doSetupRequest(handler, http.MethodGet, "/api/setup/github/repositories", "", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("repositories: %d %s", w.Code, w.Body.String())
	}
	var repositories struct {
		Repositories []struct {
			FullName      string `json:"fullName"`
			Private       bool   `json:"private"`
			DefaultBranch string `json:"defaultBranch"`
		} `json:"repositories"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &repositories); err != nil {
		t.Fatal(err)
	}
	if len(repositories.Repositories) != 1 || repositories.Repositories[0].FullName != "acme/demo" ||
		!repositories.Repositories[0].Private || repositories.Repositories[0].DefaultBranch != "main" {
		t.Fatalf("repositories = %+v", repositories)
	}
	if strings.Contains(w.Body.String(), github.privateKeyPEM) {
		t.Fatal("repository list leaked the private key")
	}
}

func TestSetupCallbackRejectsBadState(t *testing.T) {
	github := newFakeGitHub(t)
	_, handler, cookie, _, _, _ := newSetupAPITest(t, github.server.URL)

	// A fresh flow, then a mismatch between state and browser cookie.
	w := doSetupRequest(handler, http.MethodPost, "/api/setup/github/manifest", `{"owner":""}`, cookie)
	var response struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(response.Action)
	state := parsed.Query().Get("state")

	// A valid state but the wrong browser binding is rejected.
	callback := httptest.NewRequest(http.MethodGet, "https://console.test/api/setup/github/callback?code=test-code&state="+url.QueryEscape(state), nil)
	callback.AddCookie(&http.Cookie{Name: onboarding.CallbackCookieName, Value: "wrong-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, callback)
	if location := recorder.Header().Get("Location"); !strings.Contains(location, "error=invalid_state") {
		t.Fatalf("wrong binding location = %q", location)
	}

	// Unauthenticated manifest creation is not reachable.
	unauthenticated := httptest.NewRequest(http.MethodPost, "https://console.test/api/setup/github/manifest", strings.NewReader(`{"owner":""}`))
	unauthenticated.Header.Set("Origin", "https://console.test")
	unauthenticated.Header.Set("X-Requested-With", "swarmjet")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, unauthenticated)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated manifest status = %d", recorder.Code)
	}
}

func TestSetupCallbackAcceptsAdminSessionWithState(t *testing.T) {
	github := newFakeGitHub(t)
	_, handler, cookie, _, _, provider := newSetupAPITest(t, github.server.URL)
	w := doSetupRequest(handler, http.MethodPost, "/api/setup/github/manifest", `{"owner":""}`, cookie)
	var response struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(response.Action)
	state := parsed.Query().Get("state")
	// An administrator session plus the one-time state is sufficient; GitHub's
	// cross-site redirect normally also carries the Lax callback cookie.
	callback := httptest.NewRequest(http.MethodGet, "https://console.test/api/setup/github/callback?code=test-code&state="+url.QueryEscape(state), nil)
	callback.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, callback)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/#/setup?github=connected" {
		t.Fatalf("admin callback = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	credentials, err := provider.Credentials(context.Background())
	if err != nil || credentials.AppID != 777 {
		t.Fatalf("admin callback credentials = %+v, %v", credentials, err)
	}
}
