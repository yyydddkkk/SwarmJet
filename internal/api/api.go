package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarmjet/internal/artifacts"
	"swarmjet/internal/credentials"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/forge"
	"swarmjet/internal/githubapp"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/imagebuild"
	"swarmjet/internal/intake"
	"swarmjet/internal/labels"
	"swarmjet/internal/observability"
	"swarmjet/internal/repochange"
	"swarmjet/internal/secretbox"
	"swarmjet/internal/store"
)

type Config struct {
	Metrics                                               *observability.Metrics
	GitHubApp                                             *githubapp.App
	MihomoURL, MihomoSecret                               string
	Username, Password, AllowedOrigin, StaticDir, DataDir string
	DefaultAgentImage                                     string
	BuilderURL                                            string
	RegistryCAFile                                        string
	PlatformImage                                         string
	RuntimeURL, RuntimeSecret                             string
	NodeSecret                                            string
	RequireNode                                           bool
	Nodes                                                 execution.NodeResolver
	Artifacts                                             artifacts.Store
	LabelsWake                                            chan struct{}
	SecureCookies                                         bool
	ResolveCredential                                     func(string) (string, error)
	// Box encrypts saved connection tokens. nil disables connection storage
	// while deployment environment credentials keep working.
	Box *secretbox.Box
	// Initializer runs the durable managed-Meta initialization. nil disables the
	// initialization endpoints.
	Initializer       Initializer
	RepositoryChanges interface {
		Start(context.Context, string, repochange.AddRequest) (domain.RepositoryChange, error)
		List(context.Context, string) ([]domain.RepositoryChange, error)
		Available(context.Context, string, string, int) (gitlab.ProjectPage, error)
		Retry(context.Context, string, string) (domain.RepositoryChange, error)
	}
}
type server struct {
	s              *store.Store
	conns          connectionStore
	cfg            Config
	mu             sync.Mutex
	attempts       map[string]attempt
	workbenchPages workbenchPageCache
}
type attempt struct {
	start time.Time
	count int
}

func New(s *store.Store, cfg Config) http.Handler {
	a := &server{s: s, cfg: cfg, attempts: map[string]attempt{}}
	if s != nil {
		a.conns = s
	}
	return http.HandlerFunc(a.serve)
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
		problem(w, 404, "not found")
	case errors.Is(err, store.ErrConflict):
		problem(w, 409, err.Error())
	default:
		problem(w, 500, "operation failed; check server configuration and retry")
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		problem(w, 400, "invalid JSON request")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		problem(w, 400, "request must contain one JSON value")
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
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
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
func (a *server) authenticated(r *http.Request) bool {
	c, err := r.Cookie("swarmjet_session")
	return err == nil && len(c.Value) == 64 && a.s != nil && a.s.Session(r.Context(), tokenHash(c.Value))
}
func (a *server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		a.static(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/nodes" && r.Method == "POST" {
		a.nodeHeartbeat(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && !csrfAllowed(r, a.cfg.AllowedOrigin) {
		problem(w, 403, "same-origin swarmjet request required")
		return
	}
	if r.URL.Path == "/api/login" && r.Method == "POST" {
		a.login(w, r)
		return
	}
	if !a.authenticated(r) {
		problem(w, 401, "authentication required")
		return
	}
	if r.URL.Path == "/api/system/developer-options" {
		a.developerOptions(w, r)
		return
	}
	if r.URL.Path == "/api/system" || r.URL.Path == "/api/system/checks" {
		a.system(w, r)
		return
	}
	if r.URL.Path == "/api/gitlab-identities" {
		a.gitlabIdentities(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/workbench/") {
		a.workbench(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/mihomo/") {
		a.mihomo(w, r)
		return
	}
	if r.URL.Path == "/api/registry" || r.URL.Path == "/api/registry/reveal" {
		a.registry(w, r)
		return
	}
	if r.URL.Path == "/api/registry/images" || strings.HasPrefix(r.URL.Path, "/api/registry/images/") {
		a.registryImages(w, r)
		return
	}
	if r.URL.Path == "/api/registry/platform-images" || strings.HasPrefix(r.URL.Path, "/api/registry/platform-images/") {
		a.platformImages(w, r)
		return
	}
	if r.URL.Path == "/api/networks" || r.URL.Path == "/api/networks/default" {
		a.networks(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/nodes/") {
		a.manageNode(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/runtime/") {
		a.runtime(w, r)
		return
	}
	switch r.URL.Path {
	case "/api/logout":
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return
		}
		if c, err := r.Cookie("swarmjet_session"); err == nil {
			a.s.DeleteSession(r.Context(), tokenHash(c.Value))
		}
		a.cookie(w, "", -1)
		w.WriteHeader(204)
		return
	case "/api/session":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		respond(w, 200, map[string]string{"username": a.cfg.Username})
		return
	case "/api/connections":
		a.connections(w, r)
		return
	case "/api/github/installations":
		a.githubInstallations(w, r)
		return
	case "/api/connections/verify":
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return
		}
		a.verifyConnection(w, r)
		return
	case "/api/project-defaults":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		image := a.cfg.DefaultAgentImage
		if !a.cfg.RequireNode {
			var err error
			image, err = imagebuild.CurrentImage(a.cfg.DataDir, image)
			if err != nil {
				failure(w, err)
				return
			}
		}
		if image == "" {
			image = "swarmjet-agent:local"
		}
		if a.cfg.RequireNode || a.cfg.BuilderURL != "" {
			image = ""
		}
		respond(w, 200, map[string]any{"distributed": a.cfg.RequireNode || a.cfg.BuilderURL != "", "agentImage": image, "adapter": "opencode", "timeoutSeconds": 5400, "maxReworks": 3})
		return
	case "/api/projects":
		a.projects(w, r)
		return
	case "/api/projects/discover":
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return
		}
		a.discover(w, r)
		return
	case "/api/tasks":
		a.tasks(w, r)
		return
	case "/api/nodes":
		a.nodes(w, r)
		return
	case "/api/operations":
		if r.Method != http.MethodGet {
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if a.s == nil {
			problem(w, http.StatusServiceUnavailable, "operations unavailable")
			return
		}
		snapshot, err := a.s.OperationsSnapshot(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusOK, snapshot)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if len(parts) == 4 && parts[0] == "github" && parts[1] == "installations" && parts[3] == "connections" {
		a.githubInstallationConnection(w, r, parts[2])
		return
	}
	if len(parts) >= 2 && domain.IDPattern.MatchString(parts[1]) {
		switch parts[0] {
		case "connections":
			a.connection(w, r, parts[1], parts[2:])
			return
		case "projects":
			a.project(w, r, parts[1], parts[2:])
			return
		case "tasks":
			a.task(w, r, parts[1], parts[2:])
			return
		}
	}
	problem(w, 404, "API route not found")
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
		problem(w, 429, "too many login attempts")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	userOK := secureEqual(req.Username, a.cfg.Username)
	passwordOK := secureEqual(req.Password, a.cfg.Password)
	if a.cfg.Username == "" || a.cfg.Password == "" || !userOK || !passwordOK {
		problem(w, 401, "invalid username or password")
		return
	}
	token, err := opaqueToken()
	if err != nil {
		failure(w, err)
		return
	}
	if err = a.s.AddSession(r.Context(), tokenHash(token)); err != nil {
		failure(w, err)
		return
	}
	a.cookie(w, token, 12*60*60)
	respond(w, 200, map[string]string{"username": a.cfg.Username})
}
func (a *server) projects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		p, err := a.s.Projects(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, p)
	case "POST":
		var p domain.Project
		if !decode(w, r, &p) {
			return
		}
		if p.Version != 0 {
			problem(w, 400, "new project version must be zero")
			return
		}
		a.saveProject(w, r, p)
	default:
		problem(w, 405, "method not allowed")
	}
}
func (a *server) saveProject(w http.ResponseWriter, r *http.Request, p domain.Project) {
	if p.Version > 0 && a.s != nil && a.s.Pool != nil {
		previous, err := a.s.Project(r.Context(), p.ID)
		if err != nil {
			failure(w, err)
			return
		}
		if previous.HostingProvider() != p.HostingProvider() {
			problem(w, http.StatusBadRequest, "已有项目不能切换托管平台。")
			return
		}
	}
	if p.Version == 0 && p.AcceptanceStrategy == "" {
		p.AcceptanceStrategy = "none"
	}
	if p.Version == 0 && p.ReviewMode == "" {
		p.ReviewMode = "none"
	}

	// A saved connection is bound to its verified hosting platform and origin.
	if p.ConnectionID != "" {
		if a.conns == nil {
			problem(w, 503, "连接存储未启用：请在部署中配置 SWARMJET_MASTER_KEY。")
			return
		}
		connection, err := a.conns.Connection(r.Context(), p.ConnectionID)
		if err != nil {
			problem(w, 400, "connectionId 引用了不存在的连接。")
			return
		}
		if connection.HostingProvider() != p.HostingProvider() {
			problem(w, 400, "项目平台与连接平台不一致。")
			return
		}
		if p.HostingProvider() == domain.ProviderGitHub {
			if err := a.validateGitHubProject(r.Context(), p, connection); err != nil {
				problem(w, 400, err.Error())
				return
			}
		} else {
			if p.GitLabURL == "" {
				p.GitLabURL = connection.GitLabURL
			}
			if domain.GitLabIdentity(connection.GitLabURL) != domain.GitLabIdentity(p.GitLabURL) {
				problem(w, 400, "项目 GitLab 地址与所选连接不一致，已拒绝保存。")
				return
			}
		}
	}
	if err := domain.ValidateProjectForSave(p); err != nil {
		problem(w, 400, err.Error())
		return
	}
	// Managed projects can only be enabled after initialization has verified the
	// analysis; this gate is enforced server-side, not only in the console.
	if p.Enabled && p.ManagedMeta() {
		if a.cfg.Initializer == nil || !a.cfg.Initializer.ReadyForTasks(r.Context(), p.ID) {
			problem(w, 409, "Meta 仓库尚未初始化完成，不能启用项目。请先完成初始化。")
			return
		}
	}
	if p.Enabled && !p.ManagedMeta() {
		checkRemote := p.Version == 0
		if p.Version > 0 {
			previous, err := a.s.Project(r.Context(), p.ID)
			if err != nil {
				failure(w, err)
				return
			}
			checkRemote = !previous.Enabled || previous.MetaURL != p.MetaURL || previous.MetaRef != p.MetaRef ||
				previous.GitLabURL != p.GitLabURL || previous.ConnectionID != p.ConnectionID ||
				previous.CredentialRef != p.CredentialRef || previous.AcceptanceStrategy != p.AcceptanceStrategy ||
				!reflect.DeepEqual(previous.Repositories, p.Repositories) || !reflect.DeepEqual(previous.Integration, p.Integration)
		}
		if checkRemote {
			token, err := a.projectCredential(p)
			if err != nil {
				problem(w, 409, "无法使用项目的 GitLab 凭据读取 Meta-Repo："+err.Error())
				return
			}
			if err = checkMeta(r.Context(), p, token); err != nil {
				problem(w, 409, "Meta-Repo 校验未通过："+err.Error())
				return
			}
		}
	}

	p, err := a.s.SaveProject(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	if p.Enabled {
		a.wakeLabels()
	}
	respond(w, 200, p)
}
func (a *server) project(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	if len(tail) == 0 {
		switch r.Method {
		case "GET":
			p, err := a.s.Project(r.Context(), id)
			if err != nil {
				failure(w, err)
				return
			}
			respond(w, 200, p)
		case "PUT":
			var p domain.Project
			if !decode(w, r, &p) {
				return
			}
			if p.ID != id || p.Version < 1 {
				problem(w, 400, "project ID and current version are required")
				return
			}
			a.saveProject(w, r, p)
		default:
			problem(w, 405, "method not allowed")
		}
		return
	}
	if len(tail) >= 1 && tail[0] == "initialization" {
		a.initialization(w, r, id, tail[1:])
		return
	}
	if len(tail) >= 1 && tail[0] == "repository-changes" {
		a.repositoryChanges(w, r, id, tail[1:])
		return
	}
	if len(tail) == 1 && tail[0] == "labels" {
		a.projectLabels(w, r, id)
		return
	}
	if len(tail) == 1 && tail[0] == "check" && r.Method == "POST" {
		p, err := a.s.Project(r.Context(), id)
		if err != nil {
			failure(w, err)
			return
		}
		a.check(w, r, p)
		return
	}
	problem(w, 404, "API route not found")
}

func (a *server) wakeLabels() {
	if a.cfg.LabelsWake == nil {
		return
	}
	select {
	case a.cfg.LabelsWake <- struct{}{}:
	default:
	}
}

func (a *server) projectLabels(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "GET" && r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	p, err := a.s.Project(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	if !p.Enabled {
		problem(w, 409, "项目启用后才能管理标签")
		return
	}
	enabled, err := a.s.EnabledAgents(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	client, err := a.hostClient(p)
	if err != nil {
		problem(w, 503, "GitLab credential unavailable")
		return
	}
	wanted := labels.Selection(enabled)
	type repoStatus struct {
		RepositoryID string          `json:"repositoryId"`
		ProjectID    int64           `json:"projectId"`
		Labels       []labels.Status `json:"labels"`
		Error        string          `json:"error,omitempty"`
	}
	out := make([]repoStatus, 0, len(p.Repositories))
	for _, repo := range p.Repositories {
		item := repoStatus{RepositoryID: repo.ID, ProjectID: repo.ProjectID}
		if r.Method == "POST" {
			item.Labels, err = labels.Ensure(r.Context(), client, repo.ProjectID, wanted)
		} else {
			item.Labels, err = labels.Plan(r.Context(), client, repo.ProjectID, wanted)
		}
		if err != nil {
			item.Error = err.Error()
		}
		out = append(out, item)
	}
	respond(w, 200, map[string]any{"repositories": out})
}
func (a *server) credential(ref string) (string, error) {
	// Saved connections are resolved locally so the console never depends on a
	// deployment environment variable for a token the user typed in.
	if id, merge := credentials.UseMerge(ref); id != "" {
		return a.connectionToken(id, merge)
	}
	if a.cfg.ResolveCredential != nil {
		return a.cfg.ResolveCredential(ref)
	}
	value := os.Getenv(ref)
	if value == "" {
		return "", errors.New("credential unavailable")
	}
	return value, nil
}
func (a *server) client(p domain.Project) (*gitlab.Client, error) {
	token, err := a.projectCredential(p)
	if err != nil {
		return nil, err
	}
	return gitlab.New(p.GitLabURL, token), nil
}

func (a *server) hostClient(p domain.Project) (forge.Client, error) {
	if p.HostingProvider() == domain.ProviderGitHub {
		if a.conns == nil {
			return nil, errors.New("GitHub connection storage is unavailable")
		}
		connection, err := a.conns.Connection(context.Background(), p.ConnectionID)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, 0, len(p.Repositories))
		for _, repo := range p.Repositories {
			ids = append(ids, repo.ProjectID)
		}
		return a.githubClient(connection, ids)
	}
	return a.client(p)
}

// projectCredential resolves a project token and enforces that a saved
// connection is only used with the GitLab address it was verified against.
func (a *server) projectCredential(p domain.Project) (string, error) {
	if p.ConnectionID == "" {
		return a.credential(p.CredentialReference())
	}
	if strings.TrimSpace(p.GitLabURL) == "" {
		return "", errors.New("saved connection requires the project GitLab address")
	}
	if a.conns == nil {
		return "", errors.New("connection storage is not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := a.conns.Connection(ctx, p.ConnectionID)
	if err != nil {
		return "", errors.New("saved connection is unavailable")
	}
	if domain.GitLabIdentity(connection.GitLabURL) != domain.GitLabIdentity(p.GitLabURL) {
		return "", errors.New("saved connection does not match the project GitLab address")
	}
	return a.credential(p.CredentialReference())
}
func validDependencies(refs []domain.IssueRef, self domain.IssueRef) bool {
	seen := map[string]bool{}
	if len(refs) > 100 {
		return false
	}
	for _, ref := range refs {
		key := fmt.Sprintf("%s/%d/%d", ref.HostingProvider(), ref.ProjectID, ref.IID)
		if ref.ProjectID < 1 || ref.IID < 1 || ref.HostingProvider() == self.HostingProvider() && ref.ProjectID == self.ProjectID && ref.IID == self.IID || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func (a *server) tasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		query := r.URL.Query()
		limit := 50
		if raw := query.Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 100 {
				problem(w, 400, "invalid task page limit")
				return
			}
			limit = value
		}
		view := query.Get("view")
		if view != "" && view != "all" && view != "active" && view != "waiting" && view != "attention" && view != "ended" {
			problem(w, 400, "invalid task view")
			return
		}
		project := query.Get("project")
		if project != "" && !domain.IDPattern.MatchString(project) {
			problem(w, 400, "invalid project filter")
			return
		}
		tasks, err := a.s.TaskPage(r.Context(), store.TaskPageOptions{ProjectID: project, Stage: query.Get("stage"), View: view, Search: query.Get("q"), Cursor: query.Get("cursor"), Limit: limit})
		if err != nil {
			if strings.Contains(err.Error(), "task cursor") || strings.Contains(err.Error(), "task search") {
				problem(w, 400, err.Error())
				return
			}
			failure(w, err)
			return
		}
		respond(w, 200, tasks)
	case "POST":
		var req struct {
			ProjectID      string            `json:"projectId"`
			IssueProjectID int64             `json:"issueProjectId"`
			IssueIID       int64             `json:"issueIid"`
			Dependencies   []domain.IssueRef `json:"dependencies"`
		}
		if !decode(w, r, &req) {
			return
		}
		p, err := a.s.Project(r.Context(), req.ProjectID)
		if err != nil {
			failure(w, err)
			return
		}
		registered := false
		for _, repo := range p.Repositories {
			registered = registered || repo.ProjectID == req.IssueProjectID
		}
		for i := range req.Dependencies {
			if req.Dependencies[i].Provider == "" {
				req.Dependencies[i].Provider = p.HostingProvider()
			}
		}
		providerMatched := true
		for _, dependency := range req.Dependencies {
			providerMatched = providerMatched && dependency.HostingProvider() == p.HostingProvider()
		}
		if !p.Enabled || !registered || req.IssueIID < 1 || !providerMatched || !validDependencies(req.Dependencies, domain.IssueRef{Provider: p.HostingProvider(), ProjectID: req.IssueProjectID, IID: req.IssueIID}) {
			problem(w, 400, "enabled registered issue project and valid dependencies required")
			return
		}
		if !intake.ReadyForTasks(r.Context(), p, a.cfg.Initializer) {
			problem(w, 409, "Meta 仓库尚未初始化完成，暂不能接单。")
			return
		}
		client, err := a.hostClient(p)
		if err != nil {
			problem(w, 503, "GitLab credential unavailable")
			return
		}
		issue, err := client.GetIssue(r.Context(), req.IssueProjectID, req.IssueIID)
		if err != nil {
			problem(w, 502, "unable to read GitLab issue")
			return
		}
		eligible := false
		for _, label := range issue.Labels {
			eligible = eligible || label == "autonomous"
		}
		if issue.State != "opened" || !eligible || issue.ProjectID != req.IssueProjectID || issue.IID != req.IssueIID {
			problem(w, 400, "issue must be opened and labeled autonomous")
			return
		}
		enabled, err := a.s.EnabledAgents(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		pending, err := a.s.PendingIssueRunCommands(r.Context(), p.GitLabURL, issue)
		if err != nil {
			failure(w, err)
			return
		}
		scheduled, err := intake.Schedule(r.Context(), a.s, p, issue, enabled, intake.Options{DataDir: a.cfg.DataDir, RequireNode: a.cfg.RequireNode, Dependencies: req.Dependencies, ClaimRunCommands: pending})
		var agentError *intake.AgentSelectionError
		if errors.As(err, &agentError) {
			problem(w, 400, err.Error())
			return
		}
		if errors.Is(err, store.ErrNoNode) || errors.Is(err, store.ErrAdmissionQueueFull) {
			problem(w, 503, err.Error())
			return
		}
		if err != nil {
			failure(w, err)
			return
		}
		a.recordScheduledTask(scheduled)
		respond(w, 202, scheduled)
	default:
		problem(w, 405, "method not allowed")
	}
}

func (a *server) recordScheduledTask(task domain.Task) {
	slog.Info("task scheduled", "task_id", task.ID, "project_id", task.Project.ID, "provider", task.Project.HostingProvider(), "repository_id", task.Issue.ProjectID, "issue_iid", task.Issue.IID, "admission_backend", task.AdmissionBackend, "admission_status", task.AdmissionStatus, "source", "manual_api")
	if a.cfg.Metrics != nil && task.AdmissionStatus == "assigned" {
		a.cfg.Metrics.Admitted(task.AdmissionBackend)
	}
}
func (a *server) task(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	t, err := a.s.Task(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	if len(tail) == 0 && r.Method == "GET" {
		respond(w, 200, t)
		return
	}
	if len(tail) != 1 {
		problem(w, 404, "API route not found")
		return
	}
	switch tail[0] {
	case "commands":
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return
		}
		var c domain.Command
		if !decode(w, r, &c) {
			return
		}
		if err = validateCommand(t, c); err != nil {
			problem(w, 409, err.Error())
			return
		}
		if c.Kind == "cancel" && t.AdmissionStatus == "waiting" {
			cancelled, cancelErr := a.s.CancelPendingTask(r.Context(), id)
			if cancelErr != nil {
				failure(w, cancelErr)
				return
			}
			if cancelled {
				respond(w, 202, map[string]bool{"accepted": true})
				return
			}
		}
		if c.Kind == "set_auto_merge" {
			err = a.s.EnqueueAutoMerge(r.Context(), id, c)
		} else if c.Kind == "merge_round" {
			err = a.s.EnqueueManualMerge(r.Context(), id, c)
		} else {
			err = a.s.Enqueue(r.Context(), id, c)
		}
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 202, map[string]bool{"accepted": true})
	case "manual-acceptance":
		if t.Project.HostingProvider() != domain.ProviderGitLab {
			problem(w, 404, "API route not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			record, recordErr := a.s.ManualAcceptance(r.Context(), id)
			if errors.Is(recordErr, store.ErrNotFound) {
				respond(w, 200, store.ManualAcceptanceRecord{Checks: []store.ManualAcceptanceCheck{}, ExpectedDigest: domain.ManualMergeEvidenceDigest(t)})
				return
			}
			if recordErr != nil {
				failure(w, recordErr)
				return
			}
			record.ExpectedDigest = domain.ManualMergeEvidenceDigest(t)
			respond(w, 200, record)
		case http.MethodPut:
			var record store.ManualAcceptanceRecord
			if !decode(w, r, &record) {
				return
			}
			if recordErr := a.s.SaveManualAcceptance(r.Context(), id, record); recordErr != nil {
				failure(w, recordErr)
				return
			}
			respond(w, 200, record)
		default:
			problem(w, 405, "method not allowed")
		}
	case "events":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		after, err := afterID(r)
		if err != nil {
			problem(w, 400, "invalid event cursor")
			return
		}
		events, err := a.s.Events(r.Context(), id, after)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, events)
	case "stream":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		a.stream(w, r, id)
	case "artifacts":
		if r.Method != "GET" {
			problem(w, 405, "method not allowed")
			return
		}
		a.artifact(w, r, id)
	default:
		problem(w, 404, "API route not found")
	}
}
func validateCommand(t domain.Task, c domain.Command) error {
	if !domain.IDPattern.MatchString(c.ID) {
		return errors.New("valid command id required")
	}
	if domain.Terminal(t.Stage) && c.Kind != "note" && c.Kind != "merge_round" {
		return errors.New("task is terminal")
	}
	switch c.Kind {
	case "note":
		if strings.TrimSpace(c.Text) == "" {
			return errors.New("note text required")
		}
	case "reject":
		if t.Stage != "waiting_approval" || strings.TrimSpace(c.Text) == "" {
			return errors.New("reject requires waiting_approval and text")
		}
	case "approve":
		if t.Paused || t.Stage != "waiting_approval" || t.ReviewDigest == "" || c.Digest != t.ReviewDigest || c.Digest != domain.Digest(t.Workspace.Revisions) {
			return errors.New("approval requires the current reviewed full commit digest")
		}
	case "retry":
		if t.Stage != "blocked" {
			return errors.New("retry requires blocked task")
		}
	case "continue_committed":
		if !t.Paused || t.Stage != "developing" || strings.TrimSpace(c.Text) == "" || t.Workspace.Root == "" {
			return errors.New("continuing committed work requires a paused development task and reason")
		}
		if ex := t.Executions["dev"]; ex.Status == "starting" || ex.Status == "running" {
			return errors.New("development Runner must stop before continuing committed work")
		}
		changed := false
		for _, r := range t.Workspace.Revisions {
			changed = changed || r.Changed
		}
		if !changed {
			return errors.New("no committed repository change is recorded")
		}
	case "set_auto_merge":
		if c.AutoMerge == nil {
			return errors.New("autoMerge value required")
		}
		if t.Stage == "merging" || t.Stage == "partial_merge" || t.Stage == "finalizing" {
			return errors.New("automatic merge cannot change after merging starts")
		}
		for _, revision := range t.Workspace.Revisions {
			if revision.Merged {
				return errors.New("automatic merge cannot change after a repository is merged")
			}
		}
		if t.Executions["merge"].ID != "" {
			return errors.New("automatic merge cannot change after merge execution starts")
		}
		if *c.AutoMerge && t.AcceptanceIncomplete {
			return errors.New("automatic merge requires complete acceptance evidence")
		}
	case "merge_round":
		if t.Project.HostingProvider() != domain.ProviderGitLab {
			return errors.New("manual merge requires a GitLab task")
		}
	case "pause":
		if t.Paused {
			return errors.New("task already paused")
		}
	case "resume":
		if !t.Paused {
			return errors.New("task is not paused")
		}
	case "cancel":
	case "dependencies":
		for _, d := range c.Dependencies {
			if d.Provider != "" && d.HostingProvider() != t.Project.HostingProvider() {
				return errors.New("dependency platform differs from task platform")
			}
		}
		if !validDependencies(c.Dependencies, domain.IssueRef{Provider: t.Project.HostingProvider(), ProjectID: t.Issue.ProjectID, IID: t.Issue.IID}) {
			return errors.New("invalid dependencies")
		}
	default:
		return errors.New("unsupported command")
	}
	return nil
}
func afterID(r *http.Request) (int64, error) {
	value := r.URL.Query().Get("after")
	if value == "" {
		value = r.Header.Get("Last-Event-ID")
	}
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 0 {
		return 0, errors.New("invalid cursor")
	}
	return id, nil
}
func (a *server) stream(w http.ResponseWriter, r *http.Request, id string) {
	after, err := afterID(r)
	if err != nil {
		problem(w, 400, "invalid event cursor")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, 500, "stream unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		t, err := a.s.Task(r.Context(), id)
		if err != nil {
			return
		}
		events, err := a.s.Events(r.Context(), id, after)
		if err != nil {
			return
		}
		if len(events) > 0 {
			after = events[len(events)-1].ID
		}
		b, err := json.Marshal(map[string]any{"task": t, "events": events})
		if err != nil {
			return
		}
		if _, err = fmt.Fprintf(w, "id: %d\nevent: update\ndata: %s\n\n", after, b); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !a.authenticated(r) {
				return
			}
		}
	}
}
func safeArtifact(dataDir, taskID, relative string) (string, error) {
	if !domain.IDPattern.MatchString(taskID) || relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, "\\") {
		return "", errors.New("invalid artifact path")
	}
	base := filepath.Join(dataDir, "runs", taskID)
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid artifact path")
	}
	baseInfo, err := os.Lstat(base)
	if err != nil || !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid task artifact directory")
	}
	baseResolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	path := filepath.Join(baseResolved, clean)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("artifact escapes task directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}
	return resolved, nil
}
func (a *server) artifact(w http.ResponseWriter, r *http.Request, id string) {
	if a.s != nil {
		if t, e := a.s.Task(r.Context(), id); e == nil && t.AdmissionBackend == "kubernetes" {
			a.kubernetesArtifact(w, r, t)
			return
		}
	}
	if a.s != nil && a.cfg.Nodes != nil {
		if t, e := a.s.Task(r.Context(), id); e == nil && t.NodeID != "" {
			n, e := a.cfg.Nodes.Node(r.Context(), t.NodeID)
			if e != nil {
				problem(w, 502, "execution node unavailable")
				return
			}
			relative := r.URL.Query().Get("path")
			body, e := n.ReadReport(r.Context(), id, relative)
			if e != nil {
				problem(w, 404, "artifact not found")
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(relative)))
			http.ServeContent(w, r, filepath.Base(relative), time.Time{}, bytes.NewReader(body))
			return
		}
	}
	path, err := safeArtifact(a.cfg.DataDir, id, r.URL.Query().Get("path"))
	if err != nil {
		problem(w, 404, "artifact not found")
		return
	}
	// Root.Open resolves symlinks beneath the task directory at open time,
	// preventing a check/open race from escaping into another filesystem path.
	root, err := os.OpenRoot(filepath.Join(a.cfg.DataDir, "runs", id))
	if err != nil {
		problem(w, 404, "artifact not found")
		return
	}
	defer root.Close()
	f, err := root.Open(r.URL.Query().Get("path"))
	if err != nil {
		problem(w, 404, "artifact not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		problem(w, 404, "artifact not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

func (a *server) kubernetesArtifact(w http.ResponseWriter, r *http.Request, task domain.Task) {
	if a.cfg.Artifacts == nil {
		problem(w, 503, "artifact store unavailable")
		return
	}
	name, kind := r.URL.Query().Get("path"), r.URL.Query().Get("kind")
	if kind == "" {
		kind = string(artifacts.Report)
	}
	for _, ex := range task.Executions {
		for _, candidate := range ex.Artifacts {
			if candidate.TaskID != task.ID || candidate.ExecutionID != ex.ID || candidate.Name != name || candidate.Kind != kind {
				continue
			}
			ref := artifacts.Ref{TaskID: candidate.TaskID, ExecutionID: candidate.ExecutionID, Kind: artifacts.Kind(candidate.Kind), Name: candidate.Name, SHA256: candidate.SHA256, Size: candidate.Size}
			body, err := a.cfg.Artifacts.Read(r.Context(), ref)
			if err != nil {
				problem(w, 404, "artifact not found")
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(name)))
			http.ServeContent(w, r, filepath.Base(name), time.Time{}, bytes.NewReader(body))
			return
		}
	}
	problem(w, 404, "artifact not found")
}
func (a *server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		problem(w, 405, "method not allowed")
		return
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		problem(w, 404, "API route not found")
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

func repositoryURLs(repos []domain.Repository) []string {
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		out = append(out, repo.URL)
	}
	return out
}
