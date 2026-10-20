// Package mcp implements SwarmJet's Model Context Protocol (MCP) server.
//
// Local agents (Claude Code, Codex, ...) connect to this server to select a
// Hive, talk to its Queen, and spawn or inspect Foragers. The tool set follows
// docs/claude/adr-hive-queen-forager.md section 12; the domain types come from
// the shared contract in docs/claude/round1-prompts.md.
//
// This round the tools are wired to an in-memory fake backend (MemoryBackend).
// The Backend interface is a subset of the Hive store methods described in the
// contract, with identical signatures, so a later round can pass the real
// *store.Store without changing the tool layer.
package mcp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

// Backend is the subset of the Hive store that the MCP tools need. Every method
// signature matches docs/claude/round1-prompts.md verbatim so *store.Store can
// satisfy it unchanged once the data layer lands.
type Backend interface {
	CreateHive(ctx context.Context, h domain.Hive) (domain.Hive, error)
	Hive(ctx context.Context, id string) (domain.Hive, error)
	Hives(ctx context.Context) ([]domain.Hive, error)
	UpdateHive(ctx context.Context, h domain.Hive) (domain.Hive, error)

	CreateForager(ctx context.Context, a domain.Agent) (domain.Agent, error)
	Agent(ctx context.Context, id string) (domain.Agent, error)
	Queen(ctx context.Context, hiveID string) (domain.Agent, error)
	Foragers(ctx context.Context, hiveID string, states ...domain.AgentState) ([]domain.Agent, error)
	TransitionAgent(ctx context.Context, id string, from, to domain.AgentState) error

	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
	// MaxReceivedHop is the largest hop among messages delivered to `to` in
	// chainID, or 0 when there are none. The server derives an Agent's
	// outgoing hop from it, so an Agent cannot reset the hop limit.
	MaxReceivedHop(ctx context.Context, hiveID string, to domain.Address, chainID string) (int, error)
	Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error)
	ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (bool, error)
	WaitInbox(ctx context.Context, hiveID string, to domain.Address, afterID string) error

	TokenByPlaintext(ctx context.Context, plaintext string) (domain.Token, error)

	// UpdateHandoff replaces an Agent's handoff summary. It is the only store
	// method beyond the first-round Backend that the MCP tools need; the new
	// round-2 methods are used by the Router and the Forager supervisor.
	UpdateHandoff(ctx context.Context, id string, h domain.Handoff) error

	// Round 4: a Hive's versioned methodology. The four methodology tools read
	// and write through these methods.
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
	MethodologyVersion(ctx context.Context, hiveID string, version int) (domain.Methodology, []domain.MethodologyFile, error)
	MethodologyVersions(ctx context.Context, hiveID string) ([]domain.Methodology, error)
	MethodologyChanges(ctx context.Context, hiveID string) ([]methodology.Change, error)
	PublishMethodology(ctx context.Context, m domain.Methodology, files []domain.MethodologyFile) (domain.Methodology, error)
	ActivateMethodology(ctx context.Context, hiveID string, version int, actor domain.Address, note string) (domain.Methodology, error)
}

// Limits applied to update_handoff. They keep a handoff summary summary-sized
// even though the store itself only bounds message bodies.
const (
	maxHandoffArtifacts = 50
	maxArtifactKind     = 100
	maxArtifactRef      = 1000
)

// Defaults applied to zero-valued Options fields.
const (
	DefaultMaxName        = "swarmjet"
	DefaultVersion        = "0.1.0"
	DefaultMaxWait        = 60 * time.Second
	DefaultMaxResults     = 50
	DefaultMaxTextChars   = domain.MaxMessageBody
	DefaultSessionTimeout = 10 * time.Minute
)

// Options configures the MCP handler. The zero value is usable.
type Options struct {
	// Name and Version identify this server implementation to clients.
	Name    string
	Version string
	// Logger receives SDK logs. Nil means discard.
	Logger *slog.Logger
	// MaxWait caps the `wait` long poll. Defaults to 60s (ADR section 12).
	MaxWait time.Duration
	// MaxResults caps every list-style response. Defaults to 50.
	MaxResults int
	// MaxTextChars caps a single text field (message body, task, handoff
	// fields) before it is truncated. Defaults to domain.MaxMessageBody.
	MaxTextChars int
	// SessionTimeout closes idle Streamable HTTP sessions. Defaults to 10m.
	SessionTimeout time.Duration
	// Now is injectable for tests. Nil means time.Now.
	Now func() time.Time
	// Merge powers attest, pr_status and merge_pr. When nil the tools are still
	// listed but return a clear "not configured" error.
	Merge MergeService
	// Credentials writes model-provider credentials into the omp auth broker.
	// nil makes set_model_credential report that it is not configured.
	Credentials CredentialWriter
}

// withDefaults returns a copy of o with zero fields filled in.
func (o Options) withDefaults() Options {
	if o.Name == "" {
		o.Name = DefaultMaxName
	}
	if o.Version == "" {
		o.Version = DefaultVersion
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.MaxWait <= 0 {
		o.MaxWait = DefaultMaxWait
	}
	if o.MaxResults <= 0 {
		o.MaxResults = DefaultMaxResults
	}
	if o.MaxTextChars <= 0 {
		o.MaxTextChars = DefaultMaxTextChars
	}
	if o.SessionTimeout <= 0 {
		o.SessionTimeout = DefaultSessionTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// truncateText clips s to max characters (runes) and reports whether it cut
// anything. The marker tells the caller that content is missing, and it is
// itself included in the returned length budget's suffixes, not counted
// against max.
func truncateText(s string, max int) (string, bool) {
	if max <= 0 {
		return s, false
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s, false
	}
	omitted := len(runes) - max
	return string(runes[:max]) + fmt.Sprintf("\n…[truncated: %d characters omitted]", omitted), true
}

// clampLimit normalizes a caller-supplied list limit against the configured
// maximum. It returns the effective page size and whether the caller's request
// was reduced.
func clampLimit(limit, max int) (int, bool) {
	if limit <= 0 {
		return max, false
	}
	if limit > max {
		return max, true
	}
	return limit, false
}
