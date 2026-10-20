package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
	"github.com/yyydddkkk/SwarmJet/internal/llmproxy"
	"github.com/yyydddkkk/SwarmJet/internal/onboarding"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// Config is the browser console configuration. The console has exactly one
// administrator account (ADMIN_USERNAME / ADMIN_PASSWORD); GitHubApp and Box
// are the extension points other packages use for onboarding and CLI login.
type Config struct {
	GitHubApp     *githubapp.App
	Box           *secretbox.Box
	Username      string
	Password      string
	AllowedOrigin string
	StaticDir     string
	SecureCookies bool
	// Setup powers the GitHub App onboarding page. nil disables /api/setup/*.
	Setup *onboarding.Manager
	// Cells status is read from the same environment that starts the supervisor.
	CellsEnabled    bool
	CellsImage      string
	CellsModel      string
	DockerAvailable func() bool
	// MCPURL is the endpoint embedded in generated local Agent configurations.
	MCPURL string
	// Merge powers GET /api/hives/{id}/prs/{number}. nil disables it.
	Merge MergeStatus
	// Models is the auth-broker/gateway client used by the credential and
	// model endpoints. nil means the services are not part of this deployment.
	Models *llmproxy.BrokerClient
	// DefaultModel is the Hive's default provider/model selector.
	DefaultModel string
}

type server struct {
	s        *store.Store
	cfg      Config
	mu       sync.Mutex
	attempts map[string]attempt
}

type attempt struct {
	start time.Time
	count int
}

// New returns the console HTTP handler. The Hive endpoints live in hives.go;
// everything that used to serve the retired pipeline, tasks, nodes, networks
// and multi-user features was removed in round 3.
func New(s *store.Store, cfg Config) http.Handler {
	a := &server{s: s, cfg: cfg, attempts: map[string]attempt{}}
	return http.HandlerFunc(a.serve)
}

func (a *server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		respond(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		a.static(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !csrfAllowed(r, a.cfg.AllowedOrigin) {
		problem(w, http.StatusForbidden, "same-origin swarmjet request required")
		return
	}
	// GitHub redirects the browser here from github.com. The session cookie is
	// SameSite=Strict, so it is not sent on that cross-site navigation; the
	// handler validates the one-time state and its callback cookie (or a
	// present administrator session).
	if r.URL.Path == onboarding.ManifestCallbackPath {
		a.setupGitHubCallback(w, r)
		return
	}
	if r.URL.Path == "/api/login" && r.Method == http.MethodPost {
		a.login(w, r)
		return
	}
	principal, err := a.sessionPrincipal(r)
	if err != nil {
		problem(w, http.StatusUnauthorized, "authentication required")
		return
	}
	r = withPrincipal(r, principal)
	// The console has a single administrator account, so every authenticated
	// session may use the Hive and Forager endpoints.
	if r.URL.Path == "/api/setup/status" || strings.HasPrefix(r.URL.Path, "/api/setup/github/") {
		a.setup(w, r)
		return
	}
	if r.URL.Path == "/api/models" || strings.HasPrefix(r.URL.Path, "/api/models/credentials/") {
		a.models(w, r)
		return
	}
	if r.URL.Path == "/api/usage" {
		a.usage(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/runtime/") {
		problem(w, http.StatusNotFound, "runtime route not found")
		return
	}
	if r.URL.Path == "/api/tokens" {
		a.tokens(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/tokens/") {
		a.tokenItem(w, r, strings.TrimPrefix(r.URL.Path, "/api/tokens/"))
		return
	}
	if a.pullRequestStatus(w, r) {
		return
	}
	if r.URL.Path == "/api/hives" || strings.HasPrefix(r.URL.Path, "/api/hives/") ||
		strings.HasPrefix(r.URL.Path, "/api/foragers/") {
		a.hives(w, r)
		return
	}
	switch r.URL.Path {
	case "/api/logout":
		if r.Method != http.MethodPost {
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if c, err := r.Cookie("swarmjet_session"); err == nil {
			a.s.DeleteSession(r.Context(), tokenHash(c.Value))
		}
		a.cookie(w, "", -1)
		w.WriteHeader(http.StatusNoContent)
	case "/api/session":
		if r.Method != http.MethodGet {
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		username := principal.Email
		if principal.Breakglass {
			username = a.cfg.Username
		}
		respond(w, http.StatusOK, map[string]any{
			"id": principal.UserID, "username": username, "email": principal.Email,
			"displayName": principal.DisplayName, "platformAdmin": principal.PlatformAdmin,
			"breakGlass": principal.Breakglass,
		})
	default:
		problem(w, http.StatusNotFound, "API route not found")
	}
}

func (a *server) cookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "swarmjet_session", Value: value, Path: "/api", HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

func (a *server) loginAllowed(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.Sub(v.start) > time.Minute {
			delete(a.attempts, k)
		}
	}
	if len(a.attempts) >= 4096 {
		if _, exists := a.attempts[host]; !exists {
			return false
		}
	}
	v := a.attempts[host]
	if v.start.IsZero() {
		v.start = now
	}
	v.count++
	a.attempts[host] = v
	return v.count <= 10
}

func (a *server) login(w http.ResponseWriter, r *http.Request) {
	if !a.loginAllowed(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		problem(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if a.cfg.Username == "" || a.cfg.Password == "" ||
		!secureEqual(req.Username, a.cfg.Username) || !secureEqual(req.Password, a.cfg.Password) {
		problem(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, err := opaqueToken()
	if err != nil {
		failure(w, err)
		return
	}
	if err = a.s.AddBreakglassSession(r.Context(), tokenHash(token)); err != nil {
		failure(w, err)
		return
	}
	a.cookie(w, token, 12*60*60)
	respond(w, http.StatusOK, map[string]any{"username": a.cfg.Username, "platformAdmin": true, "breakGlass": true})
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func problem(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		problem(w, http.StatusConflict, err.Error())
	default:
		problem(w, http.StatusInternalServerError, "operation failed; check server configuration and retry")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		problem(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		problem(w, http.StatusBadRequest, "request must contain one JSON value")
		return false
	}
	return true
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func opaqueToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

func secureEqual(a, b string) bool {
	aa := sha256.Sum256([]byte(a))
	bb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aa[:], bb[:]) == 1
}

func csrfAllowed(r *http.Request, configured string) bool {
	if r.Header.Get("X-Requested-With") != "swarmjet" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	expected := configured
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return origin == strings.TrimRight(expected, "/")
}

func (a *server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		problem(w, http.StatusNotFound, "API route not found")
		return
	}
	if a.cfg.StaticDir == "" {
		http.NotFound(w, r)
		return
	}
	root, err := filepath.Abs(a.cfg.StaticDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(root, filepath.Clean("/"+r.URL.Path))
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		path = filepath.Join(root, "index.html")
	}
	http.ServeFile(w, r, path)
}
