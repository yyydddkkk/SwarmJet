package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forager"
	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/mcp"
	"github.com/yyydddkkk/SwarmJet/internal/service"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// memoryQueue is the in-memory River stand-in. The test calls DeliverMessage
// explicitly so it exercises the real store and the real MessageSink contract
// without starting a queue worker. Webhook jobs are recorded so the test can
// drive the same service entry the production queue worker uses.
type memoryQueue struct {
	mu       sync.Mutex
	ids      []string
	webhooks []jobs.Webhook
}

func (q *memoryQueue) InsertWebhook(_ context.Context, _ pgx.Tx, job jobs.Webhook) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.webhooks = append(q.webhooks, job)
	return nil
}

func (q *memoryQueue) takeWebhooks() []jobs.Webhook {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := append([]jobs.Webhook(nil), q.webhooks...)
	q.webhooks = nil
	return out
}

func (q *memoryQueue) InsertMessage(_ context.Context, _ pgx.Tx, job jobs.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ids = append(q.ids, job.ID)
	return nil
}

func openRound2Store(t *testing.T) *store.Store {
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
	raw := make([]byte, 8)
	if _, err = rand.Read(raw); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "round2_test_" + hex.EncodeToString(raw)
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg := admin.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	s := &store.Store{Pool: pool, Jobs: &memoryQueue{}}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// scriptedCellRuntime is the fake cell.Runtime: every Start returns a session
// whose Ref follows Spec.SessionRef, so the supervisor sees a real resume.
type scriptedCellRuntime struct {
	mu       sync.Mutex
	starts   []cell.Spec
	prompts  []string
	destroy  []string
	sessions map[string]*scriptedSession
}

func newScriptedCellRuntime() *scriptedCellRuntime {
	return &scriptedCellRuntime{sessions: map[string]*scriptedSession{}}
}

func (r *scriptedCellRuntime) Start(_ context.Context, spec cell.Spec) (cell.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, spec)
	ref := spec.SessionRef
	if ref == "" {
		ref = "ses-" + spec.AgentID
	}
	session := &scriptedSession{runtime: r, agentID: spec.AgentID, ref: ref}
	r.sessions[spec.AgentID] = session
	return session, nil
}

func (r *scriptedCellRuntime) Stop(context.Context, string) error { return nil }

func (r *scriptedCellRuntime) Remove(context.Context, string) error { return nil }

func (r *scriptedCellRuntime) Destroy(_ context.Context, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.destroy = append(r.destroy, agentID)
	delete(r.sessions, agentID)
	return nil
}

func (r *scriptedCellRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func (r *scriptedCellRuntime) startSpecs() []cell.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]cell.Spec(nil), r.starts...)
}

func (r *scriptedCellRuntime) promptTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts...)
}

func (r *scriptedCellRuntime) destroyed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.destroy...)
}

type scriptedSession struct {
	runtime *scriptedCellRuntime
	agentID string
	ref     string
}

func (s *scriptedSession) Ref() string { return s.ref }

func (s *scriptedSession) Prompt(_ context.Context, text string, sink func(cell.Update)) (cell.TurnResult, error) {
	s.runtime.mu.Lock()
	s.runtime.prompts = append(s.runtime.prompts, text)
	s.runtime.mu.Unlock()
	if sink != nil {
		sink(cell.Update{Kind: cell.UpdateText, Text: "turn finished for " + s.agentID})
	}
	return cell.TurnResult{StopReason: "end_turn"}, nil
}

func (s *scriptedSession) Cancel(context.Context) error { return nil }
func (s *scriptedSession) Close() error                 { return nil }

type scriptedPublisher struct {
	mu    sync.Mutex
	calls []published
	pr    int64
}

type published struct {
	spec  cell.Spec
	title string
	body  string
}

func (p *scriptedPublisher) Publish(_ context.Context, spec cell.Spec, title, body string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, published{spec: spec, title: title, body: body})
	return p.pr, nil
}

func (p *scriptedPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *scriptedPublisher) last() published {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return published{}
	}
	return p.calls[len(p.calls)-1]
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}

func connectRound2MCP(t *testing.T, endpoint, token string) *mcpsdk.ClientSession {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "round2-test", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callRound2Tool(t *testing.T, session *mcpsdk.ClientSession, name string, args any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		var text strings.Builder
		for _, c := range result.Content {
			if tc, ok := c.(*mcpsdk.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		t.Fatalf("CallTool(%s) failed: %s", name, text.String())
	}
	return result
}

func decodeRound2[T any](t *testing.T, result *mcpsdk.CallToolResult) T {
	t.Helper()
	var out T
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", raw, err)
	}
	return out
}

func deliverPending(t *testing.T, s *store.Store, hiveID string, to domain.Address) {
	t.Helper()
	ctx := context.Background()
	messages, err := s.Inbox(ctx, hiveID, to, "", 100)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	for _, message := range messages {
		if message.State != domain.MessagePending {
			continue
		}
		if err := s.DeliverMessage(ctx, message.ID); err != nil {
			t.Fatalf("deliver message %s: %v", message.ID, err)
		}
	}
}

func waitAgentState(t *testing.T, s *store.Store, id string, want domain.AgentState) domain.Agent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		agent, err := s.Agent(context.Background(), id)
		if err == nil && agent.State == want {
			return agent
		}
		time.Sleep(20 * time.Millisecond)
	}
	agent, _ := s.Agent(context.Background(), id)
	t.Fatalf("agent %s did not reach %s; last state=%s", id, want, agent.State)
	return domain.Agent{}
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestRound2Loop drives the whole deterministic loop against a real PostgreSQL
// store, the real MCP server, Router and Forager supervisor. Only the Cell
// runtime and the Publisher are scripted, so it runs without Docker or network.
func TestRound2Loop(t *testing.T) {
	s := openRound2Store(t)
	ctx := context.Background()

	adminPlain, adminToken, err := s.CreateToken(ctx, "round2-admin", domain.TokenAdmin)
	if err != nil {
		t.Fatal(err)
	}
	_ = adminToken

	runtime := newScriptedCellRuntime()
	publisher := &scriptedPublisher{pr: 77}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	supervisor, err := forager.New(forager.Options{
		Store:     s,
		Runtime:   runtime,
		Publisher: publisher,
		Model:     "opencode/big-pickle",
		Logger:    logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.MessageSink = supervisor.Notify
	supervisorCtx, cancelSupervisor := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = supervisor.Run(supervisorCtx)
	}()
	t.Cleanup(func() {
		cancelSupervisor()
		<-done
	})

	mcpServer := httptest.NewServer(mcp.NewHandler(s, mcp.Options{Logger: logger}))
	t.Cleanup(mcpServer.Close)
	session := connectRound2MCP(t, mcpServer.URL, adminPlain)

	// a. Create a Hive through MCP with an admin token.
	created := decodeRound2[struct {
		Hive struct {
			ID string `json:"id"`
		} `json:"hive"`
	}](t, callRound2Tool(t, session, "create_hive", map[string]any{
		"name":       "Round 2 e2e",
		"repository": "acme/round2-e2e",
	}))
	if created.Hive.ID == "" {
		t.Fatal("create_hive returned an empty id")
	}
	hiveID := created.Hive.ID

	// b. Spawn a Forager with one task.
	spawned := decodeRound2[struct {
		Forager struct {
			ID     string `json:"id"`
			Branch string `json:"branch"`
		} `json:"forager"`
	}](t, callRound2Tool(t, session, "spawn_forager", map[string]any{
		"hiveId": hiveID,
		"task":   "Create round2.txt and report back.",
	}))
	if spawned.Forager.ID == "" {
		t.Fatal("spawn_forager returned an empty id")
	}
	foragerID := spawned.Forager.ID

	// c. Deliver the task; the supervisor runs a turn, publishes, reports and parks.
	deliverPending(t, s, hiveID, domain.ForagerAddress(foragerID))
	agent := waitAgentState(t, s, foragerID, domain.AgentParked)
	if agent.PRNumber != 77 {
		t.Fatalf("PR number = %d, want 77", agent.PRNumber)
	}
	if runtime.startCount() != 1 || len(runtime.promptTexts()) != 1 {
		t.Fatalf("starts=%d prompts=%d, want 1/1", runtime.startCount(), len(runtime.promptTexts()))
	}
	if publisher.count() != 1 {
		t.Fatalf("publish calls = %d, want 1", publisher.count())
	}
	if !strings.Contains(runtime.promptTexts()[0], "Create round2.txt") {
		t.Fatalf("first prompt does not contain the task: %q", runtime.promptTexts()[0])
	}

	// local:<token> receives a kind=report through the real wait tool.
	waited := decodeRound2[struct {
		Messages []struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
			To   string `json:"to"`
		} `json:"messages"`
		TimedOut bool `json:"timedOut"`
	}](t, callRound2Tool(t, session, "wait", map[string]any{
		"hiveId":         hiveID,
		"timeoutSeconds": 5,
	}))
	if waited.TimedOut || len(waited.Messages) == 0 {
		t.Fatalf("wait returned no report: %+v", waited)
	}
	if waited.Messages[0].Kind != string(domain.MessageReport) {
		t.Fatalf("first message kind = %q, want report", waited.Messages[0].Kind)
	}
	if waited.Messages[0].To != string(domain.LocalAddress(adminToken.ID)) {
		t.Fatalf("report target = %q, want local:%s", waited.Messages[0].To, adminToken.ID)
	}

	// d. The pull_request_review webhook enters through the retained service
	// entry: GitHub signature verification and queueing, then the same
	// HandleWebhookJob the River worker calls. The real Router starts a second
	// turn on the same Forager.
	reviewPayload := []byte(`{
		"action": "submitted",
		"review": {"body": "Please also mention the review in the next turn.", "state": "commented", "user": {"login": "reviewer", "author_association": "OWNER"}, "html_url": "https://github.com/acme/round2-e2e/pull/77#pullrequestreview-1"},
		"pull_request": {"number": 77, "title": "Round 2 e2e", "html_url": "https://github.com/acme/round2-e2e/pull/77", "head": {"ref": "` + spawned.Forager.Branch + `"}},
		"repository": {"full_name": "acme/round2-e2e"}
	}`)
	const webhookSecret = "round2-test-webhook-secret"
	svc := &service.Service{Store: s, Webhooks: s}
	webhookServer := httptest.NewServer(svc.GitHubWebhook(webhookSecret))
	t.Cleanup(webhookServer.Close)
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	_, _ = mac.Write(reviewPayload)
	webhookReq, err := http.NewRequest(http.MethodPost, webhookServer.URL, bytes.NewReader(reviewPayload))
	if err != nil {
		t.Fatal(err)
	}
	webhookReq.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	webhookReq.Header.Set("X-GitHub-Delivery", "round2-review-1")
	webhookReq.Header.Set("X-GitHub-Event", "pull_request_review")
	resp, err := http.DefaultClient.Do(webhookReq)
	if err != nil {
		t.Fatalf("post review webhook: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("review webhook status = %d, want 202", resp.StatusCode)
	}
	queued := s.Jobs.(*memoryQueue).takeWebhooks()
	if len(queued) != 1 {
		t.Fatalf("queued %d webhook jobs, want 1", len(queued))
	}
	if queued[0].EventType != "pull_request_review" || queued[0].DeliveryID != "round2-review-1" {
		t.Fatalf("queued webhook = %+v", queued[0])
	}
	if err := svc.HandleWebhookJob(ctx, queued[0]); err != nil {
		t.Fatalf("route review webhook: %v", err)
	}
	deliverPending(t, s, hiveID, domain.ForagerAddress(foragerID))
	// The Forager is already parked, so wait for the second turn itself, not
	// just for a state that was already true before the message arrived.
	waitUntil(t, "second turn", func() bool { return len(runtime.promptTexts()) >= 2 })
	waitAgentState(t, s, foragerID, domain.AgentParked)
	if runtime.startCount() != 2 {
		t.Fatalf("starts = %d, want 2 (second turn must resume the Cell)", runtime.startCount())
	}
	prompts := runtime.promptTexts()
	if len(prompts) != 2 || !strings.Contains(prompts[1], "Please also mention the review") {
		t.Fatalf("second prompt does not contain the review: %q", prompts)
	}
	if starts := runtime.startSpecs(); len(starts) != 2 || starts[1].SessionRef != "ses-"+foragerID {
		t.Fatalf("second Start did not resume the session: %+v", starts)
	}

	// e. cancel_forager interrupts, destroys the Cell and revokes the Agent token.
	starts := runtime.startSpecs()
	agentToken := starts[0].MCPToken
	if agentToken == "" {
		t.Fatal("supervisor did not mint an Agent token")
	}
	cancelled := decodeRound2[struct {
		Forager struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"forager"`
	}](t, callRound2Tool(t, session, "cancel_forager", map[string]any{"agentId": foragerID}))
	if cancelled.Forager.ID != foragerID {
		t.Fatalf("cancel_forager returned %q", cancelled.Forager.ID)
	}
	deliverPending(t, s, hiveID, domain.ForagerAddress(foragerID))
	final := waitAgentState(t, s, foragerID, domain.AgentCancelled)
	if final.State != domain.AgentCancelled {
		t.Fatalf("state = %s, want cancelled", final.State)
	}
	// The terminal state is written before Destroy and RevokeAgentTokens, so
	// wait for the cleanup instead of sampling once.
	waitUntil(t, "Cell destroy", func() bool {
		destroyed := runtime.destroyed()
		return len(destroyed) == 1 && destroyed[0] == foragerID
	})
	waitUntil(t, "Agent token revocation", func() bool {
		_, err := s.TokenByPlaintext(ctx, agentToken)
		return errors.Is(err, domain.ErrNotFound)
	})
	if publisher.count() < 2 {
		t.Fatalf("publish calls = %d, want at least 2", publisher.count())
	}
}

func TestCellsRequireGitHubApp(t *testing.T) {
	if _, err := newCellStack(nil, nil, t.TempDir(), nil); err == nil {
		t.Fatal("SWARMJET_CELLS=1 without a GitHub App must fail at startup")
	} else if !strings.Contains(err.Error(), "GITHUB_APP_ID") {
		t.Fatalf("error does not name the missing configuration: %v", err)
	}
}

func TestCellsRequireDockerSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/swarmjet-docker.sock")
	_, err := newCellStack(&store.Store{}, &githubapp.App{}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("SWARMJET_CELLS=1 without a Docker socket must fail at startup")
	}
	if !strings.Contains(err.Error(), "启用 Cell 需要叠加 deploy/compose.cells.yaml") {
		t.Fatalf("error does not point at the cells overlay: %v", err)
	}
}

func TestCellCLIEnvReadsOptionalAuth(t *testing.T) {
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	env, err := cellCLIEnv(context.Background(), domain.Hive{})
	if err != nil || env != nil {
		t.Fatalf("empty auth = %v, %v", env, err)
	}
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"token":"placeholder"}`)
	env, err = cellCLIEnv(context.Background(), domain.Hive{})
	if err != nil || env["OPENCODE_AUTH_CONTENT"] != `{"token":"placeholder"}` {
		t.Fatalf("auth env = %v, %v", env, err)
	}
}
