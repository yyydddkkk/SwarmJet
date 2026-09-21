package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/domain"
	"swarmjet/internal/store"
)

func TestWebhookAuthenticationMethodAndBodyLimit(t *testing.T) {
	for _, test := range []struct {
		name, secret, token, method string
		size, status                int
	}{
		{"authenticated", "shared-secret", "shared-secret", "POST", 16, http.StatusAccepted},
		{"missing-token", "shared-secret", "", "POST", 16, http.StatusUnauthorized},
		{"wrong-token", "shared-secret", "different", "POST", 16, http.StatusUnauthorized},
		{"disabled-secret", "", "", "POST", 16, http.StatusUnauthorized},
		{"wrong-method", "shared-secret", "shared-secret", "GET", 0, http.StatusMethodNotAllowed},
		{"exact-limit", "shared-secret", "shared-secret", "POST", 1 << 20, http.StatusAccepted},
		{"oversized", "shared-secret", "shared-secret", "POST", (1 << 20) + 1, http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &Service{Wake: make(chan struct{}, 1)}
			request := httptest.NewRequest(test.method, "/webhooks/gitlab", strings.NewReader(strings.Repeat("x", test.size)))
			request.Header.Set("X-Gitlab-Token", test.token)
			response := httptest.NewRecorder()
			service.Webhook(test.secret).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			wantWake := 0
			if test.status == http.StatusAccepted {
				wantWake = 1
			}
			if len(service.Wake) != wantWake {
				t.Fatalf("wake signalled before validation: %d", len(service.Wake))
			}
		})
	}
}
func TestWebhookCoalescesConcurrentNotificationsWithoutBlocking(t *testing.T) {
	service := &Service{Wake: make(chan struct{}, 1)}
	server := httptest.NewServer(service.Webhook("token"))
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			req, err := http.NewRequest("POST", server.URL, strings.NewReader(`{"object_kind":"issue"}`))
			if err != nil {
				t.Error(err)
				return
			}
			req.Header.Set("X-Gitlab-Token", "token")
			resp, err := client.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusAccepted {
				t.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	workers.Wait()
	if len(service.Wake) != 1 {
		t.Fatalf("expected one coalesced notification, got %d", len(service.Wake))
	}
	<-service.Wake
	req := httptest.NewRequest("POST", "/webhooks/gitlab", nil)
	req.Header.Set("X-Gitlab-Token", "token")
	response := httptest.NewRecorder()
	service.Webhook("token").ServeHTTP(response, req)
	if response.Code != http.StatusAccepted || len(service.Wake) != 1 {
		t.Fatal("next event did not wake after queue drained")
	}
}
func TestWebhookNilWakeDoesNotBlock(t *testing.T) {
	service := &Service{}
	request := httptest.NewRequest("POST", "/webhooks/gitlab", nil)
	request.Header.Set("X-Gitlab-Token", "token")
	response := httptest.NewRecorder()
	service.Webhook("token").ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d", response.Code)
	}
}
func scanTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "service_test_" + hex.EncodeToString(random)
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg := admin.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{Pool: pool}
	t.Cleanup(func() {
		s.Close()
		_, err := admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func scanProject(gitlabURL string) domain.Project {
	return domain.Project{ID: "scan", Name: "Scan", Enabled: true, MetaURL: "https://git.example/group/meta.git", MetaRef: "main", GitLabURL: gitlabURL, CredentialRef: "GITLAB_TOKEN", MergeCredentialRef: "GITLAB_MERGE_TOKEN", Repositories: []domain.Repository{{ID: "service", ProjectID: 7, URL: "https://git.example/group/service.git", Path: "service", TargetBranch: "main"}}, Development: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Review: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Integration: domain.IntegrationConfig{ComposeFile: "compose.yml", StartupTimeoutSeconds: 30, Checks: []domain.Check{{Name: "test", Service: "test", Command: []string{"true"}, Required: true}}}, MaxReworks: 3}
}

func TestScanUsesDistributedSchedulingPolicy(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"project_id": 7, "iid": 1, "title": "task", "state": "opened", "labels": []string{"autonomous"}, "web_url": "http://example/7/1"}})
	}))
	defer gitlab.Close()
	p := scanProject(gitlab.URL)
	if _, err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }, RequireNode: true}
	// Distributed mode with no registered node must not create a local task.
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("distributed scan created %d tasks: %v", len(tasks), err)
	}
	// Once a node is online the same scan pins the task to it.
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "node-1", Address: "http://node-1:8092", Capacity: 2}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].NodeID != "node-1" {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}
