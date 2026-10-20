package store

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// SpawnQueueEntry is the shared console/SSE representation of a spawn request.
type SpawnQueueEntry struct {
	AgentID   string    `json:"agentId"`
	Role      string    `json:"role"`
	Task      string    `json:"task"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Store) SpawnQueue(ctx context.Context, hiveID string) ([]SpawnQueueEntry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT agent_id,agent,created_at FROM spawn_queue WHERE hive_id=$1 AND state='waiting' ORDER BY seq`, hiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpawnQueueEntry{}
	for rows.Next() {
		var entry SpawnQueueEntry
		var data []byte
		var agent domain.Agent
		if err := rows.Scan(&entry.AgentID, &data, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &agent); err != nil {
			return nil, err
		}
		entry.Role = agent.RoleName
		if entry.Role == "" {
			entry.Role = string(agent.Role)
		}
		task := []rune(agent.Task)
		if len(task) > 200 {
			task = task[:200]
		}
		entry.Task = string(task)
		entry.Position = len(out) + 1
		entry.CreatedAt = entry.CreatedAt.UTC()
		entry.ExpiresAt = entry.CreatedAt.Add(24 * time.Hour)
		out = append(out, entry)
	}
	return out, rows.Err()
}

// MethodologyEvent is deliberately also used for the SSE methodology payload.
type MethodologyEvent struct {
	Version   int            `json:"version"`
	Action    string         `json:"action"`
	Actor     domain.Address `json:"actor"`
	Note      string         `json:"note"`
	CreatedAt time.Time      `json:"createdAt"`
}

func (s *Store) MethodologyEvents(ctx context.Context, hiveID string, limit int) ([]MethodologyEvent, error) {
	rows, err := s.Pool.Query(ctx, `SELECT version,action,actor,note,created_at FROM methodology_events WHERE hive_id=$1 ORDER BY id DESC LIMIT $2`, hiveID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MethodologyEvent{}
	for rows.Next() {
		var event MethodologyEvent
		if err := rows.Scan(&event.Version, &event.Action, &event.Actor, &event.Note, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.CreatedAt = event.CreatedAt.UTC()
		out = append(out, event)
	}
	return out, rows.Err()
}

// AgentWithCost preserves Agent's existing fields and adds its lifetime spend.
type AgentWithCost struct {
	domain.Agent
	CostUSD float64 `json:"costUsd"`
}

// AgentsWithCosts uses one aggregate query for the entire list, including Queen.
func (s *Store) AgentsWithCosts(ctx context.Context, agents []domain.Agent) ([]AgentWithCost, error) {
	out := make([]AgentWithCost, len(agents))
	ids := make([]string, len(agents))
	for i, agent := range agents {
		out[i].Agent = agent
		ids[i] = agent.ID
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT agent_id,COALESCE(sum(cost_usd),0)::float8 FROM llm_usage WHERE agent_id=ANY($1) GROUP BY agent_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	costs := make(map[string]float64)
	for rows.Next() {
		var id string
		var cost float64
		if err := rows.Scan(&id, &cost); err != nil {
			return nil, err
		}
		costs[id] = cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].CostUSD = costs[out[i].ID]
	}
	return out, nil
}

type Signal struct {
	MessageID string    `json:"messageId"`
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
	Number    int64     `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Claimed   bool      `json:"claimed"`
	ClaimedBy string    `json:"claimedBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// SignalClaims independently resolves claims in one query. A handled message is
// claimed by its recipient; otherwise an Agent linked to the matching Issue/PR
// claims it. Agent state is irrelevant (historical work remains claimed).
func (s *Store) SignalClaims(ctx context.Context, hiveID string, signals []Signal) (map[string]string, error) {
	ids := make([]string, len(signals))
	numbers := make([]int64, len(signals))
	kinds := make([]string, len(signals))
	for i, signal := range signals {
		ids[i] = signal.MessageID
		numbers[i] = signal.Number
		kinds[i] = signal.Kind
	}
	rows, err := s.Pool.Query(ctx, `SELECT input.id,CASE WHEN m.state='handled' THEN CASE WHEN m.to_addr LIKE 'forager:%' THEN substring(m.to_addr FROM 9) ELSE m.to_addr END ELSE COALESCE(owner.id,'') END
 FROM unnest($2::text[],$3::bigint[],$4::text[]) AS input(id,number,kind)
 JOIN messages m ON m.id=input.id AND m.hive_id=$1
 LEFT JOIN LATERAL (SELECT a.id FROM agents a WHERE a.hive_id=$1 AND input.number>0 AND
 ((input.kind='issue' AND a.issue_number=input.number) OR (input.kind IN ('pull_request','check') AND a.pr_number=input.number))
 ORDER BY a.created_at,a.id LIMIT 1) owner ON true`, hiveID, ids, numbers, kinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, err
		}
		out[id] = owner
	}
	return out, rows.Err()
}

func (s *Store) Signals(ctx context.Context, hiveID string, since time.Time) ([]Signal, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,body,created_at FROM messages WHERE hive_id=$1 AND trust='external' AND created_at >= $2 ORDER BY created_at DESC,seq DESC`, hiveID, since)
	if err != nil {
		return nil, err
	}
	out := []Signal{}
	for rows.Next() {
		var signal Signal
		var body string
		if err := rows.Scan(&signal.MessageID, &body, &signal.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		signal.Source = "github"
		signal.CreatedAt = signal.CreatedAt.UTC()
		// Digest's fixed header ends at the first blank line. Never interpret an
		// external excerpt's forged Number/Title/Link lines as structured metadata.
		// Inbox fallbacks prepend a routing reason before the digest.
		if _, digest, found := strings.Cut(body, "GitHub event: "); found {
			body = "GitHub event: " + digest
		}
		header, _, _ := strings.Cut(body, "\n\n")
		seen := map[string]bool{}
		eventType := ""
		for _, line := range strings.Split(header, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if !ok || (seen[key] && key != "Link") {
				continue
			}
			seen[key] = true
			switch key {
			case "GitHub event":
				event, _, _ := strings.Cut(value, ".")
				eventType = event
				switch event {
				case "issues", "issue_comment":
					signal.Kind = "issue"
				case "pull_request", "pull_request_review", "pull_request_review_comment":
					signal.Kind = "pull_request"
				case "check_run", "check_suite", "status":
					signal.Kind = "check"
				default:
					signal.Kind = event
				}
			case "Number":
				signal.Number, _ = strconv.ParseInt(strings.TrimPrefix(value, "#"), 10, 64)
			case "Title":
				signal.Title = value
			case "Link":
				signal.URL = value
			}
		}
		// GitHub sends PR conversation comments as issue_comment too. Digest's
		// canonical comment link retains /pull/N, unlike Issue comments.
		if eventType == "issue_comment" {
			if link, err := url.Parse(signal.URL); err == nil {
				parts := strings.Split(strings.Trim(link.Path, "/"), "/")
				if len(parts) >= 4 && parts[2] == "pull" && parts[3] == strconv.FormatInt(signal.Number, 10) {
					signal.Kind = "pull_request"
				}
			}
		}
		out = append(out, signal)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	claims, err := s.SignalClaims(ctx, hiveID, out)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].ClaimedBy = claims[out[i].MessageID]
		out[i].Claimed = out[i].ClaimedBy != ""
	}
	return out, nil
}
