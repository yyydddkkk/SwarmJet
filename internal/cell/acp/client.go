// Package acp drives a long-running Agent Client Protocol (ACP) process.
//
// A Cell runs one CLI for the lifetime of an Agent and speaks ACP with it over
// newline-delimited JSON-RPC on stdio, either as a local process or through
// `docker exec -i` into the Cell container. The protocol was validated in
// docs/claude/spikes/opencode-driver.md; this package keeps the subset SwarmJet
// needs: initialize, session/new, session/prompt with streamed session/update
// notifications, session/cancel and session/load.
//
// Session state lives in the CLI's data directory (for OpenCode a SQLite
// database). The platform persists the session ID and passes it back through
// session/load after a container or process restart.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// UpdateSink receives one session/update notification. Sinks run on a
// dedicated dispatcher goroutine, never on the connection read loop, so a slow
// sink cannot stall JSON-RPC responses. Sinks should still return promptly and
// must tolerate being called from a goroutine other than the caller's.
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
	updates    *updateQueue
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
	c := &Client{conn: newRPCConn(rw), updates: newUpdateQueue()}
	for _, opt := range opts {
		opt(c)
	}
	c.conn.onRequest = c.handleRequest
	c.conn.onNotify = c.handleNotification
	return c
}

// Close shuts down the transport, stops the update dispatcher and fails
// in-flight calls. It is safe to call more than once.
func (c *Client) Close() error {
	err := c.conn.Close()
	c.updates.stop()
	return err
}

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
// session/update notifications before the call returns; replay is delivered to
// sink before LoadSession returns.
func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string, mcp []MCPServer, sink UpdateSink) (LoadSessionResult, error) {
	if sessionID == "" {
		return LoadSessionResult{}, errors.New("acp: session/load requires a session id")
	}
	params := LoadSessionParams{SessionID: sessionID, Cwd: cwd, McpServers: normalizeMCP(mcp)}
	c.setSink(sink)
	defer c.setSink(nil)
	res, err := callTyped[LoadSessionResult](c, ctx, MethodSessionLoad, params)
	c.updates.flush()
	return res, err
}

// Prompt sends one turn and streams session/update notifications to sink. It
// returns once the agent reports a stop reason and every update received for
// the turn has been handed to sink. The context cancels the local wait; use
// Cancel to stop the agent's turn.
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
	res, err := callTyped[PromptResult](c, ctx, MethodSessionPrompt, params)
	c.updates.flush()
	return res, err
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
	c.updates.setSink(sink)
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	if method != MethodSessionUpdate {
		return
	}
	var n SessionNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return
	}
	// Queue the update for the dispatcher instead of invoking the sink on the
	// read loop. Some agents emit very large notifications (for example
	// available_commands_update), so backpressure here would block responses.
	c.updates.push(n)
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
