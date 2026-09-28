package store

import (
	"context"
	"time"
)

// OperationsSnapshot reports durable backlog and occupied Runner slots. A
// lease counts a reserved slot, including workspace preparation and verification.
type OperationsSnapshot struct {
	InboxPending               int       `json:"inboxPending"`
	InboxRetrying              int       `json:"inboxRetrying"`
	InboxOldestWaitSeconds     float64   `json:"inboxOldestWaitSeconds"`
	InboxProcessedLastHour     int       `json:"inboxProcessedLastHour"`
	InboxP95ProcessSeconds     float64   `json:"inboxP95ProcessSeconds"`
	AdmissionWaiting           int       `json:"admissionWaiting"`
	AdmissionOldestWaitSeconds float64   `json:"admissionOldestWaitSeconds"`
	CommandPending             int       `json:"commandPending"`
	CommandOldestWaitSeconds   float64   `json:"commandOldestWaitSeconds"`
	RunnerSlotsReserved        int       `json:"runnerSlotsReserved"`
	MeasuredAt                 time.Time `json:"measuredAt"`
}

// BacklogSnapshot keeps periodic metrics scrapes cheap. OperationsSnapshot
// additionally computes a one-hour percentile for interactive inspection.
type BacklogSnapshot struct {
	LegacyInboxPending         int
	AdmissionWaiting           int
	AdmissionOldestWaitSeconds float64
	CommandPending             int
	CommandOldestWaitSeconds   float64
	RunnerSlotsReserved        int
}

func (s *Store) BacklogSnapshot(ctx context.Context) (BacklogSnapshot, error) {
	var out BacklogSnapshot
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_inbox WHERE processed_at IS NULL`).Scan(&out.LegacyInboxPending)
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(EXTRACT(EPOCH FROM now()-min(COALESCE((body->>'createdAt')::timestamptz,updated_at))),0)
		FROM tasks WHERE stage='queued' AND body->>'admissionStatus'='waiting'`).Scan(&out.AdmissionWaiting, &out.AdmissionOldestWaitSeconds)
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(EXTRACT(EPOCH FROM now()-min(created_at)),0)
		FROM commands WHERE NOT delivered`).Scan(&out.CommandPending, &out.CommandOldestWaitSeconds)
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM leases WHERE expires_at>now() AND name LIKE 'agent-slot:%'`).Scan(&out.RunnerSlotsReserved)
	return out, err
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
	err = s.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(EXTRACT(EPOCH FROM now()-min(COALESCE((body->>'createdAt')::timestamptz,updated_at))),0)
		FROM tasks WHERE stage='queued' AND body->>'admissionStatus'='waiting'`).Scan(&out.AdmissionWaiting, &out.AdmissionOldestWaitSeconds)
	if err != nil {
		return out, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(EXTRACT(EPOCH FROM now()-min(created_at)),0)
		FROM commands WHERE NOT delivered`).Scan(&out.CommandPending, &out.CommandOldestWaitSeconds)
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
