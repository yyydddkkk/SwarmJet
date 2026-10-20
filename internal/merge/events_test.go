package merge

import (
	"context"
	"testing"
)

type eventPublishingStore struct {
	*fakeStore
	hiveID string
	pr     int64
	calls  int
}

func (s *eventPublishingStore) PublishPREvent(_ context.Context, hiveID string, pr int64) error {
	s.hiveID = hiveID
	s.pr = pr
	s.calls++
	return nil
}

func TestSuccessfulMergePublishesPREvent(t *testing.T) {
	backend := &eventPublishingStore{fakeStore: &fakeStore{hive: testHive()}}
	svc := newTestService(t, readyStub(), backend, &fakeAttestations{latest: passAttestation()}, methodologyWith(requireAllPolicy()), nil)
	outcome, err := svc.Merge(context.Background(), testHive(), 7)
	if err != nil || !outcome.Merged || backend.calls != 1 || backend.hiveID != testHive().ID || backend.pr != 7 {
		t.Fatalf("outcome=%+v err=%v notification=%+v", outcome, err, backend)
	}
}
