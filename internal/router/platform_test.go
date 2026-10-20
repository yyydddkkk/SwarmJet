package router

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// platformForager seeds the Forager that owns the branch and PR used by the
// platform-event payloads.
func platformForager(t *testing.T, b *MemoryBackend, hiveID string) domain.Agent {
	t.Helper()
	return b.AddForager(domain.Agent{
		HiveID:   hiveID,
		Role:     domain.RoleForager,
		State:    domain.AgentParked,
		PRNumber: 34,
		Branch:   "swarm/acme-hive/forager-1",
		ChainID:  "chain-34",
	})
}

func TestRouteIgnoresPlatformGeneratedPullRequests(t *testing.T) {
	appEvents := []struct {
		name string
		file string
	}{
		{"opened", "testdata/pull_request_opened_by_app.json"},
		{"synchronize", "testdata/pull_request_synchronize_by_app.json"},
		{"edited", "testdata/pull_request_edited_by_app.json"},
	}

	t.Run("with configured App slug", func(t *testing.T) {
		for _, tc := range appEvents {
			t.Run(tc.name, func(t *testing.T) {
				b := NewMemoryBackend()
				hive := newTestHive(t, b, domain.HiveConfig{})
				forager := platformForager(t, b, hive.ID)
				r := NewWithOptions(b, Options{AppSlug: "swarmjet"})

				event := loadEvent(t, EventPullRequest, tc.file)
				event.DeliveryID = "delivery-platform-" + tc.name
				decision, err := r.Route(context.Background(), event)
				if err != nil {
					t.Fatalf("Route: %v", err)
				}
				if decision.Action != ActionIgnored {
					t.Fatalf("action = %q, want ignored (reason %q)", decision.Action, decision.Reason)
				}
				if !strings.Contains(decision.Reason, "platform-generated") {
					t.Fatalf("reason = %q, want an explicit platform-generated explanation", decision.Reason)
				}
				if got := len(b.Messages()); got != 0 {
					t.Fatalf("ignored event wrote %d messages, want 0", got)
				}
				if forager.PRNumber != 34 {
					t.Fatalf("ignored event changed the Forager PR to %d", forager.PRNumber)
				}
			})
		}
	})

	t.Run("fallback uses Bot sender and branch owner", func(t *testing.T) {
		for _, tc := range appEvents {
			t.Run(tc.name, func(t *testing.T) {
				b := NewMemoryBackend()
				hive := newTestHive(t, b, domain.HiveConfig{})
				forager := platformForager(t, b, hive.ID)
				r := New(b) // no App slug

				event := loadEvent(t, EventPullRequest, tc.file)
				event.DeliveryID = "delivery-fallback-" + tc.name
				decision, err := r.Route(context.Background(), event)
				if err != nil {
					t.Fatalf("Route: %v", err)
				}
				if decision.Action != ActionIgnored {
					t.Fatalf("action = %q, want ignored (reason %q)", decision.Action, decision.Reason)
				}
				if decision.ForagerID != forager.ID {
					t.Fatalf("forager = %q, want %q", decision.ForagerID, forager.ID)
				}
				if got := len(b.Messages()); got != 0 {
					t.Fatalf("ignored event wrote %d messages, want 0", got)
				}
			})
		}
	})

	t.Run("fallback requires an owned branch", func(t *testing.T) {
		b := NewMemoryBackend()
		newTestHive(t, b, domain.HiveConfig{})
		r := New(b)

		event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened_by_app.json")
		event.DeliveryID = "delivery-unowned"
		event.Branch = "feature/human-work"
		decision, err := r.Route(context.Background(), event)
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if decision.Action == ActionIgnored {
			t.Fatalf("bot event on an unowned branch was ignored: %q", decision.Reason)
		}
	})

	t.Run("configured slug does not ignore another bot", func(t *testing.T) {
		b := NewMemoryBackend()
		hive := newTestHive(t, b, domain.HiveConfig{})
		platformForager(t, b, hive.ID)
		r := NewWithOptions(b, Options{AppSlug: "swarmjet"})

		event := loadEvent(t, EventPullRequest, "testdata/pull_request_synchronize_by_app.json")
		event.DeliveryID = "delivery-other-bot"
		event.SenderLogin = "github-actions[bot]"
		decision, err := r.Route(context.Background(), event)
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if decision.Action != ActionDelivered {
			t.Fatalf("action = %q, want delivered for another bot (reason %q)", decision.Action, decision.Reason)
		}
	})
}

func TestRouteStillDeliversHumanAndCIEvents(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		file      string
	}{
		{"human PR opened", EventPullRequest, "testdata/pull_request_opened.json"},
		{"human review", EventPullRequestReview, "testdata/pull_request_review_submitted.json"},
		{"human review comment", EventPullRequestReviewComment, "testdata/pull_request_review_comment_created.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewMemoryBackend()
			hive := newTestHive(t, b, domain.HiveConfig{})
			forager := platformForager(t, b, hive.ID)
			r := NewWithOptions(b, Options{AppSlug: "swarmjet"})

			event := loadEvent(t, tc.eventType, tc.file)
			event.DeliveryID = "delivery-human-" + tc.name
			decision, err := r.Route(context.Background(), event)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if decision.Action != ActionDelivered || decision.ForagerID != forager.ID {
				t.Fatalf("decision = %+v, want delivery to %q", decision, forager.ID)
			}
			if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(forager.ID))); got != 1 {
				t.Fatalf("forager messages = %d, want 1", got)
			}
		})
	}
}

func TestNormalizeAppPullRequestSenderFields(t *testing.T) {
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_edited_by_app.json")
	if event.Action != "edited" {
		t.Fatalf("action = %q, want edited", event.Action)
	}
	if event.SenderLogin != "swarmjet[bot]" || event.SenderType != "Bot" {
		t.Fatalf("sender = %q/%q, want swarmjet[bot]/Bot", event.SenderLogin, event.SenderType)
	}
	if event.Branch != "swarm/acme-hive/forager-1" {
		t.Fatalf("branch = %q", event.Branch)
	}
}

func TestNormalizeAppSlug(t *testing.T) {
	for raw, want := range map[string]string{
		"":                 "",
		"swarmjet":         "swarmjet",
		"  swarmjet  ":     "swarmjet",
		"SwarmJet[bot]":    "SwarmJet",
		"swarmjet[bot]":    "swarmjet",
		"another-ent[bot]": "another-ent",
	} {
		if got := normalizeAppSlug(raw); got != want {
			t.Errorf("normalizeAppSlug(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestRouteReadsAppSlugSourceDynamically(t *testing.T) {
	b := NewMemoryBackend()
	newTestHive(t, b, domain.HiveConfig{})
	slug := ""
	r := NewWithOptions(b, Options{AppSlugSource: func(context.Context) string { return slug }})

	// The bot sender is ours, but the branch is not owned by a Forager. Without
	// a slug the Router cannot attribute the event and does not ignore it.
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened_by_app.json")
	event.DeliveryID = "dynamic-slug-1"
	event.Branch = "feature/human-work"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route without slug: %v", err)
	}
	if decision.Action == ActionIgnored {
		t.Fatalf("event was ignored before the App slug was known: %q", decision.Reason)
	}

	// Once the Provider learns the slug, the same Router recognises the sender
	// without being rebuilt.
	slug = "swarmjet"
	event = loadEvent(t, EventPullRequest, "testdata/pull_request_opened_by_app.json")
	event.DeliveryID = "dynamic-slug-2"
	event.Branch = "feature/human-work"
	decision, err = r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route with slug: %v", err)
	}
	if decision.Action != ActionIgnored {
		t.Fatalf("action = %q, want ignored after the slug was learned (reason %q)", decision.Action, decision.Reason)
	}
}
