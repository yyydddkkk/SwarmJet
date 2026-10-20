package acp

import (
	"bytes"
	"encoding/json"
)

// This file mirrors the subset of the Agent Client Protocol (ACP) that the
// spike exercises. Field names follow the ACP JSON schema.

// ProtocolVersion is the ACP protocol version this client speaks.
const ProtocolVersion = 1

// ClientCapabilities advertises what the client can do for the agent. A Cell
// can leave everything disabled: the CLI runs inside its own container and
// reaches the filesystem and shell directly, so the platform only needs the
// permission callbacks.
type ClientCapabilities struct {
	FS       *FileSystemCapabilities `json:"fs,omitempty"`
	Terminal bool                    `json:"terminal,omitempty"`
}

// FileSystemCapabilities toggles the fs/* client callbacks. The spike leaves
// them off to keep the client minimal.
type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// InitializeParams is the payload of the initialize request.
type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
}

// Implementation identifies an agent or client implementation.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// MCPCapabilities describes the MCP transports the agent can attach.
type MCPCapabilities struct {
	HTTP bool `json:"http,omitempty"`
	SSE  bool `json:"sse,omitempty"`
}

// PromptCapabilities describes what prompt content blocks the agent accepts.
type PromptCapabilities struct {
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
	Image           bool `json:"image,omitempty"`
}

// AgentCapabilities is the capability block returned by initialize.
type AgentCapabilities struct {
	LoadSession         bool                       `json:"loadSession,omitempty"`
	MCPCapabilities     *MCPCapabilities           `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  *PromptCapabilities        `json:"promptCapabilities,omitempty"`
	SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities,omitempty"`
}

// AuthMethod is an authentication option offered by the agent.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// InitializeResult is the response to initialize.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

// EnvVariable is one environment variable for a stdio MCP server.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// HTTPHeader is one header for an HTTP/SSE MCP server.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPServer attaches an MCP server to a session. A stdio server sets Command
// and Args; an HTTP or SSE server sets Type and URL. Agents namespace the tools
// they expose as "<server-name>_<tool-name>".
type MCPServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command,omitempty"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
	Type    string        `json:"type,omitempty"`
	URL     string        `json:"url,omitempty"`
	Headers []HTTPHeader  `json:"headers,omitempty"`
}

// MarshalJSON encodes exactly the fields the chosen transport needs. Some ACP
// agents validate stdio servers strictly and reject them unless args and env
// are present as arrays, so a zero MCPServer would otherwise fail session/new
// with "Invalid params"; omitempty cannot express an empty array.
func (m MCPServer) MarshalJSON() ([]byte, error) {
	out := map[string]any{"name": m.Name}
	switch {
	case m.Command != "":
		args := m.Args
		if args == nil {
			args = []string{}
		}
		env := m.Env
		if env == nil {
			env = []EnvVariable{}
		}
		out["command"] = m.Command
		out["args"] = args
		out["env"] = env
	case m.Type != "":
		headers := m.Headers
		if headers == nil {
			headers = []HTTPHeader{}
		}
		out["type"] = m.Type
		out["url"] = m.URL
		out["headers"] = headers
	default:
		if m.URL != "" {
			out["url"] = m.URL
		}
	}
	return json.Marshal(out)
}

// NewSessionParams is the payload of session/new.
type NewSessionParams struct {
	Cwd        string      `json:"cwd"`
	McpServers []MCPServer `json:"mcpServers"`
}

// ConfigOption is a selectable session setting such as the model.
type ConfigOption struct {
	ID           string              `json:"id"`
	Name         string              `json:"name,omitempty"`
	Category     string              `json:"category,omitempty"`
	Type         string              `json:"type,omitempty"`
	CurrentValue string              `json:"currentValue,omitempty"`
	Options      []ConfigOptionValue `json:"options,omitempty"`
}

// ConfigOptionValue is one choice of a ConfigOption.
type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// NewSessionResult is the response to session/new.
type NewSessionResult struct {
	SessionID     string          `json:"sessionId"`
	ConfigOptions []ConfigOption  `json:"configOptions,omitempty"`
	Modes         json.RawMessage `json:"modes,omitempty"`
}

// LoadSessionParams is the payload of session/load. Loading replays the
// conversation as session/update notifications before the request returns.
type LoadSessionParams struct {
	SessionID  string      `json:"sessionId"`
	Cwd        string      `json:"cwd"`
	McpServers []MCPServer `json:"mcpServers"`
}

// LoadSessionResult is the response to session/load.
type LoadSessionResult struct {
	ConfigOptions []ConfigOption  `json:"configOptions,omitempty"`
	Modes         json.RawMessage `json:"modes,omitempty"`
	Meta          json.RawMessage `json:"_meta,omitempty"`
}

// ContentBlock is one element of a prompt.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextBlock builds a text content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: "text", Text: text} }

// PromptParams is the payload of session/prompt.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// StopReason is why a prompt turn ended.
type StopReason string

// ACP stop reasons.
const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopMaxTurnRequests StopReason = "max_turn_requests"
	StopRefusal         StopReason = "refusal"
	StopCancelled       StopReason = "cancelled"
)

// Usage is the token accounting attached to a prompt result.
type Usage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	TotalTokens       int `json:"totalTokens"`
	ThoughtTokens     int `json:"thoughtTokens,omitempty"`
	CachedReadTokens  int `json:"cachedReadTokens,omitempty"`
	CachedWriteTokens int `json:"cachedWriteTokens,omitempty"`
}

// PromptResult is the response to session/prompt.
type PromptResult struct {
	StopReason StopReason      `json:"stopReason"`
	Usage      *Usage          `json:"usage,omitempty"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

// CancelParams is the payload of the session/cancel notification.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// SessionNotification is a session/update notification from the agent.
type SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// Session update kind names.
const (
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
	UpdatePlan              = "plan"
	UpdateAvailableCommands = "available_commands_update"
	UpdateCurrentMode       = "current_mode_update"
	UpdateUsage             = "usage_update"
)

// ToolCallStatus values.
const (
	ToolStatusPending    = "pending"
	ToolStatusInProgress = "in_progress"
	ToolStatusCompleted  = "completed"
	ToolStatusFailed     = "failed"
)

// SessionUpdate is the decoded body of a session/update notification. Only the
// fields the Runtime needs are typed; the complete notification stays in Raw so
// updates without a typed representation (for example a plan) can still be
// surfaced.
type SessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	// Content is kept raw because ACP uses two shapes: message chunks carry a
	// single ContentBlock object, while tool_call, tool_call_update and the
	// toolCall of a permission request carry an ARRAY of tool content (for
	// example a diff). Use Text() for the message case.
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	Status     string          `json:"status,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
	RawOutput  json.RawMessage `json:"rawOutput,omitempty"`
	Locations  []ToolLocation  `json:"locations,omitempty"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

// ToolLocation is a file path touched by a tool call.
type ToolLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// Decode parses the notification body into a SessionUpdate.
func (n SessionNotification) Decode() (SessionUpdate, error) {
	var u SessionUpdate
	if len(n.Update) == 0 {
		return u, nil
	}
	if err := json.Unmarshal(n.Update, &u); err != nil {
		return u, err
	}
	u.Raw = append(json.RawMessage(nil), n.Update...)
	return u, nil
}

// Text returns the text of a content chunk, or "" when the update carries none.
func (u SessionUpdate) Text() string {
	raw := bytes.TrimSpace(u.Content)
	// Only the single-block form carries message text; tool content arrays
	// are not text chunks and yield "".
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var block ContentBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		return ""
	}
	return block.Text
}

// Permission option kinds.
const (
	PermissionAllowOnce    = "allow_once"
	PermissionAllowAlways  = "allow_always"
	PermissionRejectOnce   = "reject_once"
	PermissionRejectAlways = "reject_always"
)

// PermissionOption is one choice offered for a permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
}

// PermissionRequest is the payload of a session/request_permission request the
// agent sends to the client.
type PermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  SessionUpdate      `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOutcome is the client's decision.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"` // "selected" or "cancelled"
	OptionID string `json:"optionId,omitempty"`
}

// Select picks an option by kind, falling back to the first option offered.
func (r PermissionRequest) Select(kinds ...string) (PermissionOption, bool) {
	for _, kind := range kinds {
		for _, o := range r.Options {
			if o.Kind == kind {
				return o, true
			}
		}
	}
	if len(r.Options) > 0 {
		return r.Options[0], true
	}
	return PermissionOption{}, false
}

// Selected builds a "selected" outcome for an option.
func Selected(o PermissionOption) PermissionOutcome {
	return PermissionOutcome{Outcome: "selected", OptionID: o.OptionID}
}

// Cancelled builds a "cancelled" outcome, used when the client cannot decide.
func Cancelled() PermissionOutcome { return PermissionOutcome{Outcome: "cancelled"} }

// PermissionResponse wraps the outcome for the JSON-RPC result.
type PermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// Error codes defined by JSON-RPC 2.0 and used by ACP.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ACP method names.
const (
	MethodInitialize        = "initialize"
	MethodSessionNew        = "session/new"
	MethodSessionLoad       = "session/load"
	MethodSessionPrompt     = "session/prompt"
	MethodSessionCancel     = "session/cancel"
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
)
