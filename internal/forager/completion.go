package forager

import (
	"context"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"strings"
)

func isDeferredCompleteMessage(m domain.Message) bool {
	return isCompleteMessage(m) && m.From == domain.AddressSystem && m.Trust == domain.TrustSystem && strings.HasPrefix(m.Body, "complete\nafter_event\n")
}

func hasImmediateCompleteMessage(messages []domain.Message) bool {
	for _, m := range messages {
		if isCompleteMessage(m) && !isDeferredCompleteMessage(m) {
			return true
		}
	}
	return false
}

func completionReason(messages []domain.Message) string {
	for _, m := range messages {
		if isCompleteMessage(m) && m.Trust == domain.TrustSystem {
			if reason, ok := strings.CutPrefix(m.Body, "complete\nafter_event\n"); ok {
				return reason
			}
		}
	}
	return "finished its task"
}

func (a *actor) publishFailure(ctx context.Context, agent domain.Agent, failure error) {
	_, err := a.sup.opts.Store.SendMessage(ctx, domain.Message{
		HiveID: agent.HiveID, From: domain.AddressSystem, To: domain.ForagerAddress(agent.ID),
		Kind: domain.MessageTask, Trust: domain.TrustSystem, ChainID: agent.ChainID,
		Body: truncateRunes("Publication failed: "+failure.Error()+"\nInspect the error and retry after correcting it. If the branch needs updating, merge the latest base into your branch, resolve conflicts, then end the turn to retry publication.", domain.MaxMessageBody),
	})
	if err != nil {
		a.sup.log.Warn("forager: send publication failure to owner", "agent", agent.ID, "error", err)
	}
}

// Check beyond the prompt batch boundary so a busy owner cannot publish while
// its closing event is waiting behind earlier messages.
func (a *actor) completionPending(ctx context.Context, hiveID string) bool {
	if reader, ok := a.sup.opts.Store.(interface {
		CompletionPending(context.Context, string, string) (bool, error)
	}); ok {
		pending, err := reader.CompletionPending(ctx, hiveID, a.id)
		if err != nil {
			a.sup.log.Warn("forager: check pending completion failed", "agent", a.id, "error", err)
			return true
		}
		return pending
	}
	a.mu.Lock()
	pending := hasCompleteMessage(a.queue)
	a.mu.Unlock()
	if pending {
		return true
	}
	messages, err := a.collectMessages(ctx, hiveID)
	return err != nil || hasCompleteMessage(messages)
}

// The final lifecycle pair must survive the ordinary prompt's message and
// character caps. Its bounded event digest takes precedence over earlier work;
// initial task text is unnecessary for reporting that the task has ended.
func buildTurnPrompt(agent domain.Agent, hive domain.Hive, messages []domain.Message, firstTurn bool) string {
	for i, m := range messages {
		if isDeferredCompleteMessage(m) && i+1 < len(messages) {
			final := []domain.Message{m, messages[i+1]}
			final = append(final, messages[:i]...)
			final = append(final, messages[i+2:]...)
			return buildPrompt(agent, hive, final, false)
		}
	}
	return buildPrompt(agent, hive, messages, firstTurn)
}
