package router

import (
	"context"
	"fmt"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

// ReconcileParkedPR repairs missed close webhooks using the authoritative forge
// state. The ordinary close route retains ownership checks and idempotent Inbox
// delivery, then the supervisor performs the same final turn and Cell cleanup.
func (r *Router) ReconcileParkedPR(ctx context.Context, hive domain.Hive, agent domain.Agent, read func(context.Context, string, int64) (github.PullState, error)) error {
	if agent.State != domain.AgentParked || agent.PRNumber <= 0 || agent.Revision != "" {
		return nil
	}
	pull, err := read(ctx, hive.Repository, agent.PRNumber)
	if err != nil {
		return err
	}
	if pull.State != "closed" && !pull.Merged {
		return nil
	}
	// A missing/mismatched head branch must never complete an unrelated Agent.
	if pull.HeadRef == "" || pull.HeadRef != agent.Branch || pull.Number != agent.PRNumber {
		return nil
	}
	_, err = r.Route(ctx, Event{
		Type: EventPullRequest, Action: "closed", Repository: hive.Repository,
		DeliveryID: fmt.Sprintf("reconcile-closed:%s:%d", agent.ID, agent.PRNumber),
		Number: pull.Number, Branch: pull.HeadRef, SHA: pull.HeadSHA,
		Merged: pull.Merged, URL: pull.URL,
	})
	return err
}
