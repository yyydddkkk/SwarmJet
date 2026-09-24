package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/domain"
	"swarmjet/internal/store"
)

func TestCSRF(t *testing.T) {
	for _, tc := range []struct {
		origin, header string
		ok             bool
	}{{"http://console.local", "swarmjet", true}, {"", "swarmjet", false}, {"https://evil.test", "swarmjet", false}, {"http://console.local", "", false}, {"http://console.local/", "swarmjet", false}} {
		r := httptest.NewRequest("POST", "http://console.local/api/login", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Requested-With", tc.header)
		if csrfAllowed(r, "") != tc.ok {
			t.Fatalf("%+v", tc)
		}
	}
}
func TestPrivateAndLoginRoutes(t *testing.T) {
	h := New(nil, Config{Username: "admin", Password: "secret"})
	for _, path := range []string{"/api/projects", "/api/project-defaults", "/api/tasks", "/api/session", "/api/unknown"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"secret"}`)))
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestLoginRateLimitAndTimingSafeComparison(t *testing.T) {
	s := &server{attempts: map[string]attempt{}}
	for i := 0; i < 10; i++ {
		if !s.loginAllowed("127.0.0.1:123") {
			t.Fatal(i)
		}
	}
	if s.loginAllowed("127.0.0.1:456") {
		t.Fatal("rate limit not applied by IP")
	}
	if !secureEqual("secret", "secret") || secureEqual("secret", "wrong") {
		t.Fatal("comparison")
	}
	token, err := opaqueToken()
	if err != nil || len(token) != 64 || token == tokenHash(token) {
		t.Fatal("token")
	}
}
func TestCommandGuards(t *testing.T) {
	task := domain.Task{Stage: "waiting_approval", Workspace: domain.Workspace{Revisions: []domain.Revision{{RepositoryID: "r", BaseSHA: "a", HeadSHA: "b", TargetBranch: "main"}}}}
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	if err := validateCommand(task, domain.Command{ID: "approve1", Kind: "approve", Digest: task.ReviewDigest}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []domain.Command{{ID: "x", Kind: "approve", Digest: "old"}, {ID: "x", Kind: "retry"}, {ID: "x", Kind: "reject"}, {ID: "x", Kind: "unknown"}} {
		if validateCommand(task, c) == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	task.Stage = "completed"
	if validateCommand(task, domain.Command{ID: "x", Kind: "cancel"}) == nil {
		t.Fatal("cancel completed")
	}
	if err := validateCommand(task, domain.Command{ID: "x", Kind: "note", Text: "additional context"}); err != nil {
		t.Fatal(err)
	}
}

func TestContinueCommittedRequiresPausedDevelopmentAndReason(t *testing.T) {
	task := domain.Task{Stage: "developing", Workspace: domain.Workspace{Root: "/node/task", Revisions: []domain.Revision{{RepositoryID: "app", Changed: true}}}}
	command := domain.Command{ID: "continue-1", Kind: "continue_committed", Text: "Committed work is ready for publishing"}
	if validateCommand(task, command) == nil {
		t.Fatal("live development can be skipped without first stopping its Runner")
	}
	task.Paused = true
	if err := validateCommand(task, command); err != nil {
		t.Fatal(err)
	}
	command.Text = " "
	if validateCommand(task, command) == nil {
		t.Fatal("operator continuation lacks a reason")
	}
	command.Text = "ready"
	task.Stage = "reviewing"
	if validateCommand(task, command) == nil {
		t.Fatal("continuation accepted outside development")
	}
}

func TestTaskAutoMergeSwitchClosesAtMergeExecution(t *testing.T) {
	task := domain.Task{Stage: "reviewing"}
	off := false
	cmd := domain.Command{ID: "toggle", Kind: "set_auto_merge", AutoMerge: &off}
	if err := validateCommand(task, cmd); err != nil {
		t.Fatal(err)
	}
	cmd.AutoMerge = nil
	if validateCommand(task, cmd) == nil {
		t.Fatal("missing switch value accepted")
	}
	cmd.AutoMerge = &off
	for _, stage := range []string{"merging", "partial_merge", "finalizing", "completed"} {
		task.Stage = stage
		if validateCommand(task, cmd) == nil {
			t.Fatalf("switch accepted after merge started: %s", stage)
		}
	}
	task.Stage = "blocked"
	task.Workspace.Revisions = []domain.Revision{{Merged: true}}
	if validateCommand(task, cmd) == nil {
		t.Fatal("merged repository allowed switch while task blocked")
	}
}
func TestArtifactConfinement(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs", "task")
	os.MkdirAll(base, 0700)
	os.WriteFile(filepath.Join(base, "log.txt"), []byte("log"), 0600)
	outside := filepath.Join(dir, "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(base, "escape"))
	for _, path := range []string{"../secret", "/etc/passwd", "escape", "", "."} {
		if _, err := safeArtifact(dir, "task", path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if _, err := safeArtifact(dir, "task", "log.txt"); err != nil {
		t.Fatal(err)
	}
	os.Symlink(dir, filepath.Join(dir, "runs", "evil"))
	if _, err := safeArtifact(dir, "evil", "secret"); err == nil {
		t.Fatal("symlink task")
	}
}
func TestArtifactHTMLAttachment(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "runs", "task")
	os.MkdirAll(base, 0700)
	os.WriteFile(filepath.Join(base, "report.html"), []byte("<script>alert(1)</script>"), 0600)
	s := server{cfg: Config{DataDir: dir}}
	w := httptest.NewRecorder()
	s.artifact(w, httptest.NewRequest("GET", "/api/tasks/task/artifacts?path=report.html", nil), "task")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(w.Result())
	}
}
func TestStaticDoesNotFallbackAPI(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("SPA"), 0600)
	h := New(nil, Config{StaticDir: dir})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/tasks/123", nil))
	if w.Code != 200 || w.Body.String() != "SPA" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api", nil))
	if w.Code != 404 || strings.Contains(w.Body.String(), "SPA") {
		t.Fatal(w.Code)
	}
}
func TestMetaCheckReadOnly(t *testing.T) {
	methods := []string{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Header.Get("PRIVATE-TOKEN") != "token" {
			t.Error("missing token")
		}
		if strings.Contains(r.URL.Path, ".meta.yaml") {
			_, _ = w.Write([]byte("projects:\n  service: https://git/service.git\n"))
		} else if strings.Contains(r.URL.Path, "compose.yaml") {
			_, _ = w.Write([]byte("services:\n  test: {image: alpine}\n"))
		} else {
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	p := domain.Project{GitLabURL: remote.URL, MetaURL: remote.URL + "/group/meta.git", MetaRef: "main", Repositories: []domain.Repository{{ID: "service", Path: "service", URL: "https://git/service.git"}}, Integration: domain.IntegrationConfig{ComposeFile: "compose.yaml", Checks: []domain.Check{{Service: "test", Name: "test"}}}}
	if err := checkMeta(context.Background(), p, "token"); err != nil {
		t.Fatal(err)
	}
	bad := p
	bad.Repositories = []domain.Repository{{ID: "service", Path: "service", URL: "https://git/other.git"}}
	if err := checkMeta(context.Background(), bad, "token"); err == nil {
		t.Fatal("mismatched Meta manifest accepted")
	}
	for _, method := range methods {
		if method != "GET" {
			t.Fatal("mutation")
		}
	}
}
func TestLoginPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	schemaID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "api_test_" + strings.ReplaceAll(schemaID, "-", "")
	if _, err = s.Pool.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer s.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	poolConfig := s.Pool.Config()
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	isolatedPool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer isolatedPool.Close()
	isolated := &store.Store{Pool: isolatedPool}
	if err = isolated.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = isolated.SetEnabledAgents(context.Background(), []string{"opencode", "codex"}); err != nil {
		t.Fatal(err)
	}

	h := New(isolated, Config{Username: "admin", Password: "secret", AllowedOrigin: "https://console.test", SecureCookies: true})
	r := httptest.NewRequest("POST", "https://console.test/api/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	r.Header.Set("Origin", "https://console.test")
	r.Header.Set("X-Requested-With", "swarmjet")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal(cookies)
	}
	defer isolated.DeleteSession(context.Background(), tokenHash(cookies[0].Value))
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("password leaked")
	}
	r = httptest.NewRequest("GET", "https://console.test/api/session", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	// Exercise real authenticated persistence using only a local GitLab fixture.
	t.Setenv("GITLAB_TOKEN", "fixture-token")
	manifestMismatch := false
	var remote *httptest.Server
	remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected remote mutation")
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("PRIVATE-TOKEN") != "fixture-token" {
			t.Error("credential not sent to fixture")
		}
		if r.URL.Path == "/api/v4/projects/1/issues/1" {
			respond(w, 200, map[string]any{"project_id": 1, "iid": 1, "title": "Implement task", "state": "opened", "labels": []string{"autonomous"}})
			return
		}
		if r.URL.Path == "/api/v4/projects/1/issues/2" {
			respond(w, 200, map[string]any{"project_id": 1, "iid": 2, "title": "Manual issue", "state": "opened", "labels": []string{}})
			return
		}
		if strings.Contains(r.URL.Path, "/repository/files/.meta.yaml/raw") {
			if manifestMismatch {
				_, _ = w.Write([]byte("projects:\n  service: https://wrong.example/service.git\n"))
				return
			}
			_, _ = w.Write([]byte("projects:\n  service: " + remote.URL + "/group/service.git\n"))
			return
		}
		w.WriteHeader(404)
	}))
	defer remote.Close()
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "https://console.test"+path, bytes.NewReader(data))
		req.AddCookie(cookies[0])
		if method != "GET" {
			req.Header.Set("Origin", "https://console.test")
			req.Header.Set("X-Requested-With", "swarmjet")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	project := domain.Project{ID: "p", Name: "Project", Enabled: true, GitLabURL: remote.URL, MetaURL: remote.URL + "/group/meta.git", MetaRef: "main", CredentialRef: "GITLAB_TOKEN", MergeCredentialRef: "GITLAB_MERGE_TOKEN", Repositories: []domain.Repository{{ID: "service", ProjectID: 1, URL: remote.URL + "/group/service.git", Path: "service", TargetBranch: "main"}}, Development: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Review: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Integration: domain.IntegrationConfig{ComposeFile: "compose.yml", Checks: []domain.Check{{Name: "test", Service: "test", Command: []string{"true"}, Required: true}}}, MaxReworks: 3}
	defaultsResponse := call("GET", "/api/project-defaults", nil)
	var defaults struct {
		AgentImage     string `json:"agentImage"`
		Adapter        string `json:"adapter"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
		MaxReworks     int    `json:"maxReworks"`
	}
	if defaultsResponse.Code != 200 || json.Unmarshal(defaultsResponse.Body.Bytes(), &defaults) != nil || defaults.AgentImage != "swarmjet-agent:local" || defaults.Adapter != "opencode" || defaults.TimeoutSeconds != 5400 || defaults.MaxReworks != 3 {
		t.Fatalf("unexpected defaults: %d %s", defaultsResponse.Code, defaultsResponse.Body)
	}
	configured := New(isolated, Config{DefaultAgentImage: "registry.example/agent:configured"})
	defaultsRequest := httptest.NewRequest("GET", "https://console.test/api/project-defaults", nil)
	defaultsRequest.AddCookie(cookies[0])
	configuredDefaults := httptest.NewRecorder()
	configured.ServeHTTP(configuredDefaults, defaultsRequest)
	if configuredDefaults.Code != 200 || json.Unmarshal(configuredDefaults.Body.Bytes(), &defaults) != nil || defaults.AgentImage != "registry.example/agent:configured" {
		t.Fatalf("configured image ignored: %d %s", configuredDefaults.Code, configuredDefaults.Body)
	}
	draft := domain.Project{ID: "draft", Name: "Meta draft", MetaURL: remote.URL + "/group/draft-meta.git"}
	draftResponse := call("POST", "/api/projects", draft)
	if draftResponse.Code != 200 {
		t.Fatal("draft rejected", draftResponse.Code, draftResponse.Body)
	}
	if err = json.Unmarshal(draftResponse.Body.Bytes(), &draft); err != nil || draft.Version != 1 || draft.Enabled {
		t.Fatal("invalid persisted draft", err, draft)
	}
	incomplete := draft
	incomplete.Enabled = true
	if rejected := call("PUT", "/api/projects/draft", incomplete); rejected.Code != 400 {
		t.Fatal("incomplete draft enabled", rejected.Code, rejected.Body)
	}
	stillDraft, err := isolated.Project(context.Background(), draft.ID)
	if err != nil || stillDraft.Version != 1 || stillDraft.Enabled {
		t.Fatal("failed activation mutated draft", err, stillDraft)
	}
	unsafeDraft := draft
	unsafeDraft.Integration.ComposeFile = "../escape.yml"
	if rejected := call("PUT", "/api/projects/draft", unsafeDraft); rejected.Code != 400 {
		t.Fatal("unsafe draft accepted", rejected.Code, rejected.Body)
	}
	activeDraft := project
	activeDraft.ID = draft.ID
	activeDraft.Version = draft.Version
	activeDraft.MetaURL = draft.MetaURL
	activeDraft.Repositories = append([]domain.Repository(nil), project.Repositories...)
	activeDraft.Repositories[0].ProjectID = 22
	manifestMismatch = true
	if rejected := call("PUT", "/api/projects/draft", activeDraft); rejected.Code != 409 || !strings.Contains(rejected.Body.String(), "Meta-Repo") {
		t.Fatal("mismatched Meta manifest enabled", rejected.Code, rejected.Body)
	}
	manifestMismatch = false
	activatedResponse := call("PUT", "/api/projects/draft", activeDraft)
	if activatedResponse.Code != 200 {
		t.Fatal("complete draft activation failed", activatedResponse.Code, activatedResponse.Body)
	}
	var activated domain.Project
	if err = json.Unmarshal(activatedResponse.Body.Bytes(), &activated); err != nil || !activated.Enabled || activated.Version != 2 {
		t.Fatal("invalid activated draft", err, activated)
	}
	if err = domain.ValidateProject(activated); err != nil {
		t.Fatal("activated snapshot not executable", err)
	}
	created := call("POST", "/api/projects", project)
	if created.Code != 200 {
		t.Fatal(created.Code, created.Body)
	}
	if err = json.Unmarshal(created.Body.Bytes(), &project); err != nil || project.Version != 1 {
		t.Fatal(err, project.Version)
	}
	stale := project
	project.Name = "Updated"
	updated := call("PUT", "/api/projects/p", project)
	if updated.Code != 200 {
		t.Fatal(updated.Code, updated.Body)
	}
	if conflict := call("PUT", "/api/projects/p", stale); conflict.Code != 409 {
		t.Fatal("stale version", conflict.Code)
	}
	if listed := call("GET", "/api/projects", nil); listed.Code != 200 || !strings.Contains(listed.Body.String(), "Updated") {
		t.Fatal(listed.Code, listed.Body)
	}
	if rejected := call("POST", "/api/tasks", map[string]any{"projectId": "p", "issueProjectId": 1, "issueIid": 2}); rejected.Code != 400 {
		t.Fatal("non-autonomous issue accepted", rejected.Code)
	}
	if rejected := call("POST", "/api/tasks", map[string]any{"projectId": "p", "issueProjectId": 2, "issueIid": 1}); rejected.Code != 400 {
		t.Fatal("unregistered issue project accepted", rejected.Code)
	}
	taskResponse := call("POST", "/api/tasks", map[string]any{"projectId": "p", "issueProjectId": 1, "issueIid": 1})
	if taskResponse.Code != 202 {
		t.Fatal(taskResponse.Code, taskResponse.Body)
	}
	var task domain.Task
	if err = json.Unmarshal(taskResponse.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Stage != "queued" || task.Project.Version != 2 || task.Round != 1 {
		t.Fatalf("task not frozen %+v", task)
	}
	pageResponse := call("GET", "/api/tasks?limit=1", nil)
	var page store.TaskPage
	if pageResponse.Code != 200 || json.Unmarshal(pageResponse.Body.Bytes(), &page) != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != task.ID {
		t.Fatalf("task page status=%d body=%s", pageResponse.Code, pageResponse.Body.String())
	}
	if duplicate := call("POST", "/api/tasks", map[string]any{"projectId": "p", "issueProjectId": 1, "issueIid": 1}); duplicate.Code != 409 {
		t.Fatal("duplicate issue task accepted", duplicate.Code)
	}
	if note := call("POST", "/api/tasks/"+task.ID+"/commands", domain.Command{ID: "note", Kind: "note", Text: "clarification while runnable"}); note.Code != 202 {
		t.Fatal(note.Code, note.Body)
	}
	pending, err := isolated.Pending(context.Background())
	if err != nil || len(pending) != 2 {
		t.Fatal("missing task/command outbox", len(pending), err)
	}
	task.Stage = "waiting_approval"
	task.Workspace.Revisions = []domain.Revision{{RepositoryID: "service", BaseSHA: "a", HeadSHA: "b", TargetBranch: "main", Changed: true}}
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	if err = isolated.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if wrong := call("POST", "/api/tasks/"+task.ID+"/commands", domain.Command{ID: "wrong", Kind: "approve", Digest: "stale"}); wrong.Code != 409 {
		t.Fatal("stale approval accepted", wrong.Code)
	}
	if approved := call("POST", "/api/tasks/"+task.ID+"/commands", domain.Command{ID: "approved", Kind: "approve", Digest: task.ReviewDigest}); approved.Code != 202 {
		t.Fatal(approved.Code, approved.Body)
	}
	csrf := httptest.NewRequest("POST", "https://console.test/api/logout", nil)
	csrf.AddCookie(cookies[0])
	csrf.Header.Set("Origin", "https://evil.test")
	csrf.Header.Set("X-Requested-With", "swarmjet")
	denied := httptest.NewRecorder()
	h.ServeHTTP(denied, csrf)
	if denied.Code != 403 {
		t.Fatal("authenticated CSRF accepted", denied.Code)
	}
	if logout := call("POST", "/api/logout", nil); logout.Code != 204 {
		t.Fatal(logout.Code)
	}
	if after := call("GET", "/api/session", nil); after.Code != 401 {
		t.Fatal("logout did not revoke session", after.Code)
	}

}
