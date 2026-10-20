package router

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestRouteFallbackInboxFollowsQueenSwitch(t *testing.T) {
	for _, tt := range []struct {
		name         string
		queenEnabled bool
		wantTarget   domain.Address
	}{
		{name: "queen enabled", queenEnabled: true, wantTarget: domain.AddressQueen},
		{name: "queen disabled", wantTarget: domain.AddressHive},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := NewMemoryBackend()
			hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive", QueenEnabled: tt.queenEnabled})
			event := loadEvent(t, EventIssues, "testdata/issues_opened.json")
			event.DeliveryID = "fallback-inbox"

			decision, err := New(b).Route(context.Background(), event)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if decision.Action != ActionInbox || decision.Target != tt.wantTarget || decision.MessageID == "" || decision.HiveID != hive.ID || decision.Repository != hive.Repository {
				t.Fatalf("decision = %+v, want inbox at %q", decision, tt.wantTarget)
			}
			messages := b.MessagesTo(hive.ID, tt.wantTarget)
			if len(messages) != 1 || messages[0].ID != decision.MessageID || messages[0].From != domain.AddressSystem || messages[0].To != tt.wantTarget || messages[0].Kind != domain.MessageEvent || messages[0].Trust != domain.TrustExternal || messages[0].State != domain.MessagePending || messages[0].ChainID != chainIDFor(event) || !strings.Contains(messages[0].Body, externalMarker) {
				t.Fatalf("inbox messages = %+v", messages)
			}
			if got := len(b.Messages()); got != 1 {
				t.Fatalf("message count = %d, want 1", got)
			}
		})
	}
}

func TestRouteQueenFallbackPreservesTriggerReason(t *testing.T) {
	b := NewMemoryBackend()
	hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive", QueenEnabled: true, Config: labelTrigger("fixer")})
	event := loadEvent(t, EventIssues, "testdata/issues_labeled.json")
	event.DeliveryID = "denied-trigger"
	event.AuthorAssociation = "NONE"

	decision, err := New(b).Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionInbox || decision.Target != domain.AddressQueen || !strings.Contains(decision.Reason, "association") {
		t.Fatalf("decision = %+v, want reasoned Queen fallback", decision)
	}
	messages := b.MessagesTo(hive.ID, domain.AddressQueen)
	if len(messages) != 1 || !strings.Contains(messages[0].Body, decision.Reason) || messages[0].Trust != domain.TrustExternal {
		t.Fatalf("Queen messages = %+v", messages)
	}
	if got := len(b.Foragers(hive.ID)); got != 0 {
		t.Fatalf("Foragers = %d, want 0", got)
	}
}

func TestRouteQueenEnabledKeepsRuleAndOwnerPriority(t *testing.T) {
	b := NewMemoryBackend()
	hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive", QueenEnabled: true, Config: labelTrigger("fixer")})
	r := New(b)
	event := loadEvent(t, EventIssues, "testdata/issues_labeled.json")
	event.DeliveryID = "rule-first"

	created, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("trigger Route: %v", err)
	}
	if created.Action != ActionForagerCreated || created.Target != domain.ForagerAddress(created.ForagerID) || created.ForagerID == "" {
		t.Fatalf("trigger decision = %+v", created)
	}
	event = loadEvent(t, EventIssues, "testdata/issues_opened.json")
	event.DeliveryID = "owner-next"
	delivered, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("owner Route: %v", err)
	}
	if delivered.Action != ActionDelivered || delivered.ForagerID != created.ForagerID || delivered.Target != created.Target {
		t.Fatalf("owner decision = %+v", delivered)
	}
	if got := len(b.MessagesTo(hive.ID, domain.AddressQueen)); got != 0 {
		t.Fatalf("Queen messages = %d, want 0", got)
	}
}

func TestResolveConfigChainDefaults(t *testing.T) {
	defaults := resolveConfig(domain.HiveConfig{})
	if defaults.MaxHops != 50 || defaults.MaxForagersPerChain != 50 {
		t.Fatalf("defaults = %+v, want both chain limits 50", defaults)
	}
	explicit := resolveConfig(domain.HiveConfig{MaxHops: 3, MaxForagersPerChain: 7})
	if explicit.MaxHops != 3 || explicit.MaxForagersPerChain != 7 {
		t.Fatalf("explicit limits changed: %+v", explicit)
	}
}
