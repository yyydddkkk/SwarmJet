package store

import (
	"context"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// hiveStoreContract mirrors the storage-method blocks of the shared contracts
// in docs/claude/round1-prompts.md and docs/claude/round2-prompts.md. The
// assertion below fails to compile if any signature drifts, which is what keeps
// the Forager supervisor and MCP wiring able to consume it.
type hiveStoreContract interface {
	CreateHive(ctx context.Context, h domain.Hive) (domain.Hive, error)
	Hive(ctx context.Context, id string) (domain.Hive, error)
	HiveByRepository(ctx context.Context, repository string) (domain.Hive, error)
	Hives(ctx context.Context) ([]domain.Hive, error)
	UpdateHive(ctx context.Context, h domain.Hive) (domain.Hive, error)

	CreateForager(ctx context.Context, a domain.Agent) (domain.Agent, error)
	Agent(ctx context.Context, id string) (domain.Agent, error)
	Queen(ctx context.Context, hiveID string) (domain.Agent, error)
	Foragers(ctx context.Context, hiveID string, states ...domain.AgentState) ([]domain.Agent, error)
	ForagerByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, error)
	ForagerByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, error)
	ForagerByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, error)
	TransitionAgent(ctx context.Context, id string, from, to domain.AgentState) error
	UpdateHandoff(ctx context.Context, id string, h domain.Handoff) error
	SetAgentSession(ctx context.Context, id, sessionRef string) error
	SetAgentPR(ctx context.Context, id string, pr int64) error

	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
	Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error)
	Unhandled(ctx context.Context, hiveID string, to domain.Address, limit int) ([]domain.Message, error)
	ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (bool, error)
	MarkMessageHandled(ctx context.Context, id string) error
	WaitInbox(ctx context.Context, hiveID string, to domain.Address, afterID string) error

	CreateToken(ctx context.Context, name string, scope domain.TokenScope) (plaintext string, t domain.Token, err error)
	CreateAgentToken(ctx context.Context, agentID string) (plaintext string, t domain.Token, err error)
	RevokeAgentTokens(ctx context.Context, agentID string) error
	TokenByPlaintext(ctx context.Context, plaintext string) (domain.Token, error)
}

var _ hiveStoreContract = (*Store)(nil)
