package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/domain"
	"swarmjet/internal/imagebuild"
	"swarmjet/internal/observability"
	"swarmjet/internal/pipeline"
	"swarmjet/internal/store"
)

func TestTaskScheduledLogLinksWebhookDelivery(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	ctx := context.WithValue(context.Background(), webhookDeliveryContextKey{}, webhookDeliveryContext{provider: "gitlab", deliveryID: "delivery-123"})
	logTaskScheduled(ctx, domain.Task{ID: "task-123", Issue: domain.Issue{ProjectID: 7, IID: 3}})
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["task_id"] != "task-123" || entry["delivery_id"] != "delivery-123" || entry["issue_iid"] != float64(3) {
		t.Fatalf("missing task-to-delivery correlation: %v", entry)
	}
}

func TestScheduledAdmissionCountsOnlyAssignedTask(t *testing.T) {
	metrics := observability.New()
	svc := &Service{Metrics: metrics}
	svc.recordScheduledAdmission(domain.Task{AdmissionStatus: "waiting", AdmissionBackend: "node"})
	svc.recordScheduledAdmission(domain.Task{AdmissionStatus: "assigned", AdmissionBackend: "local"})
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), `swarmjet_admitted_tasks_total{backend="local_or_node"} 1`) {
		t.Fatalf("immediate admission count missing: %s", response.Body.String())
	}
}

func TestWorkflowRouteSeparatesKubernetesWorker(t *testing.T) {
	for _, tc := range []struct{ backend, name, queue string }{
		{"local", pipeline.WorkflowName, pipeline.Queue},
		{"node", pipeline.WorkflowName, pipeline.Queue},
		{"kubernetes", pipeline.KubernetesWorkflowName, pipeline.KubernetesQueue},
	} {
		name, queue := workflowRoute(domain.Task{AdmissionBackend: tc.backend})
		if name != tc.name || queue != tc.queue {
			t.Fatalf("%s routed to %s/%s", tc.backend, name, queue)
		}
	}
}

func TestGitHubWebhookValidatesBodySignature(t *testing.T) {
	publisher := &testWebhookPublisher{}
	service := &Service{WebhookPublisher: publisher}
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for _, tc := range []struct {
		name, sig string
		body      []byte
		want      int
	}{
		{"valid", signature, body, http.StatusAccepted},
		{"tampered", signature, []byte(`{"action":"closed"}`), http.StatusUnauthorized},
		{"missing", "", body, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(string(tc.body)))
			req.Header.Set("X-Hub-Signature-256", tc.sig)
			req.Header.Set("X-GitHub-Delivery", "delivery-1")
			response := httptest.NewRecorder()
			service.GitHubWebhook("secret").ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d", response.Code, tc.want)
			}
		})
	}
	if publisher.count != 1 {
		t.Fatal("only the valid delivery should be published")
	}
}

func TestGitLabWebhookValidatesSigningToken(t *testing.T) {
	key := []byte("gitlab-test-signing-key")
	token := "whsec_" + base64.StdEncoding.EncodeToString(key)
	publisher := &testWebhookPublisher{}
	svc := &Service{WebhookPublisher: publisher, GitLabSigningToken: token}
	body := []byte(`{"object_kind":"issue"}`)
	id := "signed-delivery"
	stamp := fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(id + "." + stamp + "."))
	_, _ = mac.Write(body)
	signature := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, tc := range []struct {
		name, signature, stamp string
		body                   []byte
		want                   int
	}{
		{"valid", signature, stamp, body, http.StatusAccepted},
		{"tampered", signature, stamp, []byte(`{"object_kind":"note"}`), http.StatusUnauthorized},
		{"stale", signature, fmt.Sprint(time.Now().Add(-time.Hour).Unix()), body, http.StatusUnauthorized},
		{"missing", "", stamp, body, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", bytes.NewReader(tc.body))
			req.Header.Set("webhook-id", id)
			req.Header.Set("webhook-timestamp", tc.stamp)
			req.Header.Set("webhook-signature", tc.signature)
			response := httptest.NewRecorder()
			svc.Webhook("").ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d", response.Code, tc.want)
			}
		})
	}
	if publisher.count != 1 {
		t.Fatalf("published %d signed deliveries", publisher.count)
	}
}

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
			publisher := &testWebhookPublisher{}
			service := &Service{WebhookPublisher: publisher}
			request := httptest.NewRequest(test.method, "/webhooks/gitlab", strings.NewReader(strings.Repeat("x", test.size)))
			request.Header.Set("X-Gitlab-Token", test.token)
			request.Header.Set("webhook-id", "delivery-1")
			response := httptest.NewRecorder()
			service.Webhook(test.secret).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			wantPublish := 0
			if test.status == http.StatusAccepted {
				wantPublish = 1
			}
			if publisher.count != wantPublish {
				t.Fatalf("published before validation: %d", publisher.count)
			}
		})
	}
}

type testWebhookPublisher struct {
	mu       sync.Mutex
	count    int
	messages [][]byte
	err      error
}

func (p *testWebhookPublisher) Publish(_ context.Context, queue string, body []byte, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if queue == "" || id == "" {
		return errors.New("missing queue or ID")
	}
	if p.err != nil {
		return p.err
	}
	p.count++
	p.messages = append(p.messages, append([]byte(nil), body...))
	return nil
}
func TestWebhookRequiresStableDeliveryIDAndBrokerConfirmation(t *testing.T) {
	publisher := &testWebhookPublisher{}
	svc := &Service{WebhookPublisher: publisher}
	send := func(id string) int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(`{"object_kind":"issue"}`))
		req.Header.Set("X-Gitlab-Token", "secret")
		if id != "" {
			req.Header.Set("webhook-id", id)
		}
		response := httptest.NewRecorder()
		svc.Webhook("secret").ServeHTTP(response, req)
		return response.Code
	}
	if got := send(""); got != http.StatusBadRequest {
		t.Fatalf("missing ID status=%d", got)
	}
	publisher.err = errors.New("broker unavailable")
	if got := send("delivery-1"); got != http.StatusServiceUnavailable {
		t.Fatalf("publish failure status=%d", got)
	}
	publisher.err = nil
	if got := send("delivery-1"); got != http.StatusAccepted {
		t.Fatalf("confirmed status=%d", got)
	}
	if publisher.count != 1 {
		t.Fatalf("published %d", publisher.count)
	}
	var message WebhookMessage
	if err := json.Unmarshal(publisher.messages[0], &message); err != nil {
		t.Fatal(err)
	}
	if message.Provider != "gitlab" || message.DeliveryID != "delivery-1" || string(message.Payload) != `{"object_kind":"issue"}` {
		t.Fatalf("message=%+v", message)
	}
}

func TestWebhookReceiptDeduplicatesAndRejectsContentConflict(t *testing.T) {
	s := scanTestStore(t)
	svc := &Service{Store: s}
	ctx := context.Background()
	message := WebhookMessage{Provider: "gitlab", DeliveryID: "same-id", EventType: "Issue Hook", Payload: []byte(`{"project":{"id":7}}`)}
	body, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := svc.ProcessWebhookDelivery(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipts=%d err=%v", count, err)
	}
	message.Payload = []byte(`{"project":{"id":8}}`)
	body, _ = json.Marshal(message)
	if err := svc.ProcessWebhookDelivery(ctx, body); !errors.Is(err, store.ErrWebhookConflict) {
		t.Fatalf("content conflict=%v", err)
	}
}

func TestWebhookReceiptWaitsForSuccessfulScan(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	var fail atomic.Bool
	fail.Store(true)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer remote.Close()
	if _, err := s.SaveProject(ctx, scanProject(remote.URL)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	body, _ := json.Marshal(WebhookMessage{Provider: "gitlab", DeliveryID: "retry-id", Payload: []byte(`{"project":{"id":7}}`)})
	if err := svc.ProcessWebhookDelivery(ctx, body); err == nil {
		t.Fatal("expected scan failure")
	}
	var count int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts").Scan(&count); err != nil || count != 0 {
		t.Fatalf("premature receipt=%d err=%v", count, err)
	}
	fail.Store(false)
	if err := svc.ProcessWebhookDelivery(ctx, body); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt=%d err=%v", count, err)
	}
}

func TestWebhookBatchScansRepositoryOnce(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	var scans atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scans.Add(1)
		_, _ = io.WriteString(w, `[]`)
	}))
	defer remote.Close()
	if _, err := s.SaveProject(ctx, scanProject(remote.URL)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	var bodies [][]byte
	for _, id := range []string{"one", "two", "three"} {
		body, _ := json.Marshal(WebhookMessage{Provider: "gitlab", DeliveryID: id, Payload: []byte(`{"project":{"id":7}}`)})
		bodies = append(bodies, body)
	}
	for _, err := range svc.ProcessWebhookDeliveries(ctx, bodies) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if scans.Load() != 1 {
		t.Fatalf("scans=%d", scans.Load())
	}
	var receipts int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts").Scan(&receipts); err != nil || receipts != 3 {
		t.Fatalf("receipts=%d err=%v", receipts, err)
	}
}

func TestInboxRetainsDeliveryAfterScanFailure(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	var fail atomic.Bool
	fail.Store(true)
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer gitlab.Close()
	if _, err := s.SaveProject(ctx, scanProject(gitlab.URL)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueWebhook(ctx, "gitlab", "retry-me", "Issue Hook", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	if err := svc.ProcessInbox(ctx); err == nil {
		t.Fatal("expected scan failure")
	}
	var processed bool
	var attempts int
	if err := s.Pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL, attempts FROM webhook_inbox WHERE delivery_id='retry-me'").Scan(&processed, &attempts); err != nil || processed || attempts != 1 {
		t.Fatalf("processed=%v attempts=%d err=%v", processed, attempts, err)
	}
	fail.Store(false)
	if _, err := s.Pool.Exec(ctx, "UPDATE webhook_inbox SET next_attempt_at=now() WHERE delivery_id='retry-me'"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessInbox(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL, attempts FROM webhook_inbox WHERE delivery_id='retry-me'").Scan(&processed, &attempts); err != nil || !processed || attempts != 2 {
		t.Fatalf("retried processed=%v attempts=%d err=%v", processed, attempts, err)
	}
}

func TestIssueBurstStopsAtAdmissionBoundAndRetainsInbox(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	issues := make([]map[string]any, 105)
	for i := range issues {
		issues[i] = map[string]any{"project_id": 7, "iid": i + 1, "title": "burst", "state": "opened", "labels": []string{"autonomous"}}
	}
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(issues[100:])
			return
		}
		_ = json.NewEncoder(w).Encode(issues[:100])
	}))
	defer gitlab.Close()
	if _, err := s.SaveProject(ctx, scanProject(gitlab.URL)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueWebhook(ctx, "gitlab", "burst", "Issue Hook", []byte(`{"project":{"id":7}}`)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, RequireNode: true, ResolveCredential: func(string) (string, error) { return "token", nil }}
	if err := svc.ProcessInbox(ctx); !errors.Is(err, store.ErrAdmissionQueueFull) {
		t.Fatalf("burst should reach the configured queue bound: %v", err)
	}
	page, err := s.TaskPage(ctx, store.TaskPageOptions{View: "waiting"})
	if err != nil || page.Counts.Waiting != 100 || page.Total != 100 {
		t.Fatalf("waiting tasks=%d total=%d err=%v", page.Counts.Waiting, page.Total, err)
	}
	stats, err := s.OperationsSnapshot(ctx)
	if err != nil || stats.InboxPending != 1 || stats.RunnerSlotsReserved != 0 {
		t.Fatalf("operations=%+v err=%v", stats, err)
	}
}

func TestQueuedWebhookKeepsDeliveryWhenAdmissionIsFull(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	issues := make([]map[string]any, 105)
	for i := range issues {
		issues[i] = map[string]any{"project_id": 7, "iid": i + 1, "title": "burst", "state": "opened", "labels": []string{"autonomous"}}
	}
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(issues[100:])
			return
		}
		_ = json.NewEncoder(w).Encode(issues[:100])
	}))
	defer gitlab.Close()
	if _, err := s.SaveProject(ctx, scanProject(gitlab.URL)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, RequireNode: true, ResolveCredential: func(string) (string, error) { return "token", nil }}
	body, _ := json.Marshal(WebhookMessage{Provider: "gitlab", DeliveryID: "queue-admission", EventType: "Issue Hook", Payload: []byte(`{"project":{"id":7}}`)})
	if err := svc.ProcessWebhookDelivery(ctx, body); !errors.Is(err, store.ErrAdmissionQueueFull) {
		t.Fatalf("admission error=%v", err)
	}
	page, err := s.TaskPage(ctx, store.TaskPageOptions{View: "waiting"})
	if err != nil || page.Counts.Waiting != 100 {
		t.Fatalf("waiting=%d err=%v", page.Counts.Waiting, err)
	}
	var receipts int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("premature receipts=%d err=%v", receipts, err)
	}
}

func TestInboxScansAndRetriesRepositoriesIndependently(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[]`) }))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer bad.Close()
	goodProject := scanProject(good.URL)
	goodProject.ID = "good"
	badProject := scanProject(bad.URL)
	badProject.ID = "bad"
	badProject.Repositories[0].ProjectID = 8
	for _, p := range []domain.Project{goodProject, badProject} {
		if _, err := s.SaveProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []struct{ id, body string }{{"good-delivery", `{"project":{"id":7}}`}, {"bad-delivery", `{"project_id":8}`}} {
		if err := s.EnqueueWebhook(ctx, "gitlab", event.id, "Issue Hook", []byte(event.body)); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	if err := svc.ProcessInbox(ctx); err == nil {
		t.Fatal("expected bad repository scan failure")
	}
	for _, tc := range []struct {
		id        string
		processed bool
	}{{"good-delivery", true}, {"bad-delivery", false}} {
		var processed bool
		if err := s.Pool.QueryRow(ctx, "SELECT processed_at IS NOT NULL FROM webhook_inbox WHERE delivery_id=$1", tc.id).Scan(&processed); err != nil || processed != tc.processed {
			t.Fatalf("delivery %s processed=%v err=%v", tc.id, processed, err)
		}
	}
}

func TestWebhookRepositoryExtraction(t *testing.T) {
	for _, tc := range []struct {
		provider, payload string
		wantProvider      domain.Provider
		wantID            int64
	}{
		{"gitlab", `{"project":{"id":7}}`, domain.ProviderGitLab, 7},
		{"gitlab", `{"project_id":8}`, domain.ProviderGitLab, 8},
		{"github", `{"repository":{"id":9}}`, domain.ProviderGitHub, 9},
		{"github", `{"project":{"id":7}}`, "", 0},
		{"gitlab", `{}`, "", 0},
	} {
		provider, id := webhookRepository(tc.provider, []byte(tc.payload))
		if provider != tc.wantProvider || id != tc.wantID {
			t.Fatalf("%s %s: got %s/%d", tc.provider, tc.payload, provider, id)
		}
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
	if err = s.SetEnabledAgents(ctx, []string{"opencode", "codex"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func scanProject(gitlabURL string) domain.Project {
	return domain.Project{ID: "scan", Name: "Scan", Enabled: true, MetaURL: gitlabURL + "/group/meta.git", MetaRef: "main", GitLabURL: gitlabURL, CredentialRef: "GITLAB_TOKEN", MergeCredentialRef: "GITLAB_MERGE_TOKEN", Repositories: []domain.Repository{{ID: "service", ProjectID: 7, URL: gitlabURL + "/group/service.git", Path: "service", TargetBranch: "main"}}, Development: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Review: domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}, Integration: domain.IntegrationConfig{ComposeFile: "compose.yml", StartupTimeoutSeconds: 30, Checks: []domain.Check{{Name: "test", Service: "test", Command: []string{"true"}, Required: true}}}, MaxReworks: 3}
}

func TestReconcileLabelsTracksEnabledAgentsWithoutEditingExisting(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	var mu sync.Mutex
	remoteLabels := map[string]string{"autonomous": "#123456"}
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/7/labels" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "POST" {
			var label struct{ Name, Color string }
			if err := json.NewDecoder(r.Body).Decode(&label); err != nil {
				t.Error(err)
			}
			remoteLabels[label.Name] = label.Color
			w.WriteHeader(http.StatusCreated)
			return
		}
		list := make([]map[string]string, 0, len(remoteLabels))
		for name, color := range remoteLabels {
			list = append(list, map[string]string{"name": name, "color": color})
		}
		_ = json.NewEncoder(w).Encode(list)
	}))
	defer gitlab.Close()
	if _, err := s.SaveProject(ctx, scanProject(gitlab.URL)); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	if err := svc.reconcileLabels(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(remoteLabels) != 3 || remoteLabels["autonomous"] != "#123456" || remoteLabels["agent:codex"] == "" || remoteLabels["agent:opencode"] == "" {
		t.Errorf("unexpected labels %+v", remoteLabels)
	}
	mu.Unlock()
	if err := s.SetEnabledAgents(ctx, []string{"codex", "dst"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileLabels(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(remoteLabels) != 4 || remoteLabels["agent:dst"] == "" || remoteLabels["agent:opencode"] == "" {
		t.Errorf("agent change altered existing labels %+v", remoteLabels)
	}
	mu.Unlock()
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
	// Distributed mode records demand without starting a local workflow.
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].AdmissionStatus != "waiting" || tasks[0].NodeID != "" {
		t.Fatalf("distributed scan did not queue demand: %+v %v", tasks, err)
	}
	if pending, err := s.Pending(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("unassigned task has a start command: %+v %v", pending, err)
	}
	// Once a node is online admission pins the existing task to it.
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "node-1", Address: "http://node-1:8092", Capacity: 2}); err != nil {
		t.Fatal(err)
	}
	if admitted, err := s.AdmitPendingTasks(ctx, 20); err != nil || admitted != 1 {
		t.Fatal(err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].NodeID != "node-1" || tasks[0].AdmissionStatus != "assigned" {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

func TestScanRejectsAgentConflictWithoutClaimAndFreezesSelection(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	labels := []string{"autonomous", "agent:codex", "agent:opencode"}
	var mu sync.Mutex
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/notes") {
			w.Write([]byte(`[]`))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode([]map[string]any{{"project_id": 7, "iid": 1, "state": "opened", "labels": labels}})
	}))
	defer gitlab.Close()
	p := scanProject(gitlab.URL)
	if _, err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	imageDir := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	images, err := imagebuild.New(imageDir, func(ctx context.Context, args []string, out io.Writer) error {
		_, err := io.WriteString(out, digest)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	image, err := images.Import(ctx, "agent:configured")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = images.Activate(ctx, image.ID); err != nil {
		t.Fatal(err)
	}
	svc := &Service{DataDir: imageDir, Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if seen, err := s.HasSource(ctx, p.GitLabURL, domain.Issue{ProjectID: 7, IID: 1}); err != nil || seen {
		t.Fatal("invalid labels claimed issue", seen, err)
	}
	mu.Lock()
	labels = []string{"autonomous", "agent:codex"}
	mu.Unlock()
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	if tasks[0].Project.Development.Image != digest || tasks[0].Project.Review.Image != digest {
		t.Fatal("active image was not captured at acceptance")
	}
	if tasks[0].Project.Development.Adapter != "codex" || tasks[0].Project.Review.Adapter != "codex" || tasks[0].Project.Development.Model != "" || !tasks[0].Project.Development.UseCLIDefaults {
		t.Fatal("agent selection not frozen", tasks[0].Project)
	}
	// Disabling a selected adapter or changing labels never rewrites a claimed task.
	if err := s.SetEnabledAgents(ctx, []string{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	labels = []string{"autonomous", "agent:opencode"}
	mu.Unlock()
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Project.Development.Adapter != "codex" {
		t.Fatal("existing snapshot changed", tasks, err)
	}
}
