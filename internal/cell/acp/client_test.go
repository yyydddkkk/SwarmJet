package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent is an in-process ACP agent used to exercise the client without an
// OpenCode install, a container or a model.
type fakeAgent struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader

	mu                 sync.Mutex
	permissionRequests int
	lastPermission     *PermissionResponse
}

func newFakeAgentPair(t *testing.T) (*Client, *fakeAgent) {
	t.Helper()
	clientEnd, agentEnd := net.Pipe()
	agent := &fakeAgent{t: t, conn: agentEnd, br: bufio.NewReader(agentEnd)}
	go agent.serve()
	client := NewClient(clientEnd, WithPermissionHandler(func(_ context.Context, req PermissionRequest) (PermissionOutcome, error) {
		agent.mu.Lock()
		agent.permissionRequests++
		agent.mu.Unlock()
		if opt, ok := req.Select(PermissionAllowOnce, PermissionAllowAlways); ok {
			return Selected(opt), nil
		}
		return Cancelled(), nil
	}))
	t.Cleanup(func() { _ = client.Close() })
	return client, agent
}

func (a *fakeAgent) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		a.t.Errorf("fake agent marshal: %v", err)
		return
	}
	b = append(b, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.conn.Write(b); err != nil {
		// The client closed the pipe; the test is finishing.
		return
	}
}

func (a *fakeAgent) sendResult(id json.RawMessage, result any) {
	a.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

func (a *fakeAgent) sendRequest(id json.RawMessage, method string, params any) {
	a.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "method": method, "params": params})
}

func (a *fakeAgent) notify(method string, params any) {
	a.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (a *fakeAgent) read() (rpcMessage, bool) {
	line, err := a.br.ReadBytes('\n')
	if err != nil {
		return rpcMessage{}, false
	}
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return rpcMessage{}, false
	}
	return msg, true
}

func (a *fakeAgent) serve() {
	for {
		msg, ok := a.read()
		if !ok {
			return
		}
		if msg.Method == "" {
			continue
		}
		switch msg.Method {
		case MethodInitialize:
			a.sendResult(msg.ID, InitializeResult{
				ProtocolVersion: ProtocolVersion,
				AgentCapabilities: AgentCapabilities{
					LoadSession:     true,
					MCPCapabilities: &MCPCapabilities{HTTP: true},
				},
				AgentInfo: &Implementation{Name: "fake", Version: "0.0.1"},
			})
		case MethodSessionNew:
			a.sendResult(msg.ID, NewSessionResult{SessionID: "ses_fake"})
		case MethodSessionLoad:
			a.notify(MethodSessionUpdate, SessionNotification{
				SessionID: "ses_fake",
				Update:    json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"replayed"}}`),
			})
			a.sendResult(msg.ID, LoadSessionResult{})
		case MethodSessionPrompt:
			a.handlePrompt(msg)
		}
	}
}

func (a *fakeAgent) handlePrompt(msg rpcMessage) {
	var p PromptParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		a.t.Errorf("fake agent prompt params: %v", err)
		return
	}
	text := ""
	if len(p.Prompt) > 0 {
		text = p.Prompt[0].Text
	}
	a.notify(MethodSessionUpdate, SessionNotification{
		SessionID: p.SessionID,
		Update:    json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"po"}}`),
	})
	if strings.Contains(text, "perm") {
		reqID := json.RawMessage(`9001`)
		a.sendRequest(reqID, MethodRequestPermission, PermissionRequest{
			SessionID: p.SessionID,
			ToolCall:  SessionUpdate{ToolCallID: "call_1", Title: "bash", Kind: "execute", Status: ToolStatusPending},
			Options: []PermissionOption{
				{OptionID: "once", Kind: PermissionAllowOnce, Name: "Allow once"},
				{OptionID: "reject", Kind: PermissionRejectOnce, Name: "Reject"},
			},
		})
		for {
			reply, ok := a.read()
			if !ok {
				return
			}
			if reply.Method == "" && rpcIDKey(reply.ID) == rpcIDKey(reqID) {
				var response PermissionResponse
				if err := json.Unmarshal(reply.Result, &response); err == nil {
					a.mu.Lock()
					a.lastPermission = &response
					a.mu.Unlock()
				}
				break
			}
		}
	}
	if strings.Contains(text, "slow") {
		for {
			reply, ok := a.read()
			if !ok {
				return
			}
			if reply.Method == MethodSessionCancel {
				a.sendResult(msg.ID, PromptResult{StopReason: StopCancelled})
				return
			}
		}
	}
	a.notify(MethodSessionUpdate, SessionNotification{
		SessionID: p.SessionID,
		Update:    json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"ng"}}`),
	})
	a.sendResult(msg.ID, PromptResult{StopReason: StopEndTurn, Usage: &Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}})
}

// updateRecorder collects streamed session updates.
type updateRecorder struct {
	mu      sync.Mutex
	updates []SessionUpdate
	ch      chan SessionUpdate
}

func newUpdateRecorder() *updateRecorder {
	return &updateRecorder{ch: make(chan SessionUpdate, 128)}
}

func (r *updateRecorder) sink(n SessionNotification) error {
	u, err := n.Decode()
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.updates = append(r.updates, u)
	r.mu.Unlock()
	select {
	case r.ch <- u:
	default:
	}
	return nil
}

func (r *updateRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, u := range r.updates {
		if u.SessionUpdate == UpdateAgentMessageChunk {
			b.WriteString(u.Text())
		}
	}
	return b.String()
}

func (r *updateRecorder) waitFor(pred func(SessionUpdate) bool, d time.Duration) (SessionUpdate, bool) {
	deadline := time.After(d)
	for {
		select {
		case u := <-r.ch:
			if pred(u) {
				return u, true
			}
		case <-deadline:
			return SessionUpdate{}, false
		}
	}
}

func TestInitializeNewSessionAndPromptStreaming(t *testing.T) {
	client, _ := newFakeAgentPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	init, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if !init.AgentCapabilities.LoadSession {
		t.Fatal("fake agent should advertise loadSession")
	}
	session, err := client.NewSession(ctx, "/workspace", nil)
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if session.SessionID != "ses_fake" {
		t.Fatalf("session id = %q", session.SessionID)
	}

	rec := newUpdateRecorder()
	result, err := client.Prompt(ctx, session.SessionID, []ContentBlock{TextBlock("ping")}, rec.sink)
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if result.StopReason != StopEndTurn {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if got := rec.text(); got != "pong" {
		t.Fatalf("streamed text = %q, want pong", got)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestLoadSessionReplaysHistory(t *testing.T) {
	client, _ := newFakeAgentPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	rec := newUpdateRecorder()
	if _, err := client.LoadSession(ctx, "ses_fake", "/workspace", nil, rec.sink); err != nil {
		t.Fatalf("session/load: %v", err)
	}
	if got := rec.text(); got != "replayed" {
		t.Fatalf("replayed text = %q, want replayed", got)
	}
}

func TestCancelInterruptsTurnAndClientStaysUsable(t *testing.T) {
	client, _ := newFakeAgentPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, "/workspace", nil)
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result PromptResult
		err    error
	}
	done := make(chan outcome, 1)
	rec := newUpdateRecorder()
	go func() {
		result, err := client.Prompt(ctx, session.SessionID, []ContentBlock{TextBlock("slow turn")}, rec.sink)
		done <- outcome{result, err}
	}()
	if _, ok := rec.waitFor(func(u SessionUpdate) bool { return u.SessionUpdate == UpdateAgentMessageChunk }, 2*time.Second); !ok {
		t.Fatal("no streamed chunk before cancel")
	}
	if err := client.Cancel(ctx, session.SessionID); err != nil {
		t.Fatalf("session/cancel: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("prompt after cancel: %v", got.err)
		}
		if got.result.StopReason != StopCancelled {
			t.Fatalf("stopReason = %q, want cancelled", got.result.StopReason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled prompt did not return")
	}

	// The same client and session must keep working after a cancel.
	follow := newUpdateRecorder()
	result, err := client.Prompt(ctx, session.SessionID, []ContentBlock{TextBlock("ping")}, follow.sink)
	if err != nil {
		t.Fatalf("prompt after cancel: %v", err)
	}
	if result.StopReason != StopEndTurn || follow.text() != "pong" {
		t.Fatalf("reuse result = %+v text = %q", result, follow.text())
	}
}

func TestPermissionRequestUsesHandler(t *testing.T) {
	client, agent := newFakeAgentPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, "/workspace", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := newUpdateRecorder()
	result, err := client.Prompt(ctx, session.SessionID, []ContentBlock{TextBlock("perm please")}, rec.sink)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.StopReason != StopEndTurn {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.permissionRequests != 1 {
		t.Fatalf("handler called %d times, want 1", agent.permissionRequests)
	}
	if agent.lastPermission == nil || agent.lastPermission.Outcome.Outcome != "selected" || agent.lastPermission.Outcome.OptionID != "once" {
		t.Fatalf("agent saw %+v, want selected/once", agent.lastPermission)
	}
}

func TestPermissionWithoutHandlerIsCancelled(t *testing.T) {
	clientEnd, agentEnd := net.Pipe()
	defer clientEnd.Close()
	defer agentEnd.Close()
	client := NewClient(clientEnd)
	var req PermissionRequest
	if err := json.Unmarshal([]byte(permissionParamsJSON), &req); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := client.handleRequest(MethodRequestPermission, raw)
	if rpcErr != nil {
		t.Fatalf("handleRequest error: %v", rpcErr)
	}
	response, ok := result.(PermissionResponse)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if response.Outcome.Outcome != "cancelled" {
		t.Fatalf("outcome = %+v, want cancelled", response.Outcome)
	}
}

func TestUnknownServerRequestReturnsMethodNotFound(t *testing.T) {
	clientEnd, agentEnd := net.Pipe()
	defer clientEnd.Close()
	defer agentEnd.Close()
	client := NewClient(clientEnd)
	_, rpcErr := client.handleRequest("fs/read_text_file", json.RawMessage(`{}`))
	if rpcErr == nil {
		t.Fatal("expected an error for an unhandled method")
	}
	if rpcErr.Code != CodeMethodNotFound {
		t.Fatalf("code = %d, want %d", rpcErr.Code, CodeMethodNotFound)
	}
}

func TestCallsCorrelateOutOfOrder(t *testing.T) {
	clientEnd, agentEnd := net.Pipe()
	defer agentEnd.Close()

	go func() {
		br := bufio.NewReader(agentEnd)
		type pending struct {
			id json.RawMessage
			i  int
		}
		var seen []pending
		for len(seen) < 2 {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var msg rpcMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				return
			}
			var params struct {
				I int `json:"i"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			seen = append(seen, pending{id: msg.ID, i: params.I})
		}
		// Answer newest-first so a client that matched by arrival order would
		// hand the wrong payload to the wrong caller.
		for i := len(seen) - 1; i >= 0; i-- {
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      seen[i].id,
				"result":  map[string]any{"i": seen[i].i},
			})
			if _, err := agentEnd.Write(append(body, '\n')); err != nil {
				return
			}
		}
	}()

	client := NewClient(clientEnd)
	defer client.Close()
	results := make([]int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, err := client.conn.call(context.Background(), "echo", map[string]any{"i": i})
			if err != nil {
				t.Errorf("call %d: %v", i, err)
				return
			}
			var p struct {
				I int `json:"i"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Errorf("decode %d: %v", i, err)
				return
			}
			results[i] = p.I
		}(i)
	}
	wg.Wait()
	if results[0] != 0 || results[1] != 1 {
		t.Fatalf("responses misrouted: %v", results)
	}
}

func TestCallContextCancellation(t *testing.T) {
	clientEnd, agentEnd := net.Pipe()
	defer agentEnd.Close()
	// The agent reads requests but never answers.
	go func() {
		br := bufio.NewReader(agentEnd)
		for {
			if _, err := br.ReadBytes('\n'); err != nil {
				return
			}
		}
	}()
	client := NewClient(clientEnd)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Initialize(ctx); err == nil {
		t.Fatal("expected initialize to fail with a cancelled context")
	}
}

func TestProcessCloseIsIdempotent(t *testing.T) {
	// `cat` echoes, so it stands in for a stdio CLI without any dependency.
	proc, err := StartLocal(context.Background(), "cat", nil, nil, "")
	if err != nil {
		t.Skipf("cat unavailable: %v", err)
	}
	if _, err := proc.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len("hello\n"))
	if _, err := proc.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "hello\n" {
		t.Fatalf("read %q", buf)
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := proc.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if !proc.Exited() {
		t.Fatal("process should have exited")
	}
}

func TestProcessStderrIsCaptured(t *testing.T) {
	proc, err := StartLocal(context.Background(), "sh", []string{"-c", "echo boom >&2; exit 3"}, nil, "")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	_ = proc.Wait()
	if !strings.Contains(proc.Stderr(), "boom") {
		t.Fatalf("stderr = %q, want boom", proc.Stderr())
	}
	if proc.Exited() != true {
		t.Fatal("process should report exited")
	}
	_ = proc.Close()
}
