package router

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func methodologyWithEvents(events ...domain.EventRule) domain.Methodology {
	return domain.Methodology{
		Manifest: domain.MethodologyManifest{
			DefaultRole: "worker",
			Roles: map[string]domain.Role{
				"worker":   {Permissions: domain.RolePermissions{Message: true}},
				"verifier": {Permissions: domain.RolePermissions{Message: true}},
				"triage":   {Permissions: domain.RolePermissions{Message: true}},
			},
			Events: events,
		},
	}
}

func TestRouteUsesMethodologyEventRole(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	meth := methodologyWithEvents(domain.EventRule{Event: "issue_labeled", Label: "autonomous", Role: "verifier"})
	meth.HiveID = hive.ID
	b.SetActiveMethodology(meth)
	r := New(b)

	event := loadEvent(t, EventIssues, "testdata/issues_labeled.json")
	event.DeliveryID = "delivery-methodology-role"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionForagerCreated {
		t.Fatalf("action = %q, want forager created (reason %q)", decision.Action, decision.Reason)
	}
	foragers := b.Foragers(hive.ID)
	if len(foragers) != 1 || foragers[0].RoleName != "verifier" {
		t.Fatalf("foragers = %+v, want one role=verifier", foragers)
	}
	messages := b.MessagesTo(hive.ID, domain.ForagerAddress(foragers[0].ID))
	if len(messages) != 1 || messages[0].Trust != domain.TrustExternal {
		t.Fatalf("task messages = %+v, want one external message", messages)
	}
}

func TestRouteMethodologyReplacesHiveConfigTriggers(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{
		Triggers: []domain.TriggerRule{{Event: "issue_labeled", Label: "autonomous"}},
	})
	// An active methodology with no matching event means the legacy trigger is
	// not consulted at all.
	meth := methodologyWithEvents(domain.EventRule{Event: MethodologyEventPRHead, Role: "verifier"})
	meth.HiveID = hive.ID
	b.SetActiveMethodology(meth)
	r := New(b)

	event := loadEvent(t, EventIssues, "testdata/issues_labeled.json")
	event.DeliveryID = "delivery-no-fallback"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionInbox {
		t.Fatalf("action = %q, want inbox; legacy triggers must not run alongside a methodology", decision.Action)
	}
	if got := len(b.Foragers(hive.ID)); got != 0 {
		t.Fatalf("foragers = %d, want 0", got)
	}
}

func TestRouteFallsBackToHiveConfigTriggers(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{
		Triggers: []domain.TriggerRule{{Event: "issue_labeled", Label: "autonomous", Preset: "legacy"}},
	})
	r := New(b)

	event := loadEvent(t, EventIssues, "testdata/issues_labeled.json")
	event.DeliveryID = "delivery-fallback-trigger"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionForagerCreated {
		t.Fatalf("action = %q, want forager created (reason %q)", decision.Action, decision.Reason)
	}
	foragers := b.Foragers(hive.ID)
	if len(foragers) != 1 || foragers[0].Preset != "legacy" || foragers[0].RoleName != "" {
		t.Fatalf("foragers = %+v, want the legacy preset and no methodology role", foragers)
	}
}

func TestRoutePRHeadRuleDoesNotWakeOwner(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	meth := methodologyWithEvents(domain.EventRule{Event: MethodologyEventPRHead, Role: "verifier"})
	meth.HiveID = hive.ID
	b.SetActiveMethodology(meth)
	owner := platformForager(t, b, hive.ID)
	r := NewWithOptions(b, Options{AppSlug: "swarmjet"})

	event := loadEvent(t, EventPullRequest, "testdata/pull_request_synchronize_by_app.json")
	event.DeliveryID = "delivery-pr-head-rule"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionForagerCreated {
		t.Fatalf("action = %q, want forager created (reason %q)", decision.Action, decision.Reason)
	}
	if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(owner.ID))); got != 0 {
		t.Fatalf("owner received %d messages, want 0", got)
	}
	var created domain.Agent
	for _, a := range b.Foragers(hive.ID) {
		if a.ID != owner.ID {
			created = a
		}
	}
	if created.ID == "" || created.RoleName != "verifier" {
		t.Fatalf("created = %+v, want a verifier", created)
	}
	if created.PRNumber != event.Number {
		t.Fatalf("pr = %d, want %d", created.PRNumber, event.Number)
	}
	messages := b.MessagesTo(hive.ID, domain.ForagerAddress(created.ID))
	if len(messages) != 1 || messages[0].Trust != domain.TrustExternal {
		t.Fatalf("task messages = %+v, want one external message", messages)
	}
}

func TestRoutePRHeadRuleIgnoresHumanPushes(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	meth := methodologyWithEvents(domain.EventRule{Event: MethodologyEventPRHead, Role: "verifier"})
	meth.HiveID = hive.ID
	b.SetActiveMethodology(meth)
	owner := platformForager(t, b, hive.ID)
	r := NewWithOptions(b, Options{AppSlug: "swarmjet"})

	event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened.json")
	event.DeliveryID = "delivery-pr-head-human"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionDelivered || decision.ForagerID != owner.ID {
		t.Fatalf("decision = %+v, want the owner to receive a human PR event", decision)
	}
	if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(owner.ID))); got != 1 {
		t.Fatalf("owner received %d messages, want 1", got)
	}
}

func TestRouteCheckFailedRule(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	meth := methodologyWithEvents(domain.EventRule{Event: MethodologyEventCheckFailed, Role: "triage"})
	meth.HiveID = hive.ID
	b.SetActiveMethodology(meth)
	owner := platformForager(t, b, hive.ID)
	r := New(b)

	event := loadEvent(t, EventCheckSuite, "testdata/check_suite_completed.json")
	event.DeliveryID = "delivery-check-failed-rule"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionForagerCreated {
		t.Fatalf("action = %q, want forager created (reason %q)", decision.Action, decision.Reason)
	}
	if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(owner.ID))); got != 0 {
		t.Fatalf("owner received %d messages, want 0", got)
	}
	var created domain.Agent
	for _, a := range b.Foragers(hive.ID) {
		if a.ID != owner.ID {
			created = a
		}
	}
	if created.RoleName != "triage" || created.Revision != event.SHA || created.PRNumber != event.Number {
		t.Fatalf("created = %+v", created)
	}
}

func TestRouteSuccessfulCheckIsNotCheckFailed(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	meth := methodologyWithEvents(domain.EventRule{Event: MethodologyEventCheckFailed, Role: "triage"})
	meth.HiveID = hive.ID
	b.SetActiveMethodology(meth)
	owner := platformForager(t, b, hive.ID)
	r := New(b)

	event := loadEvent(t, EventCheckRun, "testdata/check_run_completed.json")
	event.DeliveryID = "delivery-check-success"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionDelivered || decision.ForagerID != owner.ID {
		t.Fatalf("decision = %+v, want the owner to receive a successful check", decision)
	}
}
