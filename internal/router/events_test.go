package router

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type eventPublishingBackend struct {
	*MemoryBackend
	hiveID string
	pr     int64
	calls  int
}

func (b *eventPublishingBackend) PublishPREvent(_ context.Context, hiveID string, pr int64) error {
	b.hiveID = hiveID
	b.pr = pr
	b.calls++
	return nil
}

func TestRoutedPREventPublishesNotification(t *testing.T) {
	backend := &eventPublishingBackend{MemoryBackend: NewMemoryBackend()}
	hive := newTestHive(t, backend.MemoryBackend, domain.HiveConfig{})
	event := loadEvent(t, EventPullRequest, "testdata/pull_request_opened.json")
	decision, err := New(backend).Route(context.Background(), event)
	if err != nil || decision.HiveID != hive.ID || backend.calls != 1 || backend.hiveID != hive.ID || backend.pr != event.Number {
		t.Fatalf("decision=%+v err=%v notification=%+v", decision, err, backend)
	}
}
