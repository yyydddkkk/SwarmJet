package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The JSON fixtures below were captured from opencode 1.18.31 while running the
// spike (see docs/claude/spikes/opencode-driver.md). They contain no secrets.

const initializeResultJSON = `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,` +
	`"mcpCapabilities":{"http":true,"sse":true},` +
	`"promptCapabilities":{"embeddedContext":true,"image":true},` +
	`"sessionCapabilities":{"close":{},"fork":{},"list":{},"resume":{}}},` +
	`"authMethods":[{"description":"Run opencode auth login in the terminal","name":"Login with opencode","id":"opencode-login"}],` +
	`"agentInfo":{"name":"OpenCode","version":"1.18.31"}}`

const promptResultJSON = `{"stopReason":"end_turn","usage":{"inputTokens":3381,` +
	`"outputTokens":3,"totalTokens":9536,"thoughtTokens":8,"cachedReadTokens":6144},"_meta":{}}`

const cancelledPromptResultJSON = `{"stopReason":"cancelled","_meta":{}}`

const permissionParamsJSON = `{"sessionId":"ses_ebc20527affe24GdWWv6aXZhdB",` +
	`"toolCall":{"toolCallId":"call_e8b02d7c588245c2bf89040a","title":"echo permission-check",` +
	`"kind":"execute","status":"pending","rawInput":{"command":"echo permission-check"}},` +
	`"options":[{"optionId":"once","kind":"allow_once","name":"Allow once"},` +
	`{"optionId":"always","kind":"allow_always","name":"Always allow"},` +
	`{"optionId":"reject","kind":"reject_once","name":"Reject"}]}`

const agentMessageChunkJSON = `{"sessionId":"ses_ebc219fe7ffeZRd3tY90SQd3DY",` +
	`"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OK"}}}`

const toolCallUpdateJSON = `{"sessionId":"ses_x","update":{"sessionUpdate":"tool_call_update",` +
	`"toolCallId":"call_16a4bbef4f36419c85432073","status":"completed","title":"echo hello-from-tool",` +
	`"kind":"execute","rawOutput":{"output":"hello-from-tool\n","metadata":{"exit":0}}}}`

func TestDecodeInitializeResult(t *testing.T) {
	var res InitializeResult
	if err := json.Unmarshal([]byte(initializeResultJSON), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.ProtocolVersion != 1 {
		t.Fatalf("protocolVersion = %d, want 1", res.ProtocolVersion)
	}
	if !res.AgentCapabilities.LoadSession {
		t.Fatal("loadSession = false, want true; session/load depends on it")
	}
	if res.AgentCapabilities.MCPCapabilities == nil || !res.AgentCapabilities.MCPCapabilities.HTTP {
		t.Fatal("mcpCapabilities.http = false, want true")
	}
	if res.AgentInfo == nil || res.AgentInfo.Name == "" || res.AgentInfo.Version == "" {
		t.Fatalf("agentInfo = %+v, want name and version", res.AgentInfo)
	}
	if len(res.AuthMethods) != 1 || res.AuthMethods[0].ID != "opencode-login" {
		t.Fatalf("authMethods = %+v", res.AuthMethods)
	}
}

func TestDecodePromptResult(t *testing.T) {
	var res PromptResult
	if err := json.Unmarshal([]byte(promptResultJSON), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.StopReason != StopEndTurn {
		t.Fatalf("stopReason = %q, want %q", res.StopReason, StopEndTurn)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 9536 {
		t.Fatalf("usage = %+v, want totalTokens 9536", res.Usage)
	}
	var cancelled PromptResult
	if err := json.Unmarshal([]byte(cancelledPromptResultJSON), &cancelled); err != nil {
		t.Fatalf("unmarshal cancelled: %v", err)
	}
	if cancelled.StopReason != StopCancelled {
		t.Fatalf("stopReason = %q, want %q", cancelled.StopReason, StopCancelled)
	}
}

func TestDecodeSessionNotifications(t *testing.T) {
	var n SessionNotification
	if err := json.Unmarshal([]byte(agentMessageChunkJSON), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.SessionID == "" {
		t.Fatal("sessionId is empty")
	}
	u, err := n.Decode()
	if err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if u.SessionUpdate != UpdateAgentMessageChunk {
		t.Fatalf("sessionUpdate = %q", u.SessionUpdate)
	}
	if got := u.Text(); got != "OK" {
		t.Fatalf("text = %q, want OK", got)
	}

	var tool SessionNotification
	if err := json.Unmarshal([]byte(toolCallUpdateJSON), &tool); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	tu, err := tool.Decode()
	if err != nil {
		t.Fatalf("decode tool: %v", err)
	}
	if tu.Status != ToolStatusCompleted || tu.ToolCallID == "" {
		t.Fatalf("tool update = %+v", tu)
	}
	if !strings.Contains(string(tu.RawOutput), "hello-from-tool") {
		t.Fatalf("rawOutput = %s", tu.RawOutput)
	}
	if tu.Text() != "" {
		t.Fatalf("text = %q, want empty for a tool update", tu.Text())
	}
}

func TestPermissionRequestSelect(t *testing.T) {
	var req PermissionRequest
	if err := json.Unmarshal([]byte(permissionParamsJSON), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.SessionID == "" || req.ToolCall.Title == "" || len(req.Options) != 3 {
		t.Fatalf("request = %+v", req)
	}
	opt, ok := req.Select(PermissionAllowAlways)
	if !ok || opt.OptionID != "always" {
		t.Fatalf("select always = %+v, %v", opt, ok)
	}
	opt, ok = req.Select(PermissionRejectAlways, PermissionAllowOnce)
	if !ok || opt.OptionID != "once" {
		t.Fatalf("select fallback = %+v, %v", opt, ok)
	}
	out := Selected(opt)
	if out.Outcome != "selected" || out.OptionID != "once" {
		t.Fatalf("outcome = %+v", out)
	}
	if Cancelled().Outcome != "cancelled" {
		t.Fatal("Cancelled() should report outcome cancelled")
	}
	empty := PermissionRequest{}
	if _, ok := empty.Select(PermissionAllowOnce); ok {
		t.Fatal("Select on a request with no options must report false")
	}
}

func TestNewSessionParamsAlwaysMarshalsMCPArray(t *testing.T) {
	raw, err := json.Marshal(NewSessionParams{Cwd: "/workspace", McpServers: normalizeMCP(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"mcpServers":[]`) {
		t.Fatalf("mcpServers must be an array, got %s", raw)
	}
	raw, err = json.Marshal(NewSessionParams{Cwd: "/workspace", McpServers: normalizeMCP([]MCPServer{{
		Name:    "swarmjet",
		Command: "python3",
		Args:    []string{"/opt/mcp.py"},
		Env:     []EnvVariable{{Name: "A", Value: "B"}},
	}})})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"name":"swarmjet"`) || !strings.Contains(string(raw), `"A"`) {
		t.Fatalf("stdio MCP server not encoded: %s", raw)
	}

	// OpenCode rejects a stdio server that omits args/env, so both must always
	// be encoded as arrays even when empty.
	raw, err = json.Marshal(NewSessionParams{Cwd: "/workspace", McpServers: normalizeMCP([]MCPServer{{
		Name:    "swarmjet",
		Command: "python3",
	}})})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"args":[]`) || !strings.Contains(string(raw), `"env":[]`) {
		t.Fatalf("stdio MCP server must encode empty args and env arrays: %s", raw)
	}
	if strings.Contains(string(raw), `"type"`) || strings.Contains(string(raw), `"url"`) {
		t.Fatalf("stdio MCP server must not carry http fields: %s", raw)
	}

	raw, err = json.Marshal(NewSessionParams{Cwd: "/workspace", McpServers: normalizeMCP([]MCPServer{{
		Name: "swarmjet",
		Type: "http",
		URL:  "http://127.0.0.1:8090/mcp",
	}})})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"type":"http"`) || !strings.Contains(string(raw), `"url":"http://127.0.0.1:8090/mcp"`) {
		t.Fatalf("http MCP server not encoded: %s", raw)
	}
	if strings.Contains(string(raw), `"command"`) {
		t.Fatalf("http MCP server must not carry a command: %s", raw)
	}
}

func TestTextBlock(t *testing.T) {
	raw, err := json.Marshal(PromptParams{SessionID: "ses_1", Prompt: []ContentBlock{TextBlock("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"prompt":[{"type":"text","text":"hi"}]`) {
		t.Fatalf("prompt = %s", raw)
	}
}

func TestRPCIDKey(t *testing.T) {
	cases := map[string]string{
		`1`:     "1",
		`42`:    "42",
		`"abc"`: "abc",
		` "x" `: "x",
	}
	for in, want := range cases {
		if got := rpcIDKey(json.RawMessage(in)); got != want {
			t.Fatalf("rpcIDKey(%s) = %q, want %q", in, got, want)
		}
	}
}
