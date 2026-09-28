package main

import (
	"context"
	"log/slog"
	"time"

	"swarmjet/internal/observability"
	"swarmjet/internal/store"
)

func observeState(ctx context.Context, s *store.Store, metrics *observability.Metrics) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		snapshot, err := s.BacklogSnapshot(readCtx)
		cancel()
		metrics.State(observability.StateSnapshot{
			AdmissionWaiting:       snapshot.AdmissionWaiting,
			AdmissionOldestWaitSec: snapshot.AdmissionOldestWaitSeconds,
			CommandPending:         snapshot.CommandPending,
			CommandOldestWaitSec:   snapshot.CommandOldestWaitSeconds,
			RunnerSlotsReserved:    snapshot.RunnerSlotsReserved,
			LegacyInboxPending:     snapshot.LegacyInboxPending,
		}, err)
		if err != nil && ctx.Err() == nil {
			slog.Warn("observability state snapshot failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
