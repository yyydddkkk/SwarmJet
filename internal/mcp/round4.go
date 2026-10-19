package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// MethodologyReader is the store method the round-4 methodology data layer
// implements. The MCP layer keeps it optional so a store without the
// methodology migration still serves the default (permissive) behaviour.
type MethodologyReader interface {
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
}

// LimitedForagerCreator is the store method that applies methodology limits on
// top of HiveConfig. Without it the Router/MCP fall back to CreateForager.
type LimitedForagerCreator interface {
	CreateForagerWithLimits(ctx context.Context, a domain.Agent, limits domain.Limits) (domain.Agent, error)
}

// activeMethodology reads the Hive's current methodology. found is false when
// no methodology has been published, in which case the platform uses its
// permissive defaults instead of inventing workflow.
func (ts *toolset) activeMethodology(ctx context.Context, hiveID string) (domain.Methodology, bool, error) {
	reader, ok := ts.backend.(MethodologyReader)
	if !ok {
		return domain.Methodology{}, false, nil
	}
	m, _, err := reader.ActiveMethodology(ctx, hiveID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Methodology{}, false, nil
	}
	if err != nil {
		return domain.Methodology{}, false, err
	}
	return m, true, nil
}

// resolveMethodologyRole resolves a role the way internal/methodology.Resolve
// will: an empty name means the manifest's defaultRole.
func resolveMethodologyRole(m domain.MethodologyManifest, roleName string) (domain.Role, error) {
	name := strings.TrimSpace(roleName)
	if name == "" {
		name = strings.TrimSpace(m.DefaultRole)
	}
	if name == "" {
		return domain.Role{}, fmt.Errorf("the active methodology has no default role")
	}
	role, ok := m.Roles[name]
	if !ok {
		return domain.Role{}, fmt.Errorf("role %q is not defined by the active methodology", name)
	}
	return role, nil
}

// callerRole returns the effective methodology role of an agent token. found is
// false for user tokens and when no methodology exists.
func (ts *toolset) callerRole(ctx context.Context, hiveID string) (domain.Role, bool, error) {
	if !ts.actor.isAgent() {
		return domain.Role{}, false, nil
	}
	m, found, err := ts.activeMethodology(ctx, hiveID)
	if err != nil || !found {
		return domain.Role{}, false, err
	}
	role, err := resolveMethodologyRole(m.Manifest, ts.actor.agent.RoleName)
	if err != nil {
		return domain.Role{}, false, fmt.Errorf("permission denied: %w", err)
	}
	return role, true, nil
}

// mayMessage reports whether an agent token's role may message any Agent in
// the Hive. A Hive without a methodology keeps the platform default: allowed.
func (ts *toolset) mayMessage(ctx context.Context, hiveID string) (bool, error) {
	if !ts.actor.isAgent() {
		return true, nil
	}
	role, found, err := ts.callerRole(ctx, hiveID)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	return role.Permissions.Message, nil
}

// maySpawn enforces the caller role's spawn list. A Hive without a methodology
// keeps the platform default: allowed.
func (ts *toolset) maySpawn(ctx context.Context, hiveID, roleName string) error {
	if !ts.actor.isAgent() {
		return nil
	}
	role, found, err := ts.callerRole(ctx, hiveID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if !slices.Contains(role.Permissions.Spawn, roleName) {
		return fmt.Errorf("permission denied: role %q may not spawn role %q (spawn list: %s)",
			ts.actor.agent.RoleName, roleName, strings.Join(role.Permissions.Spawn, ", "))
	}
	return nil
}

// resolveSendTarget applies the message permission. A role without the message
// permission may only reach its dispatcher (the address the existing default
// target logic picks). Any other explicit target is ErrPeerMessage.
func (ts *toolset) resolveSendTarget(ctx context.Context, hiveID, raw string) (domain.Address, error) {
	allowed, err := ts.mayMessage(ctx, hiveID)
	if err != nil {
		return "", err
	}
	if allowed {
		return ts.sendTarget(ctx, hiveID, raw)
	}
	fallback, err := ts.sendTarget(ctx, hiveID, "")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	requested, err := resolveAddress(raw)
	if err != nil {
		return "", err
	}
	if requested != fallback {
		return "", fmt.Errorf("%w: this agent's role may only message %s", domain.ErrPeerMessage, fallback)
	}
	return requested, nil
}

// outboundTrust is the provenance of a message sent through this MCP session:
// agent-scoped tokens speak as their Agent, every other token speaks for the
// user.
func (ts *toolset) outboundTrust() domain.MessageTrust {
	if ts.actor.isAgent() {
		return domain.TrustAgent
	}
	return domain.TrustUser
}

// spawnRequest is the shared input of spawn and spawn_forager.
type spawnRequest struct {
	HiveID      string
	RoleName    string
	Task        string
	Revision    string
	Preset      string
	CLI         string
	Branch      string
	ChainID     string
	RequestID   string
	IssueNumber int64
	PRNumber    int64
	ParentID    string
	Limits      domain.Limits
}

// createSpawnedForager is the one creation path behind spawn and
// spawn_forager. It records the methodology role, revision and parent, inherited
// chain, and delivers the task as a provenance-tagged task message.
func (ts *toolset) createSpawnedForager(ctx context.Context, req spawnRequest) (domain.Agent, domain.Message, error) {
	chainID := req.ChainID
	if chainID == "" {
		if req.RequestID != "" {
			chainID = deriveID("chain", ts.token.ID, req.RequestID)
		} else {
			chainID = newChainID()
		}
	}
	var foragerID, messageID string
	if req.RequestID != "" {
		foragerID = deriveID("forager", ts.token.ID, req.RequestID)
		messageID = deriveID("msg", ts.token.ID, req.RequestID+"\x00task")
	}

	created, found, err := ts.existingForager(ctx, foragerID, req.HiveID)
	if err != nil {
		return domain.Agent{}, domain.Message{}, err
	}
	if !found {
		created, err = ts.createForager(ctx, domain.Agent{
			ID:          foragerID,
			HiveID:      req.HiveID,
			Role:        domain.RoleForager,
			State:       domain.AgentCreating,
			Preset:      req.Preset,
			CLI:         req.CLI,
			Task:        req.Task,
			Branch:      req.Branch,
			IssueNumber: req.IssueNumber,
			PRNumber:    req.PRNumber,
			ChainID:     chainID,
			RoleName:    req.RoleName,
			Revision:    req.Revision,
			ParentID:    req.ParentID,
		}, req.Limits)
		if err != nil {
			if foragerID == "" || !errors.Is(err, domain.ErrConflict) {
				return domain.Agent{}, domain.Message{}, err
			}
			existing, ok, getErr := ts.existingForager(ctx, foragerID, req.HiveID)
			if getErr != nil {
				return domain.Agent{}, domain.Message{}, getErr
			}
			if !ok {
				return domain.Agent{}, domain.Message{}, err
			}
			created = existing
		}
	}
	messageChain := created.ChainID
	if messageChain == "" {
		messageChain = chainID
	}
	msg, err := ts.backend.SendMessage(ctx, domain.Message{
		ID:      messageID,
		HiveID:  req.HiveID,
		From:    ts.actor.address(),
		To:      domain.ForagerAddress(created.ID),
		Kind:    domain.MessageTask,
		ChainID: messageChain,
		Body:    req.Task,
		Trust:   ts.outboundTrust(),
	})
	if err != nil {
		return domain.Agent{}, domain.Message{}, fmt.Errorf("forager %s was created but its task message failed: %w", created.ID, err)
	}
	return created, msg, nil
}

func (ts *toolset) createForager(ctx context.Context, a domain.Agent, limits domain.Limits) (domain.Agent, error) {
	if creator, ok := ts.backend.(LimitedForagerCreator); ok {
		return creator.CreateForagerWithLimits(ctx, a, limits)
	}
	return ts.backend.CreateForager(ctx, a)
}

type spawnInput struct {
	HiveID    string `json:"hiveId" jsonschema:"the Hive to spawn into"`
	Role      string `json:"role,omitempty" jsonschema:"methodology role for the new Agent; a user token may choose any role, an agent token only roles in its spawn list"`
	Task      string `json:"task" jsonschema:"the task handed to the new Agent; also delivered as a kind=task message"`
	Revision  string `json:"revision,omitempty" jsonschema:"commit to check out detached, for roles that inspect one revision"`
	PRNumber  int64  `json:"pr,omitempty" jsonschema:"pull request number the new Agent relates to"`
	RequestID string `json:"requestId,omitempty" jsonschema:"optional idempotency key; retries with the same value return the existing Agent instead of creating another"`
}

type spawnOutput struct {
	Agent   agentView   `json:"agent"`
	Message messageView `json:"message"`
}

func (ts *toolset) spawn(ctx context.Context, _ *mcpsdk.CallToolRequest, in spawnInput) (*mcpsdk.CallToolResult, spawnOutput, error) {
	if in.HiveID == "" {
		return nil, spawnOutput{}, fmt.Errorf("hiveId is required")
	}
	if err := ts.requireHive(in.HiveID); err != nil {
		return nil, spawnOutput{}, err
	}
	if strings.TrimSpace(in.Task) == "" {
		return nil, spawnOutput{}, fmt.Errorf("task is required")
	}
	if utf8.RuneCountInString(in.Task) > domain.MaxMessageBody {
		return nil, spawnOutput{}, domain.ErrMessageTooLarge
	}
	m, hasMethodology, err := ts.activeMethodology(ctx, in.HiveID)
	if err != nil {
		return nil, spawnOutput{}, err
	}
	roleName := strings.TrimSpace(in.Role)
	if roleName == "" {
		if hasMethodology {
			roleName = strings.TrimSpace(m.Manifest.DefaultRole)
		}
		if roleName == "" {
			// The built-in minimal methodology names its only role worker.
			roleName = "worker"
		}
	}
	if err := ts.maySpawn(ctx, in.HiveID, roleName); err != nil {
		return nil, spawnOutput{}, err
	}
	limits := domain.Limits{}
	if hasMethodology {
		limits = m.Manifest.Limits
	}
	parentID := ""
	chainID := ""
	if ts.actor.isAgent() {
		parentID = ts.actor.agent.ID
		chainID = ts.actor.agent.ChainID
	}
	created, msg, err := ts.createSpawnedForager(ctx, spawnRequest{
		HiveID:    in.HiveID,
		RoleName:  roleName,
		Task:      in.Task,
		Revision:  in.Revision,
		PRNumber:  in.PRNumber,
		ChainID:   chainID,
		RequestID: in.RequestID,
		ParentID:  parentID,
		Limits:    limits,
	})
	if err != nil {
		return nil, spawnOutput{}, err
	}
	return nil, spawnOutput{Agent: ts.viewAgent(created), Message: ts.viewMessage(msg)}, nil
}
