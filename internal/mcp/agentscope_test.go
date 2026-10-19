package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// allToolNames is the full catalog. Visibility and permission tables are both
// checked against it so a newly added tool cannot silently bypass either.
var allToolNames = []string{
	"list_hives", "get_hive", "create_hive", "update_hive", "send", "wait",
	"spawn", "spawn_forager", "cancel_forager", "get_forager", "read_forager_detail",
	"update_handoff", "inbox", "claim", "offline_digest",
	"get_methodology", "list_methodology_versions", "publish_methodology", "activate_methodology",
}

func allTools() map[string]bool {
	out := make(map[string]bool, len(allToolNames))
	for _, name := range allToolNames {
		out[name] = true
	}
	return out
}

type agentScopeEnv struct {
	*testEnv
	hive         domain.Hive
	otherHive    domain.Hive
	queen        domain.Agent
	forager      domain.Agent
	peer         domain.Agent
	otherForager domain.Agent
	foragerToken string
	queenToken   string
}

func newAgentScopeEnv(t *testing.T) *agentScopeEnv {
	t.Helper()
	env := newTestEnv(t, Options{})
	ctx := context.Background()
	hive := env.seedHive("alpha", "acme/alpha")
	otherHive := env.seedHive("beta", "acme/beta")
	queen, err := env.backend.Queen(ctx, hive.ID)
	if err != nil {
		t.Fatalf("Queen: %v", err)
	}
	newForager := func(hiveID, task string) domain.Agent {
		a, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hiveID, Task: task})
		if err != nil {
			t.Fatalf("CreateForager: %v", err)
		}
		return a
	}
	forager := newForager(hive.ID, "fix the bug")
	peer := newForager(hive.ID, "write the docs")
	otherForager := newForager(otherHive.ID, "unrelated")
	foragerToken, _, err := env.backend.CreateAgentToken(ctx, forager.ID)
	if err != nil {
		t.Fatalf("CreateAgentToken(forager): %v", err)
	}
	queenToken, _, err := env.backend.CreateAgentToken(ctx, queen.ID)
	if err != nil {
		t.Fatalf("CreateAgentToken(queen): %v", err)
	}
	return &agentScopeEnv{
		testEnv:      env,
		hive:         hive,
		otherHive:    otherHive,
		queen:        queen,
		forager:      forager,
		peer:         peer,
		otherForager: otherForager,
		foragerToken: foragerToken,
		queenToken:   queenToken,
	}
}

func (e *agentScopeEnv) toolNames(t *testing.T, token string) map[string]bool {
	t.Helper()
	res, err := e.connect(token).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := map[string]bool{}
	for _, tool := range res.Tools {
		out[tool.Name] = true
	}
	return out
}

// callToolRaw does not fail the test on an RPC error; a hidden tool may surface
// as either an error result or a protocol error.
func callToolRaw(t *testing.T, s *mcpsdk.ClientSession, name string, args any) (*mcpsdk.CallToolResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
}

func denied(t *testing.T, res *mcpsdk.CallToolResult, err error) bool {
	t.Helper()
	if err != nil {
		return true
	}
	return res != nil && res.IsError
}

func agentVisibleTools(role domain.AgentRole) map[string]bool {
	out := map[string]bool{}
	for name, allowed := range agentTools[role] {
		if allowed {
			out[name] = true
		}
	}
	// Methodology tools are visible to every token; publish and activate check
	// the effective role permission in their handlers.
	for name := range methodologyTools {
		out[name] = true
	}
	return out
}

func TestToolVisibilityFollowsScope(t *testing.T) {
	env := newAgentScopeEnv(t)
	cases := []struct {
		name  string
		token string
		want  map[string]bool
	}{
		{"default", env.userToken, allTools()},
		{"admin", env.adminToken, allTools()},
		{"forager", env.foragerToken, agentVisibleTools(domain.RoleForager)},
		{"queen", env.queenToken, agentVisibleTools(domain.RoleQueen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := env.toolNames(t, tc.token)
			for _, name := range allToolNames {
				if got[name] != tc.want[name] {
					t.Errorf("tool %s visible=%v, want %v", name, got[name], tc.want[name])
				}
			}
		})
	}
}

func TestAgentScopePermissionTable(t *testing.T) {
	base := domain.Token{Scope: domain.TokenAgent, AgentID: "agent-1"}
	for _, role := range []domain.AgentRole{domain.RoleForager, domain.RoleQueen} {
		t.Run(string(role), func(t *testing.T) {
			agent := domain.Agent{ID: "agent-1", HiveID: "hive-1", Role: role}
			ts := &toolset{token: base, actor: actor{token: base, agent: &agent}}
			for _, tool := range allToolNames {
				err := ts.authorize(tool)
				wantAllowed := agentTools[role][tool] || methodologyTools[tool]
				if allowed := err == nil; allowed != wantAllowed {
					t.Errorf("%s %s: allowed=%v, want %v (err=%v)", role, tool, allowed, wantAllowed, err)
				}
			}
		})
	}

	// A default token may read but not configure Hives; an admin token may do
	// everything.
	for _, tc := range []struct {
		scope domain.TokenScope
		tool  string
		want  bool
	}{
		{domain.TokenDefault, "list_hives", true},
		{domain.TokenDefault, "spawn_forager", true},
		{domain.TokenDefault, "create_hive", false},
		{domain.TokenDefault, "update_hive", false},
		{domain.TokenAdmin, "create_hive", true},
		{domain.TokenAdmin, "update_hive", true},
	} {
		ts := &toolset{token: domain.Token{Scope: tc.scope}}
		if got := ts.authorize(tc.tool) == nil; got != tc.want {
			t.Errorf("scope %s tool %s: allowed=%v, want %v", tc.scope, tc.tool, got, tc.want)
		}
	}
}

func TestForagerScopeTools(t *testing.T) {
	env := newAgentScopeEnv(t)
	session := env.connect(env.foragerToken)

	t.Run("send uses the agent address", func(t *testing.T) {
		out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
			"hiveId": env.hive.ID, "body": "hello Hive",
		}))
		if out.Message.From != string(domain.ForagerAddress(env.forager.ID)) {
			t.Fatalf("from = %q, want the forager address", out.Message.From)
		}
		// This Hive has no Queen and the Forager has no task message, so the
		// report goes to the Hive Inbox instead of an unread Queen Inbox.
		if out.Message.To != string(domain.AddressHive) {
			t.Fatalf("to = %q, want hive", out.Message.To)
		}
		local := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
			"hiveId": env.hive.ID, "to": "local:" + env.user.ID, "body": "to the human",
		}))
		if local.Message.To != string(domain.LocalAddress(env.user.ID)) {
			t.Fatalf("to = %q, want the local address", local.Message.To)
		}
	})

	t.Run("forager to forager is allowed without a methodology", func(t *testing.T) {
		out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
			"hiveId": env.hive.ID, "to": "forager:" + env.peer.ID, "body": "peer message",
		}))
		if out.Message.Trust != string(domain.TrustAgent) {
			t.Fatalf("trust = %q, want agent", out.Message.Trust)
		}
	})

	t.Run("hive confinement", func(t *testing.T) {
		res := callTool(t, session, "send", map[string]any{"hiveId": env.otherHive.ID, "body": "leak"})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
			t.Fatalf("cross-hive send: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
		if res, err := callToolRaw(t, session, "inbox", map[string]any{"hiveId": env.otherHive.ID}); !denied(t, res, err) {
			t.Fatalf("cross-hive inbox was allowed")
		}
	})

	t.Run("get_forager is self only", func(t *testing.T) {
		self := decode[getForagerOutput](t, callTool(t, session, "get_forager", map[string]any{"agentId": env.forager.ID}))
		if self.Forager.ID != env.forager.ID {
			t.Fatalf("get_forager = %+v", self.Forager)
		}
		res := callTool(t, session, "get_forager", map[string]any{"agentId": env.peer.ID})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access itself") {
			t.Fatalf("other forager: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})

	t.Run("inbox is the forager's own", func(t *testing.T) {
		ctx := context.Background()
		if _, err := env.backend.SendMessage(ctx, domain.Message{
			HiveID: env.hive.ID, From: domain.AddressQueen, To: domain.ForagerAddress(env.forager.ID),
			Kind: domain.MessageReply, Body: "turn input",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := env.backend.SendMessage(ctx, domain.Message{
			HiveID: env.hive.ID, From: domain.AddressSystem, To: domain.AddressHive,
			Kind: domain.MessageEvent, Body: "hive event",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := env.backend.SendMessage(ctx, domain.Message{
			HiveID: env.hive.ID, From: domain.AddressQueen, To: domain.ForagerAddress(env.peer.ID),
			Kind: domain.MessageReply, Body: "peer input",
		}); err != nil {
			t.Fatal(err)
		}
		out := decode[inboxOutput](t, callTool(t, session, "inbox", map[string]any{"hiveId": env.hive.ID}))
		if len(out.Messages) != 1 || out.Messages[0].Body != "turn input" {
			t.Fatalf("inbox = %+v", out.Messages)
		}
	})

	t.Run("update_handoff is self only and capped", func(t *testing.T) {
		long := strings.Repeat("x", domain.MaxMessageBody+100)
		out := decode[updateHandoffOutput](t, callTool(t, session, "update_handoff", map[string]any{
			"agentId": env.forager.ID,
			"goal":    "ship the fix",
			"done":    long,
			"next":    "open the PR",
		}))
		if out.Forager.Handoff == nil || out.Forager.Handoff.Goal != "ship the fix" {
			t.Fatalf("handoff = %+v", out.Forager.Handoff)
		}
		if !strings.Contains(out.Forager.Handoff.Done, "[truncated:") {
			t.Fatalf("long handoff field was not truncated: %d chars", len(out.Forager.Handoff.Done))
		}
		res := callTool(t, session, "update_handoff", map[string]any{"agentId": env.peer.ID, "goal": "hijack"})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access itself") {
			t.Fatalf("other handoff: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})

	t.Run("tools outside the table are rejected", func(t *testing.T) {
		for _, tool := range []string{"list_hives", "get_hive", "create_hive", "update_hive", "wait", "spawn_forager", "cancel_forager", "read_forager_detail", "claim", "offline_digest"} {
			res, err := callToolRaw(t, session, tool, map[string]any{"hiveId": env.hive.ID})
			if !denied(t, res, err) {
				t.Errorf("%s was allowed for a forager token", tool)
			}
		}
	})
}

func TestQueenScopeTools(t *testing.T) {
	env := newAgentScopeEnv(t)
	session := env.connect(env.queenToken)

	t.Run("spawn within the hive", func(t *testing.T) {
		out := decode[spawnOutput](t, callTool(t, session, "spawn", map[string]any{
			"hiveId": env.hive.ID, "task": "queen delegated work", "role": "worker",
		}))
		if out.Message.From != string(domain.AddressQueen) {
			t.Fatalf("from = %q, want queen", out.Message.From)
		}
		if out.Agent.HiveID != env.hive.ID {
			t.Fatalf("agent hive = %q", out.Agent.HiveID)
		}
		if out.Agent.ParentID != env.queen.ID {
			t.Fatalf("parent = %q, want %q", out.Agent.ParentID, env.queen.ID)
		}
	})

	t.Run("spawn outside the hive is denied", func(t *testing.T) {
		res := callTool(t, session, "spawn", map[string]any{"hiveId": env.otherHive.ID, "task": "nope"})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
			t.Fatalf("cross-hive spawn: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})

	t.Run("send uses the queen address", func(t *testing.T) {
		out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
			"hiveId": env.hive.ID, "to": "forager:" + env.forager.ID, "body": "please look",
		}))
		if out.Message.From != string(domain.AddressQueen) {
			t.Fatalf("from = %q, want queen", out.Message.From)
		}
	})

	t.Run("forager targets stay in the hive", func(t *testing.T) {
		got := decode[getForagerOutput](t, callTool(t, session, "get_forager", map[string]any{"agentId": env.peer.ID}))
		if got.Forager.ID != env.peer.ID {
			t.Fatalf("get_forager = %+v", got.Forager)
		}
		res := callTool(t, session, "get_forager", map[string]any{"agentId": env.otherForager.ID})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
			t.Fatalf("cross-hive get: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
		detail := decode[readForagerDetailOutput](t, callTool(t, session, "read_forager_detail", map[string]any{"agentId": env.peer.ID}))
		if detail.Forager.ID != env.peer.ID {
			t.Fatalf("read_forager_detail = %+v", detail.Forager)
		}
		res = callTool(t, session, "read_forager_detail", map[string]any{"agentId": env.otherForager.ID})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
			t.Fatalf("cross-hive detail: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
		cancelled := decode[cancelForagerOutput](t, callTool(t, session, "cancel_forager", map[string]any{"agentId": env.peer.ID}))
		if cancelled.Message.Kind != string(domain.MessageControl) || cancelled.Message.Body != "cancel" {
			t.Fatalf("cancel message = %+v", cancelled.Message)
		}
		if cancelled.Message.From != string(domain.AddressQueen) ||
			cancelled.Message.To != string(domain.ForagerAddress(env.peer.ID)) {
			t.Fatalf("cancel message addresses = %+v", cancelled.Message)
		}
		if cancelled.Forager.State == string(domain.AgentCancelled) {
			t.Fatalf("cancel_forager changed the state directly: %+v", cancelled.Forager)
		}
		res = callTool(t, session, "cancel_forager", map[string]any{"agentId": env.otherForager.ID})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
			t.Fatalf("cross-hive cancel: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})

	t.Run("hive inbox and claim", func(t *testing.T) {
		ctx := context.Background()
		if _, err := env.backend.SendMessage(ctx, domain.Message{
			HiveID: env.hive.ID, From: domain.AddressSystem, To: domain.AddressHive,
			Kind: domain.MessageEvent, Body: "unrouted event",
		}); err != nil {
			t.Fatal(err)
		}
		inbox := decode[inboxOutput](t, callTool(t, session, "inbox", map[string]any{"hiveId": env.hive.ID}))
		if len(inbox.Messages) != 1 || inbox.Messages[0].Body != "unrouted event" {
			t.Fatalf("inbox = %+v", inbox.Messages)
		}
		claimed := decode[claimOutput](t, callTool(t, session, "claim", map[string]any{
			"hiveId": env.hive.ID, "messageId": inbox.Messages[0].ID,
		}))
		if !claimed.Claimed || claimed.Owner != string(domain.AddressQueen) {
			t.Fatalf("claim = %+v", claimed)
		}
		res := callTool(t, session, "inbox", map[string]any{"hiveId": env.otherHive.ID})
		if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
			t.Fatalf("cross-hive inbox: isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})

	t.Run("tools outside the table are rejected", func(t *testing.T) {
		for _, tool := range []string{"list_hives", "get_hive", "create_hive", "update_hive", "wait", "update_handoff", "offline_digest"} {
			res, err := callToolRaw(t, session, tool, map[string]any{"hiveId": env.hive.ID})
			if !denied(t, res, err) {
				t.Errorf("%s was allowed for a queen token", tool)
			}
		}
	})
}

func TestCancelForagerSendsControlMessage(t *testing.T) {
	env := newAgentScopeEnv(t)
	ctx := context.Background()
	cases := []struct {
		name  string
		token string
		from  domain.Address
	}{
		{"default", env.userToken, domain.LocalAddress(env.user.ID)},
		{"admin", env.adminToken, domain.LocalAddress(env.admin.ID)},
		{"queen", env.queenToken, domain.AddressQueen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forager, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: env.hive.ID, Task: "cancel me"})
			if err != nil {
				t.Fatalf("CreateForager: %v", err)
			}
			session := env.connect(tc.token)
			out := decode[cancelForagerOutput](t, callTool(t, session, "cancel_forager", map[string]any{"agentId": forager.ID}))
			if out.Message.Kind != string(domain.MessageControl) || out.Message.Body != "cancel" {
				t.Fatalf("cancel message = %+v", out.Message)
			}
			if out.Message.From != string(tc.from) || out.Message.To != string(domain.ForagerAddress(forager.ID)) {
				t.Fatalf("cancel message addresses = %+v", out.Message)
			}
			if out.Forager.State == string(domain.AgentCancelled) {
				t.Fatalf("cancel_forager transitioned the state directly: %+v", out.Forager)
			}
			msgs, err := env.backend.Inbox(ctx, env.hive.ID, domain.ForagerAddress(forager.ID), "", 10)
			if err != nil {
				t.Fatal(err)
			}
			controls := 0
			for _, m := range msgs {
				if m.Kind != domain.MessageControl {
					continue
				}
				controls++
				if m.Body != "cancel" || m.From != tc.from {
					t.Fatalf("control message = %+v", m)
				}
			}
			if controls != 1 {
				t.Fatalf("control messages = %d, want 1 (%+v)", controls, msgs)
			}
			// Once the supervisor has driven the Forager terminal, a second
			// request is rejected instead of being queued.
			if err := env.backend.TransitionAgent(ctx, forager.ID, domain.AgentCreating, domain.AgentCancelled); err != nil {
				t.Fatal(err)
			}
			res := callTool(t, session, "cancel_forager", map[string]any{"agentId": forager.ID})
			if !res.IsError || !strings.Contains(toolErrorText(res), "terminal") {
				t.Fatalf("terminal cancel: isError=%v text=%q", res.IsError, toolErrorText(res))
			}
		})
	}
}

func TestRequestIDMakesSpawnAndSendIdempotent(t *testing.T) {
	env := newAgentScopeEnv(t)
	session := env.connect(env.userToken)
	ctx := context.Background()

	spawn := func(task string) *mcpsdk.CallToolResult {
		return callTool(t, session, "spawn_forager", map[string]any{
			"hiveId": env.hive.ID, "task": task, "requestId": "spawn-1",
		})
	}
	first := decode[spawnForagerOutput](t, spawn("idempotent task"))
	second := decode[spawnForagerOutput](t, spawn("idempotent task"))
	if first.Forager.ID != second.Forager.ID {
		t.Fatalf("forager ids differ: %s vs %s", first.Forager.ID, second.Forager.ID)
	}
	if first.Message.ID != second.Message.ID {
		t.Fatalf("task message ids differ: %s vs %s", first.Message.ID, second.Message.ID)
	}
	foragers, err := env.backend.Foragers(ctx, env.hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, a := range foragers {
		if a.ID == first.Forager.ID {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("forager stored %d times", created)
	}
	tasks, err := env.backend.Inbox(ctx, env.hive.ID, domain.ForagerAddress(first.Forager.ID), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != first.Message.ID {
		t.Fatalf("task messages = %+v", tasks)
	}

	conflict := spawn("a different task")
	if !conflict.IsError || !strings.Contains(toolErrorText(conflict), "conflict") {
		t.Fatalf("reusing requestId with different content: isError=%v text=%q", conflict.IsError, toolErrorText(conflict))
	}

	send := func(body string) *mcpsdk.CallToolResult {
		return callTool(t, session, "send", map[string]any{
			"hiveId": env.hive.ID, "to": "queen", "body": body, "requestId": "send-1",
		})
	}
	firstSend := decode[sendOutput](t, send("ping"))
	secondSend := decode[sendOutput](t, send("ping"))
	if firstSend.Message.ID != secondSend.Message.ID {
		t.Fatalf("message ids differ: %s vs %s", firstSend.Message.ID, secondSend.Message.ID)
	}
	queenInbox, err := env.backend.Inbox(ctx, env.hive.ID, domain.AddressQueen, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	pings := 0
	for _, m := range queenInbox {
		if m.Body == "ping" {
			pings++
		}
	}
	if pings != 1 {
		t.Fatalf("stored %d identical send messages", pings)
	}
	if res := send("pong"); !res.IsError || !strings.Contains(toolErrorText(res), "conflict") {
		t.Fatalf("reusing send requestId with different content: isError=%v text=%q", res.IsError, toolErrorText(res))
	}
}

func TestWaitTimeoutAndParentCancellation(t *testing.T) {
	backend := NewMemoryBackend()
	ctx := context.Background()
	hive, err := backend.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/alpha"})
	if err != nil {
		t.Fatal(err)
	}
	plain, token, err := backend.CreateToken(ctx, "user", domain.TokenDefault)
	if err != nil {
		t.Fatal(err)
	}
	_ = plain
	ts := &toolset{backend: backend, opts: Options{MaxWait: 50 * time.Millisecond}.withDefaults(), token: token}
	ts.actor, ts.actorErr = resolveActor(ctx, backend, token)

	// Our own poll timeout is a successful timedOut response.
	_, out, err := ts.wait(ctx, nil, waitInput{HiveID: hive.ID})
	if err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if !out.TimedOut {
		t.Fatalf("expected timedOut, got %+v", out)
	}

	// A cancelled parent must surface as cancellation, not as a timeout.
	parent, cancel := context.WithCancel(context.Background())
	cancelled := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
		close(cancelled)
	}()
	_, _, err = ts.wait(parent, nil, waitInput{HiveID: hive.ID, TimeoutSeconds: 30})
	<-cancelled
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait error = %v, want context.Canceled", err)
	}
}

func TestMCPEndToEndHandshakeListToolsSpawn(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("alpha", "acme/alpha")
	session := env.connect(env.userToken)

	// connect performs the initialize handshake.
	if session.InitializeResult() == nil {
		t.Fatal("no initialize result")
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != len(allToolNames) {
		t.Fatalf("listed %d tools, want %d", len(tools.Tools), len(allToolNames))
	}
	out := decode[spawnForagerOutput](t, callTool(t, session, "spawn_forager", map[string]any{
		"hiveId": h.ID, "task": "end to end",
	}))
	if out.Forager.ID == "" || out.Message.To != string(domain.ForagerAddress(out.Forager.ID)) {
		t.Fatalf("spawn = %+v", out)
	}
}
