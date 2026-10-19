package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// routingBackend is a Router backend that also implements PRRecorder. The real
// store gains SetAgentPR in the parallel data-layer task, so this fake keeps the
// wiring test independent of that landing.
type routingBackend struct {
	hive     domain.Hive
	forager  domain.Agent
	messages []domain.Message
}

func (b *routingBackend) HiveByRepository(_ context.Context, repository string) (domain.Hive, error) {
	if repository != b.hive.Repository {
		return domain.Hive{}, domain.ErrNotFound
	}
	return b.hive, nil
}

func (b *routingBackend) ForagerByIssue(context.Context, string, int64) (domain.Agent, error) {
	return domain.Agent{}, domain.ErrNotFound
}

func (b *routingBackend) ForagerByPR(context.Context, string, int64) (domain.Agent, error) {
	return domain.Agent{}, domain.ErrNotFound
}

func (b *routingBackend) ForagerByBranch(_ context.Context, hiveID, branch string) (domain.Agent, error) {
	if hiveID == b.forager.HiveID && branch == b.forager.Branch {
		return b.forager, nil
	}
	return domain.Agent{}, domain.ErrNotFound
}

func (b *routingBackend) CreateForager(_ context.Context, a domain.Agent) (domain.Agent, error) {
	return domain.Agent{}, errors.New("unexpected CreateForager")
}

func (b *routingBackend) SendMessage(_ context.Context, m domain.Message) (domain.Message, error) {
	b.messages = append(b.messages, m)
	return m, nil
}

func (b *routingBackend) SetAgentPR(_ context.Context, id string, pr int64) error {
	if id != b.forager.ID {
		return domain.ErrNotFound
	}
	b.forager.PRNumber = pr
	return nil
}

func TestGitHubWebhookPRRecordsForagerPRNumber(t *testing.T) {
	s := webhookTestStore(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/hive"})
	if err != nil {
		t.Fatal(err)
	}
	backend := &routingBackend{
		hive:    hive,
		forager: domain.Agent{ID: "forager-1", HiveID: hive.ID, Role: domain.RoleForager, State: domain.AgentActive, Branch: "swarm/acme-hive/forager-1", ChainID: "chain-1"},
	}
	svc := &Service{Store: s, HiveRouter: router.New(backend)}
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
	if backend.forager.PRNumber != 34 {
		t.Fatalf("PRNumber = %d, want 34", backend.forager.PRNumber)
	}
	if len(backend.messages) != 1 || backend.messages[0].To != domain.ForagerAddress("forager-1") {
		t.Fatalf("messages = %+v", backend.messages)
	}
	if backend.messages[0].Kind != domain.MessageEvent {
		t.Fatalf("message kind = %q", backend.messages[0].Kind)
	}
}
