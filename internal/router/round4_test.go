package router

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestRouteCIRuleIgnoresSuccessButStillTriggersMergeCheck(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	forager := platformForager(t, b, hive.ID)

	var calls int
	var gotPR int64
	var gotSHA, gotConclusion string
	r := NewWithOptions(b, Options{AppSlug: "swarmjet", OnChecks: func(_ context.Context, _ domain.Hive, pr int64, sha, conclusion string) error {
		calls++
		gotPR, gotSHA, gotConclusion = pr, sha, conclusion
		return nil
	}})
	event := loadEvent(t, EventCheckRun, "testdata/check_run_completed.json")
	event.DeliveryID = "delivery-success-check"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionIgnored || !strings.Contains(decision.Reason, "does not wake the owner") {
		t.Fatalf("decision = %+v, want an explicit ignored reason", decision)
	}
	if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(forager.ID))); got != 0 {
		t.Fatalf("successful CI wrote %d messages, want 0", got)
	}
	if calls != 1 || gotPR != 34 || gotSHA != event.SHA || gotConclusion != "success" {
		t.Fatalf("OnChecks call = %d pr=%d sha=%q conclusion=%q", calls, gotPR, gotSHA, gotConclusion)
	}
}

func TestRouteCIRuleDeliversFailureAndTriggersMergeCheck(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	forager := platformForager(t, b, hive.ID)

	var conclusion string
	r := NewWithOptions(b, Options{AppSlug: "swarmjet", OnChecks: func(_ context.Context, _ domain.Hive, _ int64, _ string, got string) error {
		conclusion = got
		return nil
	}})
	event := loadEvent(t, EventCheckSuite, "testdata/check_suite_completed.json")
	event.DeliveryID = "delivery-failed-check"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionDelivered || decision.ForagerID != forager.ID {
		t.Fatalf("decision = %+v, want delivery to %q", decision, forager.ID)
	}
	if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(forager.ID))); got != 1 {
		t.Fatalf("failed CI wrote %d messages, want 1", got)
	}
	if conclusion != "failure" {
		t.Fatalf("conclusion = %q", conclusion)
	}
}

func TestRoutePlatformHeadChangeCallsOnHead(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	forager := platformForager(t, b, hive.ID)

	var called bool
	var gotHive domain.Hive
	var gotOwner domain.Agent
	var gotPR int64
	var gotSHA string
	r := NewWithOptions(b, Options{AppSlug: "swarmjet", OnHead: func(_ context.Context, h domain.Hive, owner domain.Agent, pr int64, sha string) error {
		called = true
		gotHive, gotOwner, gotPR, gotSHA = h, owner, pr, sha
		return nil
	}})
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_synchronize_by_app.json")
	event.DeliveryID = "delivery-new-head"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionIgnored {
		t.Fatalf("decision = %+v, want ignored", decision)
	}
	if !called || gotHive.ID != hive.ID || gotOwner.ID != forager.ID || gotPR != 34 || gotSHA != event.SHA {
		t.Fatalf("OnHead = %v hive=%q owner=%q pr=%d sha=%q, event sha=%q", called, gotHive.ID, gotOwner.ID, gotPR, gotSHA, event.SHA)
	}
	if event.SHA != "3f9a1c2d4e5f60718293a4b5c6d7e8f901234567" {
		t.Fatalf("normalized SHA = %q", event.SHA)
	}
	if got := len(b.Messages()); got != 0 {
		t.Fatalf("platform head change wrote %d messages, want 0", got)
	}
}

func TestRoutePlatformEditedDoesNotCallOnHead(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	platformForager(t, b, hive.ID)

	called := false
	r := NewWithOptions(b, Options{AppSlug: "swarmjet", OnHead: func(context.Context, domain.Hive, domain.Agent, int64, string) error {
		called = true
		return nil
	}})
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_edited_by_app.json")
	event.DeliveryID = "delivery-edited"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != ActionIgnored || called {
		t.Fatalf("edited event decision=%+v onHead=%v", decision, called)
	}
}
