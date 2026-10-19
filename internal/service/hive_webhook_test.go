package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/router"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// webhookTestStore is a migrated test schema plus a real River client, so
// Router.SendMessage can insert its delivery job in the same transaction.
func webhookTestStore(t *testing.T) *store.Store {
	t.Helper()
	s := scanTestStore(t)
	queue, err := jobs.Open(s.Pool, jobs.Handlers{}, jobs.Config{})
	if err != nil {
		t.Fatalf("jobs.Open: %v", err)
	}
	s.Jobs = queue
	return s
}

func githubIssuePayload(repository string, issue int64) []byte {
	return []byte(fmt.Sprintf(`{
		"action": "opened",
		"issue": {"number": %d, "title": "Flaky test", "body": "please fix", "user": {"login": "octo"}, "author_association": "MEMBER"},
		"repository": {"id": 4242, "full_name": %q},
		"sender": {"login": "octo"}
	}`, issue, repository))
}

func TestGitHubWebhookRoutesHiveRepositoryToRouter(t *testing.T) {
	s := webhookTestStore(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/hive"})
	if err != nil {
		t.Fatalf("CreateHive: %v", err)
	}
	svc := &Service{Store: s}
	job := jobs.Webhook{Provider: "github", DeliveryID: "delivery-hive-1", EventType: "issues", Payload: githubIssuePayload("acme/hive", 7)}
	if err := svc.HandleWebhookJob(ctx, job); err != nil {
		t.Fatalf("HandleWebhookJob: %v", err)
	}
	msgs, err := s.Inbox(ctx, hive.ID, domain.AddressHive, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("Hive Inbox has %d messages, want 1", len(msgs))
	}
	if msgs[0].Kind != domain.MessageEvent || !strings.Contains(msgs[0].Body, "GitHub event: issues.opened") {
		t.Fatalf("message = %+v", msgs[0])
	}
	// The Router derives gh-<hash> ids; the legacy scanner never writes Hive
	// Inbox messages, so this also shows the repository skipped the old path.
	if !strings.HasPrefix(msgs[0].ID, "gh-") {
		t.Fatalf("message id = %q, want a Router id", msgs[0].ID)
	}
	// A redelivery is idempotent.
	if err := svc.HandleWebhookJob(ctx, job); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if msgs, err = s.Inbox(ctx, hive.ID, domain.AddressHive, "", 10); err != nil || len(msgs) != 1 {
		t.Fatalf("redelivery produced %d messages (err %v), want 1", len(msgs), err)
	}
}

func TestGitHubWebhookWithoutHiveKeepsLegacyPath(t *testing.T) {
	s := webhookTestStore(t)
	ctx := context.Background()
	svc := &Service{Store: s}
	job := jobs.Webhook{Provider: "github", DeliveryID: "delivery-legacy-1", EventType: "issues", Payload: githubIssuePayload("acme/unknown", 7)}

	decision, routed, err := svc.routeHiveDelivery(ctx, job)
	if err != nil {
		t.Fatalf("routeHiveDelivery: %v", err)
	}
	if routed {
		t.Fatalf("unknown repository was routed to the Hive Router: %+v", decision)
	}
	// The legacy scan has no projects configured, so this stays a no-op and,
	// importantly, does not create any Hive message.
	if err := svc.HandleWebhookJob(ctx, job); err != nil {
		t.Fatalf("HandleWebhookJob: %v", err)
	}
	if hives, err := s.Hives(ctx); err != nil || len(hives) != 0 {
		t.Fatalf("hives = %+v, err = %v", hives, err)
	}
}

func TestGitHubWebhookHiveRepositorySkipsLegacyScanForUnmodelledEvent(t *testing.T) {
	s := webhookTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/hive"}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s}
	job := jobs.Webhook{Provider: "github", DeliveryID: "delivery-ping-1", EventType: "ping", Payload: []byte(`{"zen":"hi","repository":{"id":4242,"full_name":"acme/hive"}}`)}
	decision, routed, err := svc.routeHiveDelivery(ctx, job)
	if err != nil {
		t.Fatalf("routeHiveDelivery: %v", err)
	}
	if !routed || decision.Action != router.ActionIgnored {
		t.Fatalf("decision = %+v, routed = %v", decision, routed)
	}
	if err := svc.HandleWebhookJob(ctx, job); err != nil {
		t.Fatalf("HandleWebhookJob: %v", err)
	}
}

func TestGitHubWebhookRecordsPRThroughStore(t *testing.T) {
	s := webhookTestStore(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/hive"})
	if err != nil {
		t.Fatal(err)
	}
	forager, err := s.CreateForager(ctx, domain.Agent{
		HiveID:  hive.ID,
		Role:    domain.RoleForager,
		State:   domain.AgentActive,
		Branch:  "swarm/acme-hive/forager-1",
		ChainID: "chain-1",
	})
	if err != nil {
		t.Fatalf("CreateForager: %v", err)
	}
	svc := &Service{Store: s}
	payload := []byte(`{
		"action": "opened",
		"number": 34,
		"pull_request": {"number": 34, "title": "Fix", "body": "done", "merged": false, "user": {"login": "octo"}, "author_association": "MEMBER", "head": {"ref": "swarm/acme-hive/forager-1", "sha": "abc"}},
		"repository": {"id": 4242, "full_name": "acme/hive"},
		"sender": {"login": "octo"}
	}`)
	job := jobs.Webhook{Provider: "github", DeliveryID: "delivery-pr-1", EventType: "pull_request", Payload: payload}
	if err := svc.HandleWebhookJob(ctx, job); err != nil {
		t.Fatalf("HandleWebhookJob: %v", err)
	}
	got, err := s.Agent(ctx, forager.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRNumber != 34 {
		t.Fatalf("PRNumber = %d, want 34", got.PRNumber)
	}
	msgs, err := s.Inbox(ctx, hive.ID, domain.ForagerAddress(forager.ID), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Kind != domain.MessageEvent {
		t.Fatalf("forager inbox = %+v", msgs)
	}
	if !strings.Contains(msgs[0].Body, "pull_request.opened") {
		t.Fatalf("message body = %q", msgs[0].Body)
	}
}

// TestHiveRouterIsBuiltOnceConcurrently guards the sync.Once contract: the first
// calls from many goroutines must all observe the same Router. -race is the real
// assertion here.
func TestHiveRouterIsBuiltOnceConcurrently(t *testing.T) {
	svc := &Service{Store: &store.Store{}}
	const workers = 64
	routers := make([]*router.Router, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			routers[i] = svc.hiveRouter()
		}(i)
	}
	wg.Wait()
	if routers[0] == nil {
		t.Fatal("hiveRouter returned nil")
	}
	for i, got := range routers {
		if got != routers[0] {
			t.Fatalf("worker %d observed a different Router", i)
		}
	}
}

func TestHiveRouterUsesInjectedRouter(t *testing.T) {
	injected := router.New(router.NewMemoryBackend())
	svc := &Service{Store: &store.Store{}, HiveRouter: injected}
	const workers = 32
	routers := make([]*router.Router, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			routers[i] = svc.hiveRouter()
		}(i)
	}
	wg.Wait()
	for i, got := range routers {
		if got != injected {
			t.Fatalf("worker %d did not observe the injected Router", i)
		}
	}
}

func TestHiveRouterWithoutStoreIsNil(t *testing.T) {
	svc := &Service{}
	if got := svc.hiveRouter(); got != nil {
		t.Fatalf("hiveRouter = %v, want nil", got)
	}
	if got := svc.hiveRouter(); got != nil {
		t.Fatalf("second hiveRouter = %v, want nil", got)
	}
}
