package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/service"
	"swarmjet/internal/store"
)

func TestWorkbenchNoteSubmission(t *testing.T) {
	var posted []string
	fail := false
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/projects/7/issues/1/notes" {
			t.Errorf("unexpected GitLab request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var note struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&note); err != nil {
			t.Error(err)
		}
		posted = append(posted, note.Body)
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"message":"unavailable"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": len(posted), "body": note.Body})
	}))
	defer gitlabServer.Close()
	a := &server{}
	scope := workbenchRepository{Repo: domain.Repository{ProjectID: 7}}
	client := gitlab.New(gitlabServer.URL, "personal")
	submit := func(kind, body string, start bool) *httptest.ResponseRecorder {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"body": body, "startNextRound": start})
		w := httptest.NewRecorder()
		a.workbenchAction(w, httptest.NewRequest(http.MethodPost, "/notes", bytes.NewReader(payload)), kind, scope, client, 1, []string{"notes"})
		return w
	}
	plain := "普通评论\n第二行"
	if w := submit("issues", plain, false); w.Code != http.StatusCreated {
		t.Fatalf("ordinary comment: %d %s", w.Code, w.Body.String())
	}
	requirement := "修复反引号 `code` 与特殊空格\n保留第二行\n\n第三段"
	if w := submit("issues", requirement, true); w.Code != http.StatusCreated {
		t.Fatalf("run comment: %d %s", w.Code, w.Body.String())
	}
	if len(posted) != 2 || posted[0] != plain || posted[1] != "/swarmjet run "+requirement {
		t.Fatalf("posted bodies = %#v", posted)
	}
	if w := submit("issues", " \n\t", true); w.Code != http.StatusBadRequest || len(posted) != 2 {
		t.Fatalf("blank requirement: %d %s, posted %d", w.Code, w.Body.String(), len(posted))
	}
	if w := submit("merge_requests", requirement, true); w.Code != http.StatusBadRequest || len(posted) != 2 {
		t.Fatalf("MR next round: %d %s, posted %d", w.Code, w.Body.String(), len(posted))
	}
	fail = true
	if w := submit("issues", requirement, true); w.Code != http.StatusBadGateway || len(posted) != 3 {
		t.Fatalf("upstream failure: %d %s, posted %d", w.Code, w.Body.String(), len(posted))
	}
}

func TestWorkbenchRunCommentScansIntoQueuedNextRound(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "workbench_round_" + strings.ReplaceAll(id, "-", "")
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg := admin.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &store.Store{Pool: pool}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabledAgents(ctx, []string{"opencode"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	notes := []map[string]any{}
	var postedPaths []string
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/7/issues/1/notes":
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(notes)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/7/issues/1/resource_label_events":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "action": "add", "user": map[string]any{"id": 42}, "label": map[string]any{"name": "autonomous"}, "created_at": time.Now().UTC().Add(-time.Hour)}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/projects/7/issues/1/notes":
			var request struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			postedPaths = append(postedPaths, r.URL.Path)
			created := time.Now().UTC().Add(time.Minute)
			note := map[string]any{"id": len(notes) + 81, "body": request.Body, "author": map[string]any{"id": 42}, "created_at": created, "updated_at": created}
			notes = append(notes, note)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(note)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/7/issues":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"project_id": 7, "iid": 1, "title": "task", "state": "opened", "labels": []string{"autonomous"}, "web_url": "http://example/7/1"}})
		default:
			t.Errorf("unexpected GitLab request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gitlabServer.Close()
	p := domain.Project{ID: "round", Name: "Round", Enabled: true, GitLabTriggerMode: "operator", MetaURL: gitlabServer.URL + "/meta.git", MetaRef: "main", GitLabURL: gitlabServer.URL, CredentialRef: "GITLAB_TOKEN", MergeCredentialRef: "GITLAB_MERGE_TOKEN", Repositories: []domain.Repository{{ID: "repo", ProjectID: 7, URL: gitlabServer.URL + "/repo.git", Path: "repo", TargetBranch: "main"}}, Development: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Review: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Integration: domain.IntegrationConfig{ComposeFile: "compose.yml", StartupTimeoutSeconds: 30, Checks: []domain.Check{{Name: "test", Service: "test", Command: []string{"true"}, Required: true}}}, MaxReworks: 3}
	if _, err = s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	actorID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,status) VALUES($1,'actor@example.test','Actor','hash','active')`, actorID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetProjectGrant(ctx, p.ID, actorID, store.RoleOperator); err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertExternalIdentity(ctx, actorID, "gitlab", p.GitLabURL, "42", "actor", "sealed"); err != nil {
		t.Fatal(err)
	}
	svc := &service.Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	if err = svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("first round: tasks=%d err=%v", len(tasks), err)
	}
	first := tasks[0]
	a := &server{}
	client := gitlab.New(gitlabServer.URL, "personal")
	scope := workbenchRepository{Project: p, Repo: p.Repositories[0]}
	comment := func(body string, start bool) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"body": body, "startNextRound": start})
		w := httptest.NewRecorder()
		a.workbenchAction(w, httptest.NewRequest(http.MethodPost, "/notes", bytes.NewReader(payload)), "issues", scope, client, 1, []string{"notes"})
		if w.Code != http.StatusCreated {
			t.Fatalf("comment response: %d %s", w.Code, w.Body.String())
		}
	}
	comment("普通评论", false)
	requirement := "补充 `代码` 与不间断空格\u00a0\n保留第二行"
	comment(requirement, true)
	if err = svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	var recorded int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM issue_run_commands").Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("recorded commands=%d err=%v", recorded, err)
	}
	pending, err := s.PendingIssueRunCommands(ctx, p.GitLabURL, first.Issue)
	if err != nil || !pending {
		t.Fatalf("pending command=%v err=%v", pending, err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("active round should queue requirement: tasks=%d err=%v", len(tasks), err)
	}
	first.Stage = "completed"
	if err = s.SaveTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 2 || tasks[0].Round != 2 || tasks[0].TriggerText != requirement || len(tasks[0].TriggerNoteIDs) != 1 {
		t.Fatalf("next round: tasks=%+v err=%v", tasks, err)
	}
	if err = svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("repeat scan duplicated round: tasks=%d err=%v", len(tasks), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(postedPaths) != 2 || notes[0]["body"] != "普通评论" || notes[1]["body"] != "/swarmjet run "+requirement {
		t.Fatalf("posted paths=%v notes=%v", postedPaths, notes)
	}
}
