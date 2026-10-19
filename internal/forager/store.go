// Package forager supervises Foragers: one Agent per task, one branch, one PR.
//
// A Forager is woken by a message in its Inbox. The Supervisor starts or
// resumes its Cell, turns the unhandled messages into one prompt, publishes the
// result after the turn, reports back, and applies the Hive's lifecycle limits
// (idle park, hibernation, turn timeout, maximum lifetime and cancellation).
//
// The supervisor only talks to the storage layer through Store and to the
// runtime through cell.Runtime and cell.Publisher, so tests use fakes.
package forager

import (
	"context"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Store is the subset of *store.Store the supervisor needs. Every method
// signature is identical to the store method of the same name, including the
// round-2 additions (SetAgentSession, SetAgentPR, Unhandled, CreateAgentToken
// and RevokeAgentTokens).
type Store interface { // Agent reads one Agent, including SessionRef and Handoff.
	Agent(ctx context.Context, id string) (domain.Agent, error)
	// Hive reads one Hive, with the documented config defaults applied.
	Hive(ctx context.Context, id string) (domain.Hive, error)
	// Hives lists every Hive, used by the periodic recovery scan.
	Hives(ctx context.Context) ([]domain.Hive, error)
	// Foragers lists a Hive's Foragers, optionally filtered by state.
	Foragers(ctx context.Context, hiveID string, states ...domain.AgentState) ([]domain.Agent, error)

	// TransitionAgent is a compare-and-swap on the Agent state.
	TransitionAgent(ctx context.Context, id string, from, to domain.AgentState) error
	// SetAgentSession persists the CLI session ID used to resume the Cell.
	SetAgentSession(ctx context.Context, id, sessionRef string) error
	// SetAgentPR persists the PR created or updated for the Agent.
	SetAgentPR(ctx context.Context, id string, pr int64) error

	// Unhandled returns an Inbox's pending or delivered messages in insertion
	// order. The supervisor consumes a Forager's Inbox through it.
	Unhandled(ctx context.Context, hiveID string, to domain.Address, limit int) ([]domain.Message, error)
	// MarkMessageHandled moves a consumed message out of the Unhandled set.
	MarkMessageHandled(ctx context.Context, id string) error
	// SendMessage stores a report or other message and schedules its delivery.
	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)

	// CreateAgentToken mints an Agent-scoped token and returns its plaintext
	// once. RevokeAgentTokens invalidates every token minted for the Agent.
	CreateAgentToken(ctx context.Context, agentID string) (plaintext string, t domain.Token, err error)
	RevokeAgentTokens(ctx context.Context, agentID string) error
}

// MethodologyStore is implemented by *store.Store when the methodology task
// lands. The supervisor type-asserts the Store so Cell wiring compiles and runs
// (with Builtin) before that method exists.
type MethodologyStore interface {
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
}
