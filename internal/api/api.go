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
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarmjet/internal/credentials"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/imagebuild"
	"swarmjet/internal/secretbox"
	"swarmjet/internal/store"
)

type Config struct {
	MihomoURL, MihomoSecret                               string
	Username, Password, AllowedOrigin, StaticDir, DataDir string
	DefaultAgentImage                                     string
	BuilderURL                                            string
	RuntimeURL, RuntimeSecret                             string
	NodeSecret                                            string
	RequireNode                                           bool
	Nodes                                                 execution.NodeResolver
	SecureCookies                                         bool
	ResolveCredential                                     func(string) (string, error)
	// Box encrypts saved connection tokens. nil disables connection storage
	// while deployment environment credentials keep working.
	Box *secretbox.Box
	// Initializer runs the durable managed-Meta initialization. nil disables the
	// initialization endpoints.
	Initializer Initializer
}
type server struct {
	s        *store.Store
	conns    connectionStore
	cfg      Config
	mu       sync.Mutex
	attempts map[string]attempt
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
	if strings.HasPrefix(r.URL.Path, "/api/mihomo/") {
		a.mihomo(w, r)
		return
	}
	if r.URL.Path == "/api/registry" {
		a.registry(w, r)
		return
	}
	if r.URL.Path == "/api/registry/images" || strings.HasPrefix(r.URL.Path, "/api/registry/images/") {
		a.registryImages(w, r)
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
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
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
	if p.Version == 0 && p.AcceptanceStrategy == "" {
		p.AcceptanceStrategy = "repository"
	}

	// A saved connection is bound to the GitLab address it was verified against.
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
		if p.GitLabURL == "" {
			p.GitLabURL = connection.GitLabURL
		}
		if domain.GitLabIdentity(connection.GitLabURL) != domain.GitLabIdentity(p.GitLabURL) {
			problem(w, 400, "项目 GitLab 地址与所选连接不一致，已拒绝保存。")
			return
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

	p, err := a.s.SaveProject(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
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
	seen := map[domain.IssueRef]bool{}
	if len(refs) > 100 {
		return false
	}
	for _, ref := range refs {
		if ref.ProjectID < 1 || ref.IID < 1 || ref == self || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}
func (a *server) tasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		tasks, err := a.s.Tasks(r.Context())
		if err != nil {
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
		if !p.Enabled || !registered || req.IssueIID < 1 || !validDependencies(req.Dependencies, domain.IssueRef{ProjectID: req.IssueProjectID, IID: req.IssueIID}) {
			problem(w, 400, "enabled registered issue project and valid dependencies required")
			return
		}
		if p.ManagedMeta() && (a.cfg.Initializer == nil || !a.cfg.Initializer.ReadyForTasks(r.Context(), p.ID)) {
			problem(w, 409, "Meta 仓库尚未初始化完成，暂不能接单。")
			return
		}
		client, err := a.client(p)
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
		p, err = domain.ResolveIssueAgent(p, issue, enabled)
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		if !a.cfg.RequireNode && p.Nodes.Mode == "" {
			p, err = imagebuild.ApplyToProject(a.cfg.DataDir, p)
			if err != nil {
				failure(w, err)
				return
			}
		}
		id, err := newID()
		if err != nil {
			failure(w, err)
			return
		}
		now := time.Now().UTC()
		t := domain.Task{ID: id, Project: p, Issue: issue, Dependencies: req.Dependencies, Stage: "queued", Executions: map[string]domain.Execution{}, Verification: []domain.Verification{}, Assumptions: []string{}, Notes: []string{}, CreatedAt: now, UpdatedAt: now}
		scheduled, err := a.s.CreateTaskScheduled(r.Context(), t, a.cfg.RequireNode)
		if errors.Is(err, store.ErrNoNode) {
			problem(w, 503, err.Error())
			return
		}
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 202, scheduled)
	default:
		problem(w, 405, "method not allowed")
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
		if err = a.s.Enqueue(r.Context(), id, c); err != nil {
			failure(w, err)
			return
		}
		respond(w, 202, map[string]bool{"accepted": true})
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
	if domain.Terminal(t.Stage) && c.Kind != "note" {
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
		if !validDependencies(c.Dependencies, domain.IssueRef{ProjectID: t.Issue.ProjectID, IID: t.Issue.IID}) {
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
