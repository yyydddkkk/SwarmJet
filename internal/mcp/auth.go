package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// actor is the identity a token acts as. A default or admin token acts as the
// local client that owns it (local:<token-id>); an agent token acts as the one
// Agent it was minted for, so its messages can never claim another sender.
type actor struct {
	token domain.Token
	agent *domain.Agent // nil unless the token scope is agent
}

func (a actor) isAgent() bool   { return a.agent != nil }
func (a actor) isForager() bool { return a.agent != nil && a.agent.Role == domain.RoleForager }

// address is the fixed From address for messages sent by this actor.
func (a actor) address() domain.Address {
	if a.agent == nil {
		return domain.LocalAddress(a.token.ID)
	}
	if a.agent.Role == domain.RoleQueen {
		return domain.AddressQueen
	}
	return domain.ForagerAddress(a.agent.ID)
}

// resolveActor binds an agent-scoped token to its Agent record. The Agent is read
// once when the MCP session is created; a token that names a missing Agent is
// rejected on every call.
func resolveActor(ctx context.Context, backend Backend, token domain.Token) (actor, error) {
	a := actor{token: token}
	if token.Scope != domain.TokenAgent {
		return a, nil
	}
	if token.AgentID == "" {
		return a, fmt.Errorf("permission denied: agent token %s has no agent", token.ID)
	}
	agent, err := backend.Agent(ctx, token.AgentID)
	if err != nil {
		return a, fmt.Errorf("permission denied: agent token %s does not resolve to an agent: %w", token.ID, err)
	}
	a.agent = &agent
	return a, nil
}

// agentTools is the permission table for agent-scoped tokens (ADR section 12).
// A tool that is absent from the table is denied, even if it is registered.
var agentTools = map[domain.AgentRole]map[string]bool{
	domain.RoleForager: {
		"finish_task":    true,
		"send":           true,
		"spawn":          true,
		"get_forager":    true,
		"inbox":          true,
		"update_handoff": true,
		"attest":         true,
		"pr_status":      true,
		"merge_pr":       true,
	},
	domain.RoleQueen: {
		"send":                true,
		"spawn":               true,
		"cancel_forager":      true,
		"get_forager":         true,
		"read_forager_detail": true,
		"inbox":               true,
		"claim":               true,
		"attest":              true,
		"pr_status":           true,
		"merge_pr":            true,
	},
}

// visible reports whether the tool is listed for this session. Default and admin
// tokens see the whole catalog; agent tokens only see the tools they may call.
func (ts *toolset) visible(tool string) bool {
	if methodologyTools[tool] {
		// Reads are open to every token; publish and activate enforce the
		// effective role permission in their handlers.
		return true
	}
	if ts.token.Scope != domain.TokenAgent {
		return true
	}
	if ts.actorErr != nil {
		// Keep the catalog intact so calls fail with the resolution error below
		// instead of looking like an unknown tool.
		return true
	}
	if ts.actor.agent == nil {
		return true
	}
	return agentTools[ts.actor.agent.Role][tool]
}

// authorize enforces the scope table. It is called before every tool handler and
// is deliberately independent of visibility: a tool that is reachable anyway is
// still rejected.
func (ts *toolset) authorize(tool string) error {
	if methodologyTools[tool] {
		if ts.token.Scope == domain.TokenAgent && ts.actorErr != nil {
			return ts.actorErr
		}
		return nil
	}
	switch ts.token.Scope {
	case domain.TokenAdmin:
		return nil
	case domain.TokenDefault:
		switch tool {
		case "create_hive", "update_hive", "set_model_credential":
			return fmt.Errorf("permission denied: %s requires an admin-scoped token (this token is %q)", tool, ts.token.Scope)
		default:
			return nil
		}
	case domain.TokenAgent:
		if ts.actorErr != nil {
			return ts.actorErr
		}
		if ts.actor.agent == nil {
			return fmt.Errorf("permission denied: agent token %s has no agent", ts.token.ID)
		}
		if !agentTools[ts.actor.agent.Role][tool] {
			return fmt.Errorf("permission denied: %s is not available to an agent-scoped token for %s %s", tool, ts.actor.agent.Role, ts.actor.agent.ID)
		}
		return nil
	default:
		return fmt.Errorf("permission denied: unsupported token scope %q", ts.token.Scope)
	}
}

// requireHive keeps an agent inside its own Hive.
func (ts *toolset) requireHive(hiveID string) error {
	if !ts.actor.isAgent() {
		return nil
	}
	if ts.actor.agent.HiveID != hiveID {
		return fmt.Errorf("permission denied: agent %s can only access Hive %s", ts.actor.agent.ID, ts.actor.agent.HiveID)
	}
	return nil
}

// requireForagerTarget keeps a Forager to itself and a Queen to the Foragers of
// its own Hive.
func (ts *toolset) requireForagerTarget(a domain.Agent) error {
	if !ts.actor.isAgent() {
		return nil
	}
	if ts.actor.isForager() {
		if a.ID != ts.actor.agent.ID {
			return fmt.Errorf("permission denied: forager %s can only access itself", ts.actor.agent.ID)
		}
		return nil
	}
	if a.HiveID != ts.actor.agent.HiveID {
		return fmt.Errorf("permission denied: agent %s can only access Hive %s", ts.actor.agent.ID, ts.actor.agent.HiveID)
	}
	if a.Role != domain.RoleForager {
		return fmt.Errorf("agent %s is a %s, not a forager", a.ID, a.Role)
	}
	return nil
}

// guarded wraps a tool handler with the scope check. The wrapper always runs
// before argument validation, so a denied caller never learns anything about the
// requested resource.
func guarded[In, Out any](ts *toolset, tool string, next mcpsdk.ToolHandlerFor[In, Out]) mcpsdk.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, Out, error) {
		if err := ts.authorize(tool); err != nil {
			var zero Out
			return nil, zero, err
		}
		return next(ctx, req, in)
	}
}

// addTool registers a tool only when the session's scope may see it.
func addTool[In, Out any](ts *toolset, s *mcpsdk.Server, tool *mcpsdk.Tool, h mcpsdk.ToolHandlerFor[In, Out]) {
	if !ts.visible(tool.Name) {
		return
	}
	mcpsdk.AddTool(s, tool, guarded(ts, tool.Name, h))
}
