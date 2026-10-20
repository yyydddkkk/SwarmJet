package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type spawnQueueDrainKey struct{}

// EnqueueSpawn persists a request that CreateForager rejected solely because
// its Hive has no free slot. agent.ID identifies the request across retries.
func (s *Store) EnqueueSpawn(ctx context.Context, agent domain.Agent, message domain.Message, limits domain.Limits) (string, int, error) {
	if agent.ID == "" {
		id, err := newOpaqueID()
		if err != nil {
			return "", 0, err
		}
		agent.ID = id
	}
	if message.ID == "" {
		message.ID = "queued-task-" + agent.ID
	}
	message.HiveID = agent.HiveID
	message.To = domain.ForagerAddress(agent.ID)
	if message.ChainID == "" {
		message.ChainID = agent.ChainID
	}
	agentJSON, err := json.Marshal(agent)
	if err != nil {
		return "", 0, err
	}
	messageJSON, err := json.Marshal(message)
	if err != nil {
		return "", 0, err
	}
	limitsJSON, err := json.Marshal(limits)
	if err != nil {
		return "", 0, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback(ctx)
	var hiveLock string
	if err = tx.QueryRow(ctx, `SELECT id FROM hives WHERE id=$1 FOR UPDATE`, agent.HiveID).Scan(&hiveLock); err != nil {
		return "", 0, domainErr(err)
	}
	var alreadyCreated bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1)`, agent.ID).Scan(&alreadyCreated); err != nil {
		return "", 0, err
	}
	if alreadyCreated {
		return "", 0, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO spawn_queue(hive_id,agent_id,agent,task_message,limits)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(agent_id) DO NOTHING`, agent.HiveID, agent.ID, agentJSON, messageJSON, limitsJSON); err != nil {
		return "", 0, err
	}
	var seq int64
	var state string
	var storedAgent, storedMessage []byte
	if err = tx.QueryRow(ctx, `SELECT seq,state,agent,task_message FROM spawn_queue WHERE agent_id=$1`, agent.ID).
		Scan(&seq, &state, &storedAgent, &storedMessage); err != nil {
		return "", 0, err
	}
	if agent.Revision != "" {
		var previous domain.Agent
		if err = json.Unmarshal(storedAgent, &previous); err != nil {
			return "", 0, err
		}
		if previous.HiveID != agent.HiveID || previous.Revision != agent.Revision || previous.ParentID != agent.ParentID || previous.RoleName != agent.RoleName {
			return "", 0, domain.ErrConflict
		}
	} else if !equalJSON(storedAgent, agentJSON) || !equalJSON(storedMessage, messageJSON) {
		return "", 0, domain.ErrConflict
	}
	var position int
	if state == "waiting" {
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM spawn_queue WHERE hive_id=$1 AND state='waiting' AND seq<=$2`, agent.HiveID, seq).Scan(&position); err != nil {
			return "", 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", 0, err
	}
	return agent.ID, position, nil
}

func equalJSON(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// DrainSpawnQueue processes requests in FIFO order for one Hive. A session
// advisory lock serializes scanners across processes without blocking enqueue.
func (s *Store) DrainSpawnQueue(ctx context.Context, hiveID string) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(1205, hashtext($1))`, hiveID).Scan(&locked); err != nil || !locked {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(1205, hashtext($1))`, hiveID)
	for {
		var seq int64
		var agentJSON, messageJSON, limitsJSON []byte
		var queuedAt time.Time
		err = conn.QueryRow(ctx, `SELECT seq,agent,task_message,limits,created_at FROM spawn_queue
			WHERE hive_id=$1 AND state='waiting' ORDER BY seq LIMIT 1`, hiveID).
			Scan(&seq, &agentJSON, &messageJSON, &limitsJSON, &queuedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var agent domain.Agent
		var message domain.Message
		var limits domain.Limits
		if err = json.Unmarshal(agentJSON, &agent); err != nil {
			return err
		}
		if err = json.Unmarshal(messageJSON, &message); err != nil {
			return err
		}
		if err = json.Unmarshal(limitsJSON, &limits); err != nil {
			return err
		}
		existing, lookupErr := s.Agent(ctx, agent.ID)
		if lookupErr == nil && existing.HiveID != hiveID {
			return domain.ErrConflict
		}
		if lookupErr != nil && !errors.Is(lookupErr, domain.ErrNotFound) {
			return lookupErr
		}
		if errors.Is(lookupErr, domain.ErrNotFound) && time.Since(queuedAt) >= 24*time.Hour {
			if err = s.abandonSpawn(ctx, seq, agent, "等待并发名额超过 24 小时，派生请求已放弃"); err != nil {
				return err
			}
			continue
		}
		err = lookupErr
		if errors.Is(err, domain.ErrNotFound) {
			_, err = s.CreateForagerWithLimits(context.WithValue(ctx, spawnQueueDrainKey{}, true), agent, limits)
		}
		if errors.Is(err, domain.ErrHiveLimit) {
			return nil
		}
		if errors.Is(err, domain.ErrChainLimit) {
			if err = s.abandonSpawn(ctx, seq, agent, "派生请求超过链内 Agent 上限，已放弃"); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, domain.ErrConflict) {
			existing, err = s.Agent(ctx, agent.ID)
			if err == nil && existing.HiveID != hiveID {
				return domain.ErrConflict
			}
		}
		if err != nil {
			return err
		}
		if _, err = s.SendMessage(ctx, message); err != nil {
			return err
		}
		if _, err = conn.Exec(ctx, `UPDATE spawn_queue SET state='started',finished_at=now() WHERE seq=$1 AND state='waiting'`, seq); err != nil {
			return err
		}
	}
}

func (s *Store) abandonSpawn(ctx context.Context, seq int64, agent domain.Agent, reason string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var queenEnabled bool
	if err = tx.QueryRow(ctx, `SELECT queen_enabled FROM hives WHERE id=$1 FOR UPDATE`, agent.HiveID).Scan(&queenEnabled); err != nil {
		return domainErr(err)
	}
	to := domain.AddressHive
	if queenEnabled {
		to = domain.AddressQueen
	}
	body := reason + ": " + agent.Task
	if runes := []rune(body); len(runes) > domain.MaxMessageBody {
		body = string(runes[:domain.MaxMessageBody])
	}
	if _, err = s.sendMessageTx(ctx, tx, domain.Message{
		ID: "spawn-expired-" + agent.ID, HiveID: agent.HiveID,
		From: domain.AddressSystem, To: to, Kind: domain.MessageEvent,
		Body: body, Trust: domain.TrustSystem,
	}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE spawn_queue SET state='expired',finished_at=now() WHERE seq=$1 AND state='waiting'`, seq); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
