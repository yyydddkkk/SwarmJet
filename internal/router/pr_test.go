package router

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func openedPRForager(t *testing.T, b *MemoryBackend) (domain.Hive, domain.Agent) {
	t.Helper()
	hive := newTestHive(t, b, domain.HiveConfig{})
	forager := b.AddForager(domain.Agent{
		HiveID:  hive.ID,
		Role:    domain.RoleForager,
		State:   domain.AgentActive,
		Branch:  "swarm/acme-hive/forager-1",
		ChainID: "chain-1",
	})
	return hive, forager
}

func TestPullRequestOpenedRecordsPRNumber(t *testing.T) {
	b := NewMemoryBackend()
	hive, forager := openedPRForager(t, b)
	r := New(b)

	event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened.json")
	event.DeliveryID = "delivery-pr-opened"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionDelivered || decision.ForagerID != forager.ID {
		t.Fatalf("decision = %+v, want delivery to %s", decision, forager.ID)
	}
	if !strings.Contains(decision.Reason, "recorded PR #34") {
		t.Fatalf("reason = %q, want the recorded PR", decision.Reason)
	}
	if got := b.Foragers(hive.ID)[0].PRNumber; got != 34 {
		t.Fatalf("PRNumber = %d, want 34", got)
	}

	// A redelivery must not change the link or fail.
	event.DeliveryID = "delivery-pr-opened-2"
	if _, err := r.Route(context.Background(), event); err != nil {
		t.Fatalf("Route redelivery: %v", err)
	}
	if got := b.Foragers(hive.ID)[0].PRNumber; got != 34 {
		t.Fatalf("PRNumber after redelivery = %d, want 34", got)
	}
}

func TestPullRequestReopenedRecordsPRNumber(t *testing.T) {
	payload, err := os.ReadFile("testdata/pull_request_opened.json")
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), `"action": "opened"`, `"action": "reopened"`, 1))
	event, ok := NormalizeDelivery(EventPullRequest, "delivery-pr-reopened", payload)
	if !ok {
		t.Fatal("NormalizeDelivery(reopened) = false")
	}
	if event.Action != "reopened" {
		t.Fatalf("action = %q", event.Action)
	}

	b := NewMemoryBackend()
	hive, forager := openedPRForager(t, b)
	decision, err := New(b).Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionDelivered || decision.ForagerID != forager.ID {
		t.Fatalf("decision = %+v", decision)
	}
	if got := b.Foragers(hive.ID)[0].PRNumber; got != 34 {
		t.Fatalf("PRNumber = %d, want 34", got)
	}
}

func TestPullRequestNeverOverwritesLinkedPR(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	b.AddForager(domain.Agent{
		HiveID:   hive.ID,
		Role:     domain.RoleForager,
		State:    domain.AgentActive,
		Branch:   "swarm/acme-hive/forager-1",
		ChainID:  "chain-1",
		PRNumber: 34,
	})
	r := New(b)
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened.json")
	event.DeliveryID = "delivery-pr-other"
	event.Number = 35
	if _, err := r.Route(context.Background(), event); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if got := b.Foragers(hive.ID)[0].PRNumber; got != 34 {
		t.Fatalf("PRNumber = %d, want the existing 34", got)
	}
}

func TestPullRequestWithoutMatchingForagerGoesToInbox(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	forager := b.AddForager(domain.Agent{
		HiveID:  hive.ID,
		Role:    domain.RoleForager,
		State:   domain.AgentActive,
		Branch:  "swarm/acme-hive/someone-else",
		ChainID: "chain-1",
	})
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened.json")
	event.DeliveryID = "delivery-pr-unowned"
	decision, err := New(b).Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionInbox {
		t.Fatalf("action = %q, want inbox", decision.Action)
	}
	if got := b.Foragers(hive.ID)[0].PRNumber; got != 0 {
		t.Fatalf("PRNumber = %d, want 0", got)
	}
	if forager.ID == "" {
		t.Fatal("fixture lost its forager")
	}
}
