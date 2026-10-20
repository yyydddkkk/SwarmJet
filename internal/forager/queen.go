package forager

import (
	"context"
	"errors"
	"fmt"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/omp"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/queen"
)

func (a *actor) address() domain.Address {
	if a.queen {
		return domain.AddressQueen
	}
	return domain.ForagerAddress(a.id)
}

func (a *actor) queenSpec(ctx context.Context, agent domain.Agent, hive domain.Hive, token string) (cell.Spec, error) {
	model := hive.Config.QueenModel
	if model == "" {
		model = a.sup.opts.Model
	}
	source, _ := a.sup.opts.Store.(MethodologyStore)
	// Only the Queen style contributes to its platform prompt. A broken or
	// absent methodology cannot remove the Hive's manager.
	files, err := omp.ConfigFiles(omp.Options{
		Model: model, LLMBaseURL: a.sup.opts.LLMBaseURL,
		RolePrompt: queen.Header(ctx, source, hive.ID),
	})
	if err != nil {
		return cell.Spec{}, err
	}
	return cell.Spec{
		AgentID: agent.ID, HiveID: hive.ID, Repository: hive.Repository,
		Branch: "queen/" + hive.ID, CLI: DefaultCLI, Model: model,
		SessionRef: agent.SessionRef, ReadOnly: true,
		MCPURL: a.sup.opts.MCPURL, MCPToken: token,
		LLMBaseURL: a.sup.opts.LLMBaseURL, LLMToken: token, ConfigFiles: files,
	}, nil
}

// prepareQueen reconciles the switch before ordinary Inbox/idle processing.
// The scan lock prevents concurrent Notify and periodic scans racing a stop
// against recovery. Turns remain governed by the actor's own mutex/context.
func (a *actor) prepareQueen(ctx context.Context, agent domain.Agent, hive domain.Hive) (domain.Agent, error) {
	var err error
	// Re-read after obtaining the scan lock; a previous scan may have changed it.
	agent, err = a.sup.opts.Store.Agent(ctx, a.id)
	if err != nil {
		return agent, err
	}
	hive, err = a.sup.opts.Store.Hive(ctx, agent.HiveID)
	if err != nil {
		return agent, err
	}
	if !hive.QueenEnabled {
		a.mu.Lock()
		a.terminal = true
		a.queue = nil
		cancel, done := a.runCancel, a.runDone
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return agent, ctx.Err()
			}
		}
		a.mu.Lock()
		a.session = nil
		a.mcpToken = ""
		a.stopped = true
		a.mu.Unlock()
		agent, err = a.sup.opts.Store.Agent(ctx, a.id)
		if err != nil {
			return agent, err
		}
		if agent.State != domain.AgentDisabled {
			if err := a.sup.opts.Runtime.Stop(ctx, agent.ID); err != nil && !errors.Is(err, cell.ErrNoCell) {
				return agent, err
			}
			if err := a.sup.opts.Store.RevokeAgentTokens(ctx, agent.ID); err != nil {
				return agent, err
			}
			if err := a.sup.opts.Store.TransitionAgent(ctx, agent.ID, agent.State, domain.AgentDisabled); err != nil {
				return agent, err
			}
			agent.State = domain.AgentDisabled
		}
		return agent, nil
	}
	a.mu.Lock()
	running, terminal := a.running, a.terminal
	a.mu.Unlock()
	if running {
		return agent, nil
	}
	switch agent.State {
	case domain.AgentDisabled, domain.AgentFailed, domain.AgentCancelled, domain.AgentCompleted:
		if err := a.sup.opts.Store.TransitionAgent(ctx, agent.ID, agent.State, domain.AgentParked); err != nil {
			return agent, err
		}
		agent.State = domain.AgentParked
	default:
		// Recover interrupted turns and failed state writes; with no local
		// turn running, an active record must not evade idle shutdown.
		if agent.State == domain.AgentActive || agent.State == domain.AgentCreating || (terminal && agent.State != domain.AgentParked) {
			if err := a.sup.opts.Store.TransitionAgent(ctx, agent.ID, agent.State, domain.AgentParked); err != nil {
				return agent, err
			}
			agent.State = domain.AgentParked
		}
	}
	a.mu.Lock()
	a.terminal = false
	a.cancelPending = false
	if terminal {
		a.failures = 0
	}
	a.mu.Unlock()
	return agent, nil
}

// failQueen keeps both the Cell volume and unhandled input for the next scan.
// It emits a platform diagnostic to the Hive, never back to its own Inbox.
func (a *actor) failQueen(ctx context.Context, reason string) {
	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return
	}
	a.terminal = true
	a.queue = nil
	session := a.session
	a.session = nil
	a.mcpToken = ""
	a.mu.Unlock()
	cleanup := context.WithoutCancel(ctx)
	if session != nil {
		_ = session.Cancel(cleanup)
		_ = session.Close()
	}
	if err := a.sup.opts.Runtime.Stop(cleanup, a.id); err != nil && !errors.Is(err, cell.ErrNoCell) {
		a.sup.log.Warn("queen: stop failed Cell", "agent", a.id, "error", err)
	}
	if _, err := a.transitionTo(cleanup, domain.AgentFailed); err != nil {
		a.sup.log.Warn("queen: mark failed", "agent", a.id, "error", err)
	}
	if err := a.sup.opts.Store.RevokeAgentTokens(cleanup, a.id); err != nil {
		a.sup.log.Warn("queen: revoke tokens", "agent", a.id, "error", err)
	}
	store, ok := a.sup.opts.Store.(QueenStore)
	if !ok {
		a.sup.log.Warn("queen: failure diagnostics unavailable", "agent", a.id)
		return
	}
	_, err := store.SendHiveDiagnostic(cleanup, a.hiveID,
		truncateRunes(fmt.Sprintf("Queen failed: %s. Recovery will be retried on the next scan while Queen remains enabled.", reason), domain.MaxMessageBody))
	if err != nil {
		a.sup.log.Warn("queen: failure notification", "agent", a.id, "error", err)
	}
}
