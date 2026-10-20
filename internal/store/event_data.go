package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// EventData reads committed entities for the API's shared fanout.
func (s *Store) EventData(ctx context.Context, ref EventRef) (any, error) {
	switch ref.Type {
	case "hive":
		if ref.ID != ref.HiveID {
			return nil, domain.ErrNotFound
		}
		hive, err := s.Hive(ctx, ref.HiveID)
		hive.CreatedAt = hive.CreatedAt.UTC()
		hive.UpdatedAt = hive.UpdatedAt.UTC()
		return hive, err
	case "agent":
		agent, err := s.Agent(ctx, ref.ID)
		if err == nil && agent.HiveID != ref.HiveID {
			return nil, domain.ErrNotFound
		}
		agent.CreatedAt = agent.CreatedAt.UTC()
		agent.UpdatedAt = agent.UpdatedAt.UTC()
		return agent, err
	case "message":
		m, err := scanMessage(s.Pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE hive_id=$1 AND id=$2`, ref.HiveID, ref.ID))
		if err != nil {
			return nil, err
		}
		preview := []rune(m.Body)
		if len(preview) > 120 {
			preview = preview[:120]
		}
		return map[string]any{"id": m.ID, "hiveId": m.HiveID, "from": m.From, "to": m.To, "kind": m.Kind, "trust": m.Trust, "chainId": m.ChainID, "createdAt": m.CreatedAt.UTC(), "preview": string(preview)}, nil
	case "methodology":
		var version int
		var action, actor, note string
		var createdAt time.Time
		err := s.Pool.QueryRow(ctx, `SELECT version,action,actor,note,created_at FROM methodology_events WHERE hive_id=$1 AND id=$2::bigint`, ref.HiveID, ref.ID).Scan(&version, &action, &actor, &note, &createdAt)
		return map[string]any{"version": version, "action": action, "actor": actor, "note": note, "createdAt": createdAt.UTC()}, domainErr(err)
	case "queue":
		var agentID, state string
		var agentJSON []byte
		var createdAt time.Time
		var position int
		err := s.Pool.QueryRow(ctx, `SELECT q.agent_id,q.agent,q.created_at,q.state,CASE WHEN q.state='waiting' THEN (SELECT count(*) FROM spawn_queue w WHERE w.hive_id=q.hive_id AND w.state='waiting' AND w.seq<=q.seq) ELSE 0 END FROM spawn_queue q WHERE hive_id=$1 AND agent_id=$2`, ref.HiveID, ref.ID).Scan(&agentID, &agentJSON, &createdAt, &state, &position)
		if err != nil {
			return nil, domainErr(err)
		}
		var agent domain.Agent
		if err := json.Unmarshal(agentJSON, &agent); err != nil {
			return nil, err
		}
		return struct {
			SpawnQueueEntry
			State string `json:"state"`
		}{spawnQueueEntry(agentID, agent, createdAt, position), state}, nil
	}
	return nil, domain.ErrNotFound
}
