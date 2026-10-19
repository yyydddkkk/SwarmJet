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
		{"failing CI", EventCheckSuite, "testdata/check_suite_completed.json"},
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

func TestPullRequestNormalizesHeadSHA(t *testing.T) {
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened.json")
	if event.SHA != "6dcb09b5b57875f334f61aebed695e2e4193db5e" {
		t.Fatalf("SHA = %q, want the pull_request.head.sha", event.SHA)
	}
}

func TestPlatformHeadCallsOnHeadWithoutWakingOwner(t *testing.T) {
	type headCall struct {
		hiveID  string
		ownerID string
		pr      int64
		sha     string
	}
	var calls []headCall

	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	forager := platformForager(t, b, hive.ID)
	r := NewWithOptions(b, Options{
		AppSlug: "swarmjet",
		OnHead: func(_ context.Context, gotHive domain.Hive, owner domain.Agent, pr int64, sha string) error {
			calls = append(calls, headCall{hiveID: gotHive.ID, ownerID: owner.ID, pr: pr, sha: sha})
			return nil
		},
	})

	event := loadEvent(t, EventPullRequest, "testdata/pull_request_synchronize_by_app.json")
	event.DeliveryID = "delivery-on-head-synchronize"
	decision, err := r.Route(context.Background(), event)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Action != ActionIgnored {
		t.Fatalf("action = %q, want ignored", decision.Action)
	}
	if len(calls) != 1 {
		t.Fatalf("OnHead calls = %d, want 1", len(calls))
	}
	want := headCall{
		hiveID:  hive.ID,
		ownerID: forager.ID,
		pr:      34,
		sha:     "3f9a1c2d4e5f60718293a4b5c6d7e8f901234567",
	}
	if calls[0] != want {
		t.Fatalf("OnHead call = %+v, want %+v", calls[0], want)
	}
	if got := len(b.MessagesTo(hive.ID, domain.ForagerAddress(forager.ID))); got != 0 {
		t.Fatalf("platform event woke the owner with %d messages, want 0", got)
	}
}

func TestPlatformHeadSkipsEditedEvents(t *testing.T) {
	b := NewMemoryBackend()
	hive := newTestHive(t, b, domain.HiveConfig{})
	platformForager(t, b, hive.ID)
	called := false
	r := NewWithOptions(b, Options{
		AppSlug: "swarmjet",
		OnHead: func(context.Context, domain.Hive, domain.Agent, int64, string) error {
			called = true
			return nil
		},
	})
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_edited_by_app.json")
	event.DeliveryID = "delivery-on-head-edited"
	if _, err := r.Route(context.Background(), event); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if called {
		t.Fatal("OnHead fired for an edited pull_request without a new head")
	}
}

func TestCheckConclusionsChooseOwnerDelivery(t *testing.T) {
	successConclusions := []string{"success", "neutral", "skipped", "cancelled"}
	failureConclusions := []string{"failure", "timed_out", "action_required", "startup_failure"}

	for _, conclusion := range append(append([]string{}, successConclusions...), failureConclusions...) {
		conclusion := conclusion
		wantWake := false
		for _, failure := range failureConclusions {
			if conclusion == failure {
				wantWake = true
			}
		}
		t.Run(conclusion, func(t *testing.T) {
			var calls []string
			b := NewMemoryBackend()
			hive := newTestHive(t, b, domain.HiveConfig{})
			forager := platformForager(t, b, hive.ID)
			r := NewWithOptions(b, Options{
				AppSlug: "swarmjet",
				OnChecks: func(_ context.Context, gotHive domain.Hive, pr int64, sha, gotConclusion string) error {
					if gotHive.ID != hive.ID || pr != 34 || sha == "" {
						t.Errorf("OnChecks args = %s/%d/%q, want hive/34/non-empty SHA", gotHive.ID, pr, sha)
					}
					calls = append(calls, gotConclusion)
					return nil
				},
			})

			event := loadEvent(t, EventCheckRun, "testdata/check_run_completed.json")
			event.DeliveryID = "delivery-check-" + conclusion
			event.Conclusion = conclusion
			decision, err := r.Route(context.Background(), event)
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if len(calls) != 1 || calls[0] != conclusion {
				t.Fatalf("OnChecks calls = %v, want [%q]", calls, conclusion)
			}
			messages := b.MessagesTo(hive.ID, domain.ForagerAddress(forager.ID))
			if wantWake {
				if decision.Action != ActionDelivered || len(messages) != 1 {
					t.Fatalf("failure conclusion: decision = %+v, messages = %d, want delivery to owner", decision, len(messages))
				}
				return
			}
			if decision.Action != ActionIgnored || len(messages) != 0 {
				t.Fatalf("success conclusion: decision = %+v, messages = %d, want ignored without waking owner", decision, len(messages))
			}
		})
	}
}
