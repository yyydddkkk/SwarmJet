package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/router"
)

// routeHiveDelivery sends a GitHub delivery through the deterministic Router
// when its repository belongs to a Hive. routed=false means no Hive owns the
// repository, so the caller must keep using the legacy issue scanner.
//
// Once a repository belongs to a Hive the legacy scanner never sees it: an
// event the Router does not model is ignored, not rescanned.
func (s *Service) routeHiveDelivery(ctx context.Context, job jobs.Webhook) (router.Decision, bool, error) {
	if job.Provider != "github" || s.Store == nil {
		return router.Decision{}, false, nil
	}
	repository := githubRepositoryFullName(job.Payload)
	if repository == "" {
		return router.Decision{}, false, nil
	}
	if _, err := s.Store.HiveByRepository(ctx, repository); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return router.Decision{Action: router.ActionIgnored, Repository: repository, Reason: "no Hive for repository"}, false, nil
		}
		return router.Decision{}, true, err
	}
	r := s.hiveRouter()
	if r == nil {
		return router.Decision{}, false, nil
	}
	event, ok := router.NormalizeDelivery(job.EventType, job.DeliveryID, job.Payload)
	if !ok {
		return router.Decision{Action: router.ActionIgnored, Repository: repository, Reason: "unsupported or irrelevant GitHub event"}, true, nil
	}
	decision, err := r.Route(ctx, event)
	return decision, true, err
}

// hiveRouter lazily resolves the Router exactly once. Every read goes through
// the Once: the injected field is never touched outside it, and concurrent first
// calls share the same built Router.
func (s *Service) hiveRouter() *router.Router {
	s.hiveRouterOnce.Do(func() {
		if s.HiveRouter != nil {
			s.hiveRoutes = s.HiveRouter
			return
		}
		if s.Store != nil {
			s.hiveRoutes = router.New(s.Store)
		}
	})
	return s.hiveRoutes
}

// githubRepositoryFullName reads the owner/name a Hive is keyed by. GitHub puts
// it at the top level of every delivery, whatever the event type.
func githubRepositoryFullName(payload []byte) string {
	var event struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return ""
	}
	return strings.TrimSpace(event.Repository.FullName)
}

// logHiveDelivery records the Router's decision as structured fields, so a
// delivery can be traced from GitHub to the Inbox message it produced.
func logHiveDelivery(job jobs.Webhook, decision router.Decision, err error) {
	attrs := []any{
		"provider", job.Provider,
		"delivery_id", job.DeliveryID,
		"event_type", job.EventType,
		"repository", decision.Repository,
		"action", string(decision.Action),
		"hive_id", decision.HiveID,
		"forager_id", decision.ForagerID,
		"target", string(decision.Target),
		"message_id", decision.MessageID,
	}
	if decision.Reason != "" {
		attrs = append(attrs, "reason", decision.Reason)
	}
	if err != nil {
		slog.Warn("hive event routing failed", append(attrs, "error", err)...)
		return
	}
	slog.Info("hive event routed", attrs...)
}
