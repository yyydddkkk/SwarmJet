package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

func TestWorkbenchRepositoryScopeExcludesOtherProjectAndProvider(t *testing.T) {
	projects := []domain.Project{
		{ID: "one", GitLabURL: "https://gitlab-a.example", Repositories: []domain.Repository{{ID: "api", ProjectID: 7}}},
		{ID: "two", GitLabURL: "https://gitlab-b.example", Repositories: []domain.Repository{{ID: "api", ProjectID: 7}}},
		{ID: "github", Provider: domain.ProviderGitHub, Repositories: []domain.Repository{{ID: "api", ProjectID: 7}}},
	}
	got := filterWorkbenchRepos(projects, "one", "api")
	if len(got) != 1 || got[0].Project.GitLabURL != "https://gitlab-a.example" {
		t.Fatalf("cross-instance scope: %+v", got)
	}
	if got := filterWorkbenchRepos(projects, "one", "other"); len(got) != 0 {
		t.Fatalf("cross-repository scope: %+v", got)
	}
}

func TestWorkbenchPageSkipsEmptyRepositoriesAndKeepsCursorOffset(t *testing.T) {
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
	schema := "workbench_test_" + strings.ReplaceAll(id, "-", "")
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
	var upstreamCalls, activeCalls, peakCalls atomic.Int64
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		active := activeCalls.Add(1)
		defer activeCalls.Add(-1)
		for peak := peakCalls.Load(); active > peak && !peakCalls.CompareAndSwap(peak, active); peak = peakCalls.Load() {
		}
		time.Sleep(30 * time.Millisecond)
		if r.Header.Get("PRIVATE-TOKEN") != "personal" {
			t.Errorf("personal identity not used")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("state") == "closed" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		var projectID, count int
		if _, e := fmt.Sscanf(r.URL.Path, "/api/v4/projects/%d/issues", &projectID); e != nil {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		switch projectID {
		case 2:
			count = 49
		case 3:
			count = 2
		}
		rows := make([]map[string]any, 0, count)
		for i := 1; i <= count; i++ {
			rows = append(rows, map[string]any{"iid": i, "title": fmt.Sprintf("Issue %d", i), "state": "opened"})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer gitlabServer.Close()
	project := domain.Project{ID: "workbench", Enabled: true, GitLabURL: gitlabServer.URL, Repositories: []domain.Repository{{ID: "empty", ProjectID: 1}, {ID: "mostly-full", ProjectID: 2}, {ID: "last", ProjectID: 3}}}
	body, _ := json.Marshal(project)
	if _, err = s.Pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES($1,1,$2)`, project.ID, body); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("personal")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGitLabIdentity(ctx, project.GitLabURL, sealed, "admin"); err != nil {
		t.Fatal(err)
	}
	a := &server{s: s, cfg: Config{Box: box}}
	request := func(cursor string) struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	} {
		t.Helper()
		path := "/api/workbench/issues?state=opened"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		a.workbenchPage(w, httptest.NewRequest(http.MethodGet, path, nil), "issues")
		if w.Code != 200 {
			t.Fatalf("page failed: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"nextCursor"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal("invalid page JSON")
		}
		return result
	}
	first := request("")
	if peakCalls.Load() < 2 {
		t.Fatalf("repository pages were fetched serially (peak %d)", peakCalls.Load())
	}
	if len(first.Items) != 50 || first.Items[0]["repositoryId"] != "mostly-full" || first.Items[49]["repositoryId"] != "last" || first.NextCursor == "" {
		t.Fatalf("first page does not cross empty repo: %d %v %q", len(first.Items), first.Items[0]["repositoryId"], first.NextCursor)
	}
	second := request(first.NextCursor)
	if len(second.Items) != 1 || second.Items[0]["repositoryId"] != "last" || second.NextCursor != "" {
		t.Fatalf("cursor skipped last Issue: %+v", second)
	}
	w := httptest.NewRecorder()
	a.workbenchPage(w, httptest.NewRequest(http.MethodGet, "/api/workbench/issues?state=closed", nil), "issues")
	var empty struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"nextCursor"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &empty) != nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty result had another page: %d %s", w.Code, w.Body.String())
	}
	cachedCalls := upstreamCalls.Load()
	_ = request("")
	if upstreamCalls.Load() != cachedCalls {
		t.Fatalf("fresh list requested upstream again: %d -> %d", cachedCalls, upstreamCalls.Load())
	}
	a.invalidateWorkbenchPages()
	_ = request("")
	if upstreamCalls.Load() <= cachedCalls {
		t.Fatal("list cache survived invalidation")
	}
}

func TestWorkbenchMissingPersonalCredentialAndSanitizedError(t *testing.T) {
	a := &server{}
	if _, err := a.workbenchClient(nil, domain.Project{}); err == nil || !strings.Contains(err.Error(), "MASTER_KEY") {
		t.Fatalf("missing identity: %v", err)
	}
	w := httptest.NewRecorder()
	workbenchError(w, &gitlab.APIError{StatusCode: 422, Reason: "branch is protected; token [redacted]"})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "branch is protected") || strings.Contains(w.Body.String(), "personal-secret") {
		t.Fatalf("error response: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchOptionsScopeAndPersonalIdentity(t *testing.T) {
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
	schema := "workbench_options_" + strings.ReplaceAll(id, "-", "")
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
	var calls atomic.Int64
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("PRIVATE-TOKEN") != "personal-secret" || !strings.HasPrefix(r.URL.Path, "/api/v4/projects/7/") {
			t.Errorf("wrong identity or project: %s", r.URL.RequestURI())
		}
		if r.URL.Query().Get("query") == "denied" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"denied personal-secret"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v4/projects/7/labels":
			fmt.Fprint(w, `[{"name":"bug","color":"#f00"}]`)
		case "/api/v4/projects/7/members/all":
			if r.URL.Query().Get("query") != "alice" || r.URL.Query().Get("state") != "active" {
				t.Errorf("member search missing: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `[{"id":2,"name":"Alice","username":"alice","avatar_url":"https://example/avatar","state":"active"}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer gitlabServer.Close()
	project := domain.Project{ID: "one", Enabled: true, GitLabURL: gitlabServer.URL, Repositories: []domain.Repository{{ID: "repo", ProjectID: 7}}}
	body, _ := json.Marshal(project)
	if _, err = s.Pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES($1,1,$2)`, project.ID, body); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("personal-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGitLabIdentity(ctx, project.GitLabURL, sealed, "admin"); err != nil {
		t.Fatal(err)
	}
	a := &server{s: s, cfg: Config{Box: box}}
	for _, kind := range []string{"issues", "merge-requests"} {
		w := httptest.NewRecorder()
		a.workbench(w, withPrincipal(httptest.NewRequest(http.MethodGet, "/api/workbench/"+kind+"/one/repo/options?search=alice", nil), store.Principal{Breakglass: true, PlatformAdmin: true}))
		if w.Code != 200 || strings.Contains(w.Body.String(), "personal-secret") {
			t.Fatalf("options %s: %d %s", kind, w.Code, w.Body.String())
		}
		var options struct {
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
			Assignees []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"assignees"`
		}
		if json.Unmarshal(w.Body.Bytes(), &options) != nil || len(options.Labels) != 1 || len(options.Assignees) != 1 || options.Assignees[0].Name != "Alice" {
			t.Fatalf("options payload: %s", w.Body.String())
		}
	}
	before := calls.Load()
	for _, path := range []string{"/api/workbench/issues/other/repo/options", "/api/workbench/issues/one/other/options"} {
		w := httptest.NewRecorder()
		a.workbench(w, withPrincipal(httptest.NewRequest(http.MethodGet, path, nil), store.Principal{Breakglass: true, PlatformAdmin: true}))
		if w.Code != 404 {
			t.Fatalf("cross scope %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if calls.Load() != before {
		t.Fatal("cross-scope request reached GitLab")
	}
	w := httptest.NewRecorder()
	a.workbench(w, withPrincipal(httptest.NewRequest(http.MethodGet, "/api/workbench/issues/one/repo/options?search=denied", nil), store.Principal{Breakglass: true, PlatformAdmin: true}))
	if w.Code != 403 || strings.Contains(w.Body.String(), "personal-secret") {
		t.Fatalf("permission/token response: %d %s", w.Code, w.Body.String())
	}
}
