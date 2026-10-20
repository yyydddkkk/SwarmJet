package forager

import (
	"context"
	"testing"
)

type queueScanStore struct {
	Store
	hives []string
}

func (s *queueScanStore) DrainSpawnQueue(_ context.Context, hiveID string) error {
	s.hives = append(s.hives, hiveID)
	return nil
}

func TestSupervisorScanDrainsSpawnQueue(t *testing.T) {
	var queue *queueScanStore
	f := newFixture(t, func(opts *Options) {
		queue = &queueScanStore{Store: opts.Store}
		opts.Store = queue
	})
	if err := f.sup.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(queue.hives) != 1 || queue.hives[0] != testHiveID {
		t.Fatalf("queue scans = %v", queue.hives)
	}
}
