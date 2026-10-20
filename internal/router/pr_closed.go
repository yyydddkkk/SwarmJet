package router

import (
	"context"
	"fmt"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Lifecycle messages are atomic: either delivery can wake the owner, but the
// authoritative Inbox always contains both the event and completion control.
func (r *Router) closePROwner(ctx context.Context, hive domain.Hive, e Event) (Decision, bool, error) {
	if e.Type != EventPullRequest || e.Action != "closed" {
		return Decision{}, false, nil
	}
	owner, found, err := r.owningForager(ctx, hive.ID, e)
	if err != nil || !found {
		return Decision{}, false, err
	}
	sender, ok := r.backend.(interface {
		SendMessages(context.Context, ...domain.Message) ([]domain.Message, error)
	})
	if !ok {
		return Decision{}, true, fmt.Errorf("router: backend cannot atomically complete PR owner")
	}
	target := domain.ForagerAddress(owner.ID)
	id := messageID(e, target)
	reason := fmt.Sprintf("PR #%d closed (merged=%t)", e.Number, e.Merged)
	_, err = sender.SendMessages(ctx,
		domain.Message{ID: id + "-complete", HiveID: hive.ID, From: domain.AddressSystem, To: target, Kind: domain.MessageControl, ChainID: owner.ChainID, Trust: domain.TrustSystem, Body: "complete\nafter_event\n" + reason},
		domain.Message{ID: id, HiveID: hive.ID, From: domain.AddressSystem, To: target, Kind: domain.MessageEvent, ChainID: owner.ChainID, Trust: domain.TrustExternal, Body: Digest(e)},
	)
	return Decision{Action: ActionDelivered, Reason: reason, Repository: e.Repository, HiveID: hive.ID, ForagerID: owner.ID, Target: target, MessageID: id}, true, err
}

func (m *MemoryBackend) SendMessages(ctx context.Context, messages ...domain.Message) ([]domain.Message, error) {
	var sent []domain.Message
	for _, message := range messages {
		result, err := m.SendMessage(ctx, message)
		if err != nil {
			return nil, err
		}
		sent = append(sent, result)
	}
	return sent, nil
}
