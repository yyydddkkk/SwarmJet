// Package cell runs Agents in long-lived containers. This file is the round-2
// contract shared by the Cell runtime and the Forager supervisor; change it
// only by adding, never by editing existing declarations.
package cell

import (
	"context"
	"errors"
	"time"
)

// Spec is everything needed to start or resume one Agent's Cell.
type Spec struct {
	AgentID    string
	HiveID     string
	Repository string            // GitHub owner/name
	Branch     string            // the Agent's own branch
	BaseBranch string            // branch to start from; empty means the repository default
	CLI        string            // "opencode" this round
	Model      string            // provider/model, always pinned explicitly
	SessionRef string            // empty starts a new CLI session; otherwise it is loaded
	Env        map[string]string // CLI secrets such as OPENCODE_AUTH_CONTENT; never logged
	MCPURL     string            // SwarmJet MCP endpoint as reachable from inside the Cell
	MCPToken   string            // Agent-scoped token for MCPURL

	// Round 4. Revision, when set, is the commit the workspace checks out
	// detached; Branch then only names the local branch. ReadOnly means the
	// role may not publish: the Publisher refuses the Spec.
	Revision string
	ReadOnly bool
	// ConfigFiles are written read-only under the CLI's configuration
	// directory, outside the workspace, so they are never committed. They
	// carry the methodology and the platform's CLI settings overlay.
	ConfigFiles map[string]string
	// LLMBaseURL and LLMToken point the CLI at the platform's model proxy.
	// The Cell never receives a provider credential.
	LLMBaseURL string
	LLMToken   string
}

// UpdateKind classifies streamed output of a turn.
type UpdateKind string

const (
	UpdateText     UpdateKind = "text"
	UpdateThought  UpdateKind = "thought"
	UpdateToolCall UpdateKind = "tool_call"
	UpdateToolDone UpdateKind = "tool_done"
	UpdatePlan     UpdateKind = "plan"
)

// Update is one streamed piece of a turn.
type Update struct {
	Kind  UpdateKind
	Text  string // truncated by the runtime to at most MaxUpdateText characters
	Title string // tool title for tool updates
	At    time.Time
}

// MaxUpdateText caps Update.Text.
const MaxUpdateText = 4000

// TurnResult is how a turn ended.
type TurnResult struct {
	StopReason   string // end_turn, cancelled, max_tokens, refusal, ...
	InputTokens  int64
	OutputTokens int64
}

// Session is a connected CLI session inside a running Cell. One turn at a time.
type Session interface {
	// Ref is the CLI session ID; persist it and pass it back as Spec.SessionRef.
	Ref() string
	// Prompt runs one turn. sink must not block; it is called from the reader.
	Prompt(ctx context.Context, text string, sink func(Update)) (TurnResult, error)
	// Cancel interrupts the current turn; the session stays usable.
	Cancel(ctx context.Context) error
	// Close stops the CLI process; the container keeps running.
	Close() error
}

// Runtime manages Cells. Every method is idempotent per AgentID.
type Runtime interface {
	// Start creates or restarts the container and its volumes, prepares the
	// workspace on Spec.Branch, starts the CLI and creates or loads the session.
	Start(ctx context.Context, spec Spec) (Session, error)
	// Stop closes the CLI and stops the container; volumes stay (parked).
	Stop(ctx context.Context, agentID string) error
	// Remove deletes the container and keeps the volumes (hibernated).
	Remove(ctx context.Context, agentID string) error
	// Destroy deletes the container and the volumes (terminal cleanup).
	Destroy(ctx context.Context, agentID string) error
}

// Publisher moves work from a Cell workspace to GitHub. It runs on the
// platform side with platform credentials; a Cell never holds Git credentials.
type Publisher interface {
	// Publish commits any uncommitted changes as a WIP commit, then, if
	// Spec.Branch is ahead of the base, pushes it and creates or updates the PR
	// and returns its number. It returns 0 and no error when there is nothing
	// to publish.
	Publish(ctx context.Context, spec Spec, title, body string) (pr int64, err error)
}

// ErrNoCell is returned for an AgentID that has no Cell.
var ErrNoCell = errors.New("cell: no such cell")
