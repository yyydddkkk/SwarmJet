package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/domain"
	"swarmjet/internal/store"
)

func TestProjectAuthorizationAcrossAPIRoutes(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	root, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "api_access_" + strings.ReplaceAll(id, "-", "")
	if _, err = root.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer root.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	config := root.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &store.Store{Pool: pool}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	userID, _ := newID()
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,status) VALUES($1,'viewer@example.test','Viewer','hash','active')`, userID); err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []string{"alpha", "beta"} {
		body, _ := json.Marshal(domain.Project{ID: projectID, Name: projectID})
		if _, err = pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES($1,1,$2)`, projectID, body); err != nil {
			t.Fatal(err)
		}
		taskID := "task-" + projectID
		taskBody, _ := json.Marshal(domain.Task{ID: taskID, Project: domain.Project{ID: projectID}, Stage: "queued"})
		if _, err = pool.Exec(ctx, `INSERT INTO tasks(id,project_id,source,stage,body) VALUES($1,$2,$3,'queued',$4)`, taskID, projectID, taskID, taskBody); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.SetProjectGrant(ctx, "alpha", userID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if err = s.AddUserSession(ctx, tokenHash(token), userID); err != nil {
		t.Fatal(err)
	}
	h := New(s, Config{Username: "admin", Password: "secret", AllowedOrigin: "https://console.test"})
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://console.test"+path, nil)
		r.AddCookie(&http.Cookie{Name: "swarmjet_session", Value: token})
		if method != http.MethodGet {
			r.Header.Set("Origin", "https://console.test")
			r.Header.Set("X-Requested-With", "swarmjet")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/projects"); w.Code != 200 || strings.Contains(w.Body.String(), `"id":"beta"`) || !strings.Contains(w.Body.String(), `"myRole":"viewer"`) {
		t.Fatalf("project list: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/tasks"); w.Code != 200 || strings.Contains(w.Body.String(), "task-beta") || !strings.Contains(w.Body.String(), "task-alpha") {
		t.Fatalf("task list: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/projects/beta", "/api/tasks/task-beta", "/api/tasks/task-beta/events"} {
		if w := call("GET", path); w.Code != 404 {
			t.Fatalf("%s leaked: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := call("PUT", "/api/projects/alpha"); w.Code != 404 {
		t.Fatalf("viewer project write: %d", w.Code)
	}
	if w := call("GET", "/api/system"); w.Code != 403 {
		t.Fatalf("viewer system access: %d", w.Code)
	}
	teamID, _ := newID()
	if err = s.CreateTeam(ctx, teamID, "Team"); err != nil {
		t.Fatal(err)
	}
	if err = s.AddTeamMember(ctx, teamID, userID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetTeamProjectGrant(ctx, teamID, "beta", store.RoleOperator); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/projects/beta"); w.Code != 200 || !strings.Contains(w.Body.String(), `"myRole":"operator"`) {
		t.Fatalf("team grant: %d %s", w.Code, w.Body.String())
	}
	if err = s.DeleteTeamMember(ctx, teamID, userID); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/projects/beta"); w.Code != 404 {
		t.Fatalf("team revocation: %d", w.Code)
	}
}
