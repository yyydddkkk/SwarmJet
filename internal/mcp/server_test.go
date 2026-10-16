package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// --- test environment ------------------------------------------------------

type testEnv struct {
	t          *testing.T
	backend    *MemoryBackend
	server     *httptest.Server
	adminToken string
	admin      domain.Token
	userToken  string
	user       domain.Token
}

func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()
	backend := NewMemoryBackend()
	adminPlain, admin, err := backend.CreateToken(context.Background(), "admin", domain.TokenAdmin)
	if err != nil {
		t.Fatalf("CreateToken(admin): %v", err)
	}
	userPlain, user, err := backend.CreateToken(context.Background(), "user", domain.TokenDefault)
	if err != nil {
		t.Fatalf("CreateToken(user): %v", err)
	}
	srv := httptest.NewServer(NewHandler(backend, opts))
	t.Cleanup(srv.Close)
	return &testEnv{
		t:          t,
		backend:    backend,
		server:     srv,
		adminToken: adminPlain,
		admin:      admin,
		userToken:  userPlain,
		user:       user,
	}
}

type authRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (a authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	if a.token != "" {
		clone.Header.Set("Authorization", "Bearer "+a.token)
	}
	return a.base.RoundTrip(clone)
}

func (e *testEnv) connect(token string) *mcpsdk.ClientSession {
	e.t.Helper()
	httpClient := &http.Client{Transport: authRoundTripper{token: token, base: http.DefaultTransport}}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "swarmjet-test", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	e.t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             e.server.URL,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(func() { _ = session.Close() })
	return session
}

func (e *testEnv) seedHive(name, repo string) domain.Hive {
	e.t.Helper()
	h, err := e.backend.CreateHive(context.Background(), domain.Hive{Name: name, Repository: repo})
	if err != nil {
		e.t.Fatalf("CreateHive: %v", err)
	}
	return h
}

func callTool(t *testing.T, s *mcpsdk.ClientSession, name string, args any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func decode[T any](t *testing.T, res *mcpsdk.CallToolResult) T {
	t.Helper()
	var out T
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", b, err)
	}
	return out
}

func toolErrorText(res *mcpsdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// --- tests -----------------------------------------------------------------

func TestUnauthenticatedIsRejected(t *testing.T) {
	env := newTestEnv(t, Options{})
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`

	for name, token := range map[string]string{"no header": "", "bad token": "swj_not_a_real_token"} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, env.server.URL, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}
}

func TestToolListContainsEveryContractTool(t *testing.T) {
	env := newTestEnv(t, Options{})
	session := env.connect(env.userToken)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
	}
	want := []string{
		"list_hives", "get_hive", "create_hive", "update_hive", "send", "wait",
		"spawn_forager", "cancel_forager", "get_forager", "read_forager_detail",
		"inbox", "claim", "offline_digest",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tool %s is missing", name)
		}
	}
}

func TestAdminScopeForHiveConfiguration(t *testing.T) {
	env := newTestEnv(t, Options{})

	user := env.connect(env.userToken)
	res := callTool(t, user, "create_hive", map[string]any{"name": "n", "repository": "acme/h"})
	if !res.IsError {
		t.Fatalf("default token was allowed to create a hive")
	}
	if text := toolErrorText(res); !strings.Contains(text, "admin") {
		t.Fatalf("error %q does not mention the admin scope", text)
	}

	admin := env.connect(env.adminToken)
	created := decode[hiveOutput](t, callTool(t, admin, "create_hive", map[string]any{
		"name": "n", "repository": "acme/h",
	}))
	if created.Hive.ID == "" {
		t.Fatalf("create_hive returned no id: %+v", created)
	}
	if created.Hive.Version != 1 {
		t.Fatalf("version = %d, want 1", created.Hive.Version)
	}
	queen, err := env.backend.Queen(context.Background(), created.Hive.ID)
	if err != nil {
		t.Fatalf("Queen: %v", err)
	}
	if queen.State != domain.AgentDisabled {
		t.Fatalf("queen state = %s, want disabled", queen.State)
	}

	// Optimistic version check.
	updated := decode[hiveOutput](t, callTool(t, admin, "update_hive", map[string]any{
		"hiveId": created.Hive.ID, "version": 1, "name": "renamed",
	}))
	if updated.Hive.Version != 2 || updated.Hive.Name != "renamed" {
		t.Fatalf("update_hive = %+v", updated.Hive)
	}
	stale := callTool(t, admin, "update_hive", map[string]any{
		"hiveId": created.Hive.ID, "version": 1, "name": "again",
	})
	if !stale.IsError || !strings.Contains(toolErrorText(stale), "conflict") {
		t.Fatalf("stale update: isError=%v text=%q", stale.IsError, toolErrorText(stale))
	}
}

func TestListAndGetHive(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	list := decode[listHivesOutput](t, callTool(t, session, "list_hives", map[string]any{}))
	if list.Count != 1 || list.Hives[0].ID != h.ID {
		t.Fatalf("list_hives = %+v", list)
	}
	if list.Hives[0].Repository != "acme/alpha" {
		t.Fatalf("repository = %q", list.Hives[0].Repository)
	}

	got := decode[getHiveOutput](t, callTool(t, session, "get_hive", map[string]any{"hiveId": h.ID}))
	if got.Hive.ID != h.ID {
		t.Fatalf("get_hive = %+v", got)
	}
	if got.Queen == nil || got.Queen.State != string(domain.AgentDisabled) {
		t.Fatalf("queen = %+v", got.Queen)
	}
}

func TestListHivesTruncates(t *testing.T) {
	env := newTestEnv(t, Options{MaxResults: 2})
	for _, repo := range []string{"acme/a", "acme/b", "acme/c"} {
		env.seedHive(repo, repo)
	}
	session := env.connect(env.userToken)
	out := decode[listHivesOutput](t, callTool(t, session, "list_hives", map[string]any{}))
	if !out.Truncated || len(out.Hives) != 2 {
		t.Fatalf("expected a 2-item truncated page, got %+v", out)
	}
}

func TestSendValidationErrors(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"too large", map[string]any{"hiveId": h.ID, "body": strings.Repeat("x", domain.MaxMessageBody+1)}, "too large"},
		{"hop limit", map[string]any{"hiveId": h.ID, "body": "hi", "hop": 11}, "hop limit"},
		{"unknown hive", map[string]any{"hiveId": "hive_missing", "body": "hi"}, "not found"},
		{"bad address", map[string]any{"hiveId": h.ID, "body": "hi", "to": "nonsense"}, "invalid address"},
		{"unknown kind", map[string]any{"hiveId": h.ID, "body": "hi", "kind": "gossip"}, "invalid kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := callTool(t, session, "send", tc.args)
			if !res.IsError {
				t.Fatalf("expected an error result")
			}
			if text := toolErrorText(res); !strings.Contains(text, tc.want) {
				t.Fatalf("error %q does not contain %q", text, tc.want)
			}
		})
	}
}

func TestSendDefaultsToQueenAndNewChain(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
		"hiveId": h.ID, "body": "please look at issue 7",
	}))
	if out.Message.To != string(domain.AddressQueen) {
		t.Fatalf("to = %q, want queen", out.Message.To)
	}
	if out.Message.From != string(domain.LocalAddress(env.user.ID)) {
		t.Fatalf("from = %q", out.Message.From)
	}
	if out.Message.ChainID == "" {
		t.Fatalf("expected a generated chain id")
	}
	if out.Message.Kind != string(domain.MessageTask) {
		t.Fatalf("kind = %q, want task", out.Message.Kind)
	}

	inbox, err := env.backend.Inbox(context.Background(), h.ID, domain.AddressQueen, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Body != "please look at issue 7" {
		t.Fatalf("queen inbox = %+v", inbox)
	}
}

func TestWaitTimesOutAndWakes(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	// Timeout path.
	timed := decode[waitOutput](t, callTool(t, session, "wait", map[string]any{
		"hiveId": h.ID, "timeoutSeconds": 1,
	}))
	if !timed.TimedOut || len(timed.Messages) != 0 {
		t.Fatalf("wait timeout = %+v", timed)
	}

	// Wakeup path: call wait, then push a message into the local token's inbox.
	done := make(chan *mcpsdk.CallToolResult, 1)
	fail := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "wait", Arguments: map[string]any{
			"hiveId": h.ID, "timeoutSeconds": 10,
		}})
		if err != nil {
			fail <- err
			return
		}
		done <- res
	}()

	time.Sleep(150 * time.Millisecond)
	if _, err := env.backend.SendMessage(context.Background(), domain.Message{
		HiveID: h.ID,
		From:   domain.AddressQueen,
		To:     domain.LocalAddress(env.user.ID),
		Kind:   domain.MessageReply,
		Body:   "status update",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	select {
	case err := <-fail:
		t.Fatalf("wait: %v", err)
	case res := <-done:
		if res.IsError {
			t.Fatalf("wait tool error: %s", toolErrorText(res))
		}
		out := decode[waitOutput](t, res)
		if out.TimedOut {
			t.Fatalf("expected wakeup, got timeout: %+v", out)
		}
		if len(out.Messages) != 1 || out.Messages[0].Body != "status update" {
			t.Fatalf("messages = %+v", out.Messages)
		}
		if out.Cursor == "" {
			t.Fatalf("expected a cursor")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("wait did not return")
	}
}

func TestSpawnForagerDeliversTaskMessage(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	out := decode[spawnForagerOutput](t, callTool(t, session, "spawn_forager", map[string]any{
		"hiveId": h.ID, "task": "fix the flaky test",
	}))
	if out.Forager.ID == "" || out.Forager.State != string(domain.AgentCreating) {
		t.Fatalf("forager = %+v", out.Forager)
	}
	if !strings.HasPrefix(out.Forager.Branch, "swarm/") {
		t.Fatalf("branch = %q", out.Forager.Branch)
	}
	if out.Message.Kind != string(domain.MessageTask) || out.Message.To != string(domain.ForagerAddress(out.Forager.ID)) {
		t.Fatalf("task message = %+v", out.Message)
	}

	inbox, err := env.backend.Inbox(context.Background(), h.ID, domain.ForagerAddress(out.Forager.ID), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Body != "fix the flaky test" {
		t.Fatalf("forager inbox = %+v", inbox)
	}
}

func TestGetReadAndCancelForager(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	spawned := decode[spawnForagerOutput](t, callTool(t, session, "spawn_forager", map[string]any{
		"hiveId": h.ID, "task": "do a thing",
	}))

	got := decode[getForagerOutput](t, callTool(t, session, "get_forager", map[string]any{"agentId": spawned.Forager.ID}))
	if got.Forager.ID != spawned.Forager.ID || got.Forager.Task != "do a thing" {
		t.Fatalf("get_forager = %+v", got.Forager)
	}

	detail := decode[readForagerDetailOutput](t, callTool(t, session, "read_forager_detail", map[string]any{
		"agentId": spawned.Forager.ID,
	}))
	if len(detail.Messages) != 1 || detail.Messages[0].Body != "do a thing" {
		t.Fatalf("read_forager_detail = %+v", detail)
	}
	if detail.Cursor == "" {
		t.Fatalf("expected a cursor")
	}

	cancelled := decode[cancelForagerOutput](t, callTool(t, session, "cancel_forager", map[string]any{
		"agentId": spawned.Forager.ID,
	}))
	if cancelled.Forager.State != string(domain.AgentCancelled) {
		t.Fatalf("cancel_forager = %+v", cancelled.Forager)
	}
	again := callTool(t, session, "cancel_forager", map[string]any{"agentId": spawned.Forager.ID})
	if !again.IsError || !strings.Contains(toolErrorText(again), "terminal") {
		t.Fatalf("second cancel: isError=%v text=%q", again.IsError, toolErrorText(again))
	}
}

func TestInboxAndClaim(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	if _, err := env.backend.SendMessage(context.Background(), domain.Message{
		HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive,
		Kind: domain.MessageEvent, Body: "unrouted event",
	}); err != nil {
		t.Fatal(err)
	}

	list := decode[inboxOutput](t, callTool(t, session, "inbox", map[string]any{"hiveId": h.ID}))
	if len(list.Messages) != 1 || list.Messages[0].Body != "unrouted event" {
		t.Fatalf("inbox = %+v", list)
	}
	msgID := list.Messages[0].ID

	claimed := decode[claimOutput](t, callTool(t, session, "claim", map[string]any{
		"hiveId": h.ID, "messageId": msgID,
	}))
	if !claimed.Claimed || claimed.Owner != string(domain.LocalAddress(env.user.ID)) {
		t.Fatalf("claim = %+v", claimed)
	}

	// A different token cannot take the live lease.
	otherPlain, _, err := env.backend.CreateToken(context.Background(), "other", domain.TokenDefault)
	if err != nil {
		t.Fatal(err)
	}
	other := env.connect(otherPlain)
	contended := decode[claimOutput](t, callTool(t, other, "claim", map[string]any{
		"hiveId": h.ID, "messageId": msgID,
	}))
	if contended.Claimed {
		t.Fatalf("second owner claimed a live lease: %+v", contended)
	}
}

func TestOfflineDigestCursorIsForwardConsistent(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	first := decode[offlineDigestOutput](t, callTool(t, session, "offline_digest", map[string]any{}))
	if first.Cursor == "" {
		t.Fatalf("first digest returned no cursor")
	}
	if len(first.Messages) != 0 || len(first.Foragers) != 0 {
		t.Fatalf("first digest should be empty: %+v", first)
	}

	second := decode[offlineDigestOutput](t, callTool(t, session, "offline_digest", map[string]any{
		"cursor": first.Cursor,
	}))
	if len(second.Messages) != 0 || len(second.Foragers) != 0 {
		t.Fatalf("second digest should stay empty: %+v", second)
	}
	if second.Cursor == "" {
		t.Fatalf("second digest returned no cursor")
	}

	// New activity after the first cursor must show up.
	spawned := decode[spawnForagerOutput](t, callTool(t, session, "spawn_forager", map[string]any{
		"hiveId": h.ID, "task": "digest me",
	}))
	if _, err := env.backend.SendMessage(context.Background(), domain.Message{
		HiveID: h.ID, From: domain.AddressQueen, To: domain.LocalAddress(env.user.ID),
		Kind: domain.MessageReport, Body: "forager started",
	}); err != nil {
		t.Fatal(err)
	}

	third := decode[offlineDigestOutput](t, callTool(t, session, "offline_digest", map[string]any{
		"cursor": first.Cursor,
	}))
	if len(third.Messages) != 1 || third.Messages[0].Body != "forager started" {
		t.Fatalf("messages = %+v", third.Messages)
	}
	if len(third.Foragers) != 1 || third.Foragers[0].ID != spawned.Forager.ID {
		t.Fatalf("foragers = %+v", third.Foragers)
	}
	if third.Counts.Messages != 1 || third.Counts.Foragers != 1 {
		t.Fatalf("counts = %+v", third.Counts)
	}
}

func TestLongTextIsTruncated(t *testing.T) {
	env := newTestEnv(t, Options{MaxTextChars: 24})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	long := strings.Repeat("a", 200)
	if _, err := env.backend.SendMessage(context.Background(), domain.Message{
		HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive,
		Kind: domain.MessageEvent, Body: long,
	}); err != nil {
		t.Fatal(err)
	}

	out := decode[inboxOutput](t, callTool(t, session, "inbox", map[string]any{"hiveId": h.ID}))
	if len(out.Messages) != 1 {
		t.Fatalf("inbox = %+v", out)
	}
	msg := out.Messages[0]
	if !msg.BodyTruncated {
		t.Fatalf("expected truncation flag: %+v", msg)
	}
	if !strings.Contains(msg.Body, "[truncated:") {
		t.Fatalf("expected truncation marker in %q", msg.Body)
	}
	if utf8len(msg.Body) >= len(long) {
		t.Fatalf("body was not shortened: %q", msg.Body)
	}
}

func utf8len(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
