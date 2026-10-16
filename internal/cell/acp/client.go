// Package acp is an EXPERIMENTAL (实验性) client for the Agent Client Protocol
// (ACP).
//
// It exists to validate how SwarmJet should drive a long-running OpenCode
// process inside a Cell container (docs/claude/adr-hive-queen-forager.md,
// section 8). Nothing here is wired into the runtime yet and the API may change
// without notice; see docs/claude/spikes/opencode-driver.md for the findings.
//
// The client speaks newline-delimited JSON-RPC over a stdio transport, which is
// either a local process or `docker exec -i` into a Cell container. It covers
// the calls the spike needs: initialize, session/new, session/prompt with
// streamed session/update notifications, session/cancel and session/load.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// UpdateSink receives one session/update notification. It runs on the
// connection's read loop, so it must return quickly.
type UpdateSink func(SessionNotification) error

// PermissionHandler decides a session/request_permission request. Returning an
// error makes the client answer "cancelled" instead of selecting an option.
type PermissionHandler func(context.Context, PermissionRequest) (PermissionOutcome, error)

// Client drives one ACP agent process. Only one prompt turn may be active at a
// time, which matches a Cell driving a single Forager.
type Client struct {
	conn       *rpcConn
	caps       ClientCapabilities
	permission PermissionHandler

	mu   sync.Mutex
	sink UpdateSink
}

// Option configures a Client.
type Option func(*Client)

// WithClientCapabilities sets the capabilities advertised to the agent. The
// zero value is the minimal ACP client: no filesystem or terminal callbacks.
func WithClientCapabilities(caps ClientCapabilities) Option {
	return func(c *Client) { c.caps = caps }
}

// WithPermissionHandler installs the policy that answers permission requests.
// Without one, every request is declined.
func WithPermissionHandler(h PermissionHandler) Option {
	return func(c *Client) { c.permission = h }
}

// NewClient wraps an already-connected transport. rw is closed by Close.
func NewClient(rw io.ReadWriteCloser, opts ...Option) *Client {
	c := &Client{conn: newRPCConn(rw)}
	for _, opt := range opts {
		opt(c)
	}
	c.conn.onRequest = c.handleRequest
	c.conn.onNotify = c.handleNotification
	return c
}

// Close shuts down the transport and fails in-flight calls.
func (c *Client) Close() error { return c.conn.Close() }

// Initialize performs the ACP handshake and reports the agent's capabilities.
func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	params := InitializeParams{ProtocolVersion: ProtocolVersion, ClientCapabilities: c.caps}
	res, err := callTyped[InitializeResult](c, ctx, MethodInitialize, params)
	if err != nil {
		return InitializeResult{}, err
	}
	return res, nil
}

// NewSession creates a session rooted at cwd and attaches the given MCP
// servers. Pass nil when no MCP server is needed.
func (c *Client) NewSession(ctx context.Context, cwd string, mcp []MCPServer) (NewSessionResult, error) {
	params := NewSessionParams{Cwd: cwd, McpServers: normalizeMCP(mcp)}
	return callTyped[NewSessionResult](c, ctx, MethodSessionNew, params)
}

// LoadSession resumes a previous session. The agent replays the conversation as
// session/update notifications before the call returns, so sink must be set
// before it is invoked.
func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string, mcp []MCPServer, sink UpdateSink) (LoadSessionResult, error) {
	if sessionID == "" {
		return LoadSessionResult{}, errors.New("acp: session/load requires a session id")
	}
	params := LoadSessionParams{SessionID: sessionID, Cwd: cwd, McpServers: normalizeMCP(mcp)}
	c.setSink(sink)
	defer c.setSink(nil)
	return callTyped[LoadSessionResult](c, ctx, MethodSessionLoad, params)
}

// Prompt sends one turn and streams session/update notifications to sink. It
// returns once the agent reports a stop reason. The context cancels the local
// wait; use Cancel to stop the agent's turn.
func (c *Client) Prompt(ctx context.Context, sessionID string, blocks []ContentBlock, sink UpdateSink) (PromptResult, error) {
	if sessionID == "" {
		return PromptResult{}, errors.New("acp: session/prompt requires a session id")
	}
	if len(blocks) == 0 {
		return PromptResult{}, errors.New("acp: session/prompt requires at least one content block")
	}
	params := PromptParams{SessionID: sessionID, Prompt: blocks}
	c.setSink(sink)
	defer c.setSink(nil)
	return callTyped[PromptResult](c, ctx, MethodSessionPrompt, params)
}

// Cancel asks the agent to stop the current turn. ACP models this as a
// notification, so there is no acknowledgement; the prompt request resolves
// with stop reason "cancelled".
func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return c.conn.notify(MethodSessionCancel, CancelParams{SessionID: sessionID})
}

func callTyped[T any](c *Client, ctx context.Context, method string, params any) (T, error) {
	var zero T
	raw, err := c.conn.call(ctx, method, params)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("acp: decode %s result: %w", method, err)
	}
	return out, nil
}

func normalizeMCP(mcp []MCPServer) []MCPServer {
	if mcp == nil {
		return []MCPServer{}
	}
	return mcp
}

func (c *Client) setSink(sink UpdateSink) {
	c.mu.Lock()
	c.sink = sink
	c.mu.Unlock()
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	if method != MethodSessionUpdate {
		return
	}
	var n SessionNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return
	}
	c.mu.Lock()
	sink := c.sink
	c.mu.Unlock()
	if sink != nil {
		_ = sink(n)
	}
}

func (c *Client) handleRequest(method string, params json.RawMessage) (any, *rpcError) {
	if method != MethodRequestPermission {
		return nil, &rpcError{Code: CodeMethodNotFound, Message: "method not found: " + method}
	}
	var req PermissionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &rpcError{Code: CodeInvalidParams, Message: "invalid permission request: " + err.Error()}
	}
	outcome := Cancelled()
	if c.permission != nil {
		decided, err := c.permission(context.Background(), req)
		if err == nil {
			outcome = decided
		}
	}
	return PermissionResponse{Outcome: outcome}, nil
}
