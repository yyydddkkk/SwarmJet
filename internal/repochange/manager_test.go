package repochange

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/managedmeta"
)

type memoryStore struct {
	mu      sync.Mutex
	project domain.Project
	change  domain.RepositoryChange
	init    domain.Initialization
}


func (s *memoryStore) Project(context.Context, string) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.project, nil
}
func (s *memoryStore) SaveProject(_ context.Context, p domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Version++
	s.project = p
	return p, nil
}
func (s *memoryStore) SaveRepositoryChange(_ context.Context, c domain.RepositoryChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.change = c
	return nil
}
func (s *memoryStore) RepositoryChange(context.Context, string) (domain.RepositoryChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.change, nil
}
func (s *memoryStore) RepositoryChanges(_ context.Context, _ string) ([]domain.RepositoryChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []domain.RepositoryChange{s.change}, nil
}
func (s *memoryStore) Initialization(context.Context, string) (domain.Initialization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.init, nil
}
func (s *memoryStore) SaveInitialization(_ context.Context, i domain.Initialization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init = i
	return nil
}
func (s *memoryStore) Lease(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *memoryStore) ReleaseLeases(context.Context, string) error { return nil }

func TestRemoteAdditionWaitsForMergedMR(t *testing.T) {
	const first = "projects:\n  alpha:\n    repo: PLACEHOLDER/team/alpha.git\n    path: alpha\n"
	var server *httptest.Server
	var store *memoryStore
	var merged, committed atomic.Bool
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/projects/2") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 2, "name": "Beta", "path_with_namespace": "team/beta", "default_branch": "main", "http_url_to_repo": server.URL + "/team/beta.git"})
			return
		}
		if strings.Contains(r.URL.Path, "/projects/team/meta") || strings.Contains(r.URL.Path, "/projects/team%2Fmeta") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 3, "name": "Meta", "http_url_to_repo": server.URL + "/team/meta.git"})
			return
		}
		if strings.Contains(r.URL.Path, "/repository/branches/") {
			if strings.HasSuffix(r.URL.Path, "swarmjet/add-repo-") {
				http.NotFound(w, r)
				return
			}
			if strings.Contains(r.URL.Path, "swarmjet/add-repo-") && !committed.Load() {
				http.NotFound(w, r)
				return
			}
			sha := strings.Repeat("a", 40)
			if merged.Load() {
				sha = strings.Repeat("b", 40)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "commit": map[string]string{"id": sha}})
			return
		}
		if strings.Contains(r.URL.Path, "/repository/files/") {
			body := strings.ReplaceAll(first, "PLACEHOLDER", server.URL)
			if merged.Load() || strings.Contains(r.URL.RawQuery, "swarmjet%2Fadd") {
				body += "  beta:\n    repo: " + server.URL + "/team/beta.git\n    path: beta\n"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(body))})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/repository/commits") {
			committed.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.Repeat("c", 40)})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/merge_requests") && r.Method == "GET" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/merge_requests") && r.Method == "POST" {
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 7, "web_url": server.URL + "/mr/7"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/merge_requests/7") {
			state := "opened"
			if merged.Load() {
				state = "merged"
			}
			current, _ := store.RepositoryChange(r.Context(), "")
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 7, "state": state, "source_branch": "swarmjet/add-repo-" + current.ID, "target_branch": "main"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	store = &memoryStore{project: domain.Project{ID: "p", Name: "P", Version: 1, GitLabURL: server.URL, MetaURL: server.URL + "/team/meta.git", MetaRef: "main", Repositories: []domain.Repository{{ID: "alpha", ProjectID: 1, URL: server.URL + "/team/alpha.git", Path: "alpha", TargetBranch: "main"}}}}
	m := &Manager{Store: store, CredentialFor: func(domain.Project) (string, error) { return "token", nil }}
	ctx := context.Background()
	change, err := m.Start(ctx, "p", AddRequest{GitLabProjectID: 2, Alias: "beta", Path: "beta", TargetBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.advance(ctx, &change); err != nil {
		t.Fatal(err)
	}
	if change.Status != "waiting" || change.MergeRequestID != 7 || len(store.project.Repositories) != 1 {
		t.Fatalf("premature registration: %+v", change)
	}
	if err = m.advance(ctx, &change); err != nil {
		t.Fatal(err)
	}
	if len(store.project.Repositories) != 1 {
		t.Fatal("registered before MR merge")
	}
	merged.Store(true)
	if err = m.advance(ctx, &change); err != nil {
		t.Fatal(err)
	}
	if change.Status != "applied" || !change.DocsStale || len(store.project.Repositories) != 2 {
		t.Fatalf("merged MR did not register repository: %+v", change)
	}
}

func TestLocalManagedAdditionUpdatesPublicationSHA(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/projects/2") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 2, "name": "Beta", "path_with_namespace": "team/beta", "default_branch": "main", "http_url_to_repo": server.URL + "/team/beta.git"})
			return
		}
		if strings.Contains(r.URL.Path, "/repository/branches/main") {
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "commit": map[string]string{"id": strings.Repeat("a", 40)}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	ctx := context.Background()
	managed := managedmeta.New(t.TempDir())
	files, err := managedmeta.Files("P", []domain.Repository{{ID: "alpha", URL: server.URL + "/team/alpha.git", Path: "alpha", TargetBranch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	oldSHA, err := managed.Ensure(ctx, "p", "main", files)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{project: domain.Project{ID: "p", Name: "P", Version: 1, MetaMode: domain.MetaModeManaged, GitLabURL: server.URL, MetaRef: "main", Repositories: []domain.Repository{{ID: "alpha", ProjectID: 1, URL: server.URL + "/team/alpha.git", Path: "alpha", TargetBranch: "main"}}}, init: domain.Initialization{ID: "init-p", ProjectID: "p", Status: domain.InitReady, ReadyForTasks: true, MetaSHA: oldSHA, Version: 1}}
	m := &Manager{Store: store, Managed: managed, CredentialFor: func(domain.Project) (string, error) { return "token", nil }}
	change, err := m.Start(ctx, "p", AddRequest{GitLabProjectID: 2, Alias: "beta", Path: "beta", TargetBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.advance(ctx, &change); err != nil {
		t.Fatal(err)
	}
	if change.Status != "applied" || store.init.MetaSHA == oldSHA || len(store.project.Repositories) != 2 {
		t.Fatalf("local addition not applied: %+v", change)
	}
}
