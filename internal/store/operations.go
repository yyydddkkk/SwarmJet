package store

import (
	"context"
	"time"
)

// OperationsSnapshot reports durable backlog and occupied Runner slots. A
// lease counts a reserved slot, including workspace preparation and verification.
type OperationsSnapshot struct {
	InboxPending           int       `json:"inboxPending"`
	InboxRetrying          int       `json:"inboxRetrying"`
	InboxOldestWaitSeconds float64   `json:"inboxOldestWaitSeconds"`
	InboxProcessedLastHour int       `json:"inboxProcessedLastHour"`
	InboxP95ProcessSeconds float64   `json:"inboxP95ProcessSeconds"`
	AdmissionWaiting       int       `json:"admissionWaiting"`
	RunnerSlotsReserved    int       `json:"runnerSlotsReserved"`
	MeasuredAt             time.Time `json:"measuredAt"`
}

func (s *Store) OperationsSnapshot(ctx context.Context) (OperationsSnapshot, error) {
	var out OperationsSnapshot
	err := s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE attempts>1),
		COALESCE(EXTRACT(EPOCH FROM now()-min(received_at)),0)
		FROM webhook_inbox WHERE processed_at IS NULL`).Scan(&out.InboxPending, &out.InboxRetrying, &out.InboxOldestWaitSeconds)
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(percentile_cont(0.95) WITHIN GROUP
		(ORDER BY EXTRACT(EPOCH FROM processed_at-received_at)),0)
		FROM webhook_inbox WHERE processed_at>=now()-interval '1 hour'`).Scan(&out.InboxProcessedLastHour, &out.InboxP95ProcessSeconds)
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE stage='queued' AND body->>'admissionStatus'='waiting'`).Scan(&out.AdmissionWaiting)
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM leases WHERE expires_at>now() AND name LIKE 'agent-slot:%'`).Scan(&out.RunnerSlotsReserved)
	if err != nil {
		return out, err
	}
	out.MeasuredAt = time.Now().UTC()
	return out, nil
}
