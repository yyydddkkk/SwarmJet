package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// applySchemaUpgrade11 installs the model-usage ledger. Migration numbers 8-10
// belong to the other round-4 tasks; this function is intentionally independent
// of them so task B can run alone, while the reviewer keeps the numeric order.
func applySchemaUpgrade11(ctx context.Context, tx pgx.Tx, version int) error {
	if version >= 11 {
		return nil
	}
	if _, err := tx.Exec(ctx, schemaUpgrade11); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO schema_version(version) VALUES(11) ON CONFLICT DO NOTHING`)
	return err
}

// RecordLLMUsage appends one completed model request. Usage rows are only ever
// appended; the console aggregates them.
func (s *Store) RecordLLMUsage(ctx context.Context, usage domain.LLMUsage) error {
	if usage.CreatedAt.IsZero() {
		usage.CreatedAt = time.Now().UTC()
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO llm_usage(
		agent_id,hive_id,model,input_tokens,output_tokens,cache_read_tokens,
		cache_write_tokens,cost_usd,unknown_price,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		usage.AgentID, usage.HiveID, usage.Model, usage.InputTokens, usage.OutputTokens,
		usage.CacheReadTokens, usage.CacheWriteTokens, usage.CostUSD,
		usage.UnknownPrice, usage.CreatedAt)
	return err
}

// AgentCost is the Agent's cumulative spend in USD. Agents are short-lived, so
// this is also the amount the per-Agent methodology limit is checked against.
func (s *Store) AgentCost(ctx context.Context, agentID string) (float64, error) {
	var cost float64
	err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(sum(cost_usd),0)::float8 FROM llm_usage WHERE agent_id=$1`,
		agentID).Scan(&cost)
	return cost, err
}

// HiveCostToday is the Hive's spend since the start of the database's current
// day. It backs the per-day methodology limit.
func (s *Store) HiveCostToday(ctx context.Context, hiveID string) (float64, error) {
	var cost float64
	err := s.Pool.QueryRow(ctx,
		`SELECT COALESCE(sum(cost_usd),0)::float8 FROM llm_usage
		 WHERE hive_id=$1 AND created_at >= date_trunc('day', now())`,
		hiveID).Scan(&cost)
	return cost, err
}

// LLMUsageSummary aggregates the ledger. Empty hiveID/agentID mean "all".
func (s *Store) LLMUsageSummary(ctx context.Context, hiveID, agentID string) (domain.LLMUsageSummary, error) {
	summary := domain.LLMUsageSummary{
		HiveID:  hiveID,
		AgentID: agentID,
		ByAgent: []domain.LLMUsageGroup{},
		ByModel: []domain.LLMUsageGroup{},
	}
	const filter = `WHERE ($1::text = '' OR hive_id = $1::text)
		AND ($2::text = '' OR agent_id = $2::text)`
	totals := `count(*), COALESCE(sum(input_tokens),0), COALESCE(sum(output_tokens),0),
		COALESCE(sum(cache_read_tokens),0), COALESCE(sum(cache_write_tokens),0),
		COALESCE(sum(cost_usd),0)::float8,
		count(*) FILTER (WHERE unknown_price)`
	if err := s.Pool.QueryRow(ctx, `SELECT `+totals+` FROM llm_usage `+filter, hiveID, agentID).
		Scan(&summary.Totals.Requests, &summary.Totals.InputTokens, &summary.Totals.OutputTokens,
			&summary.Totals.CacheReadTokens, &summary.Totals.CacheWriteTokens,
			&summary.Totals.CostUSD, &summary.Totals.UnknownPriceRequests); err != nil {
		return domain.LLMUsageSummary{}, err
	}
	byAgent, err := s.llmUsageGroups(ctx,
		`SELECT agent_id, `+totals+` FROM llm_usage `+filter+` GROUP BY agent_id ORDER BY agent_id`, hiveID, agentID)
	if err != nil {
		return domain.LLMUsageSummary{}, err
	}
	byModel, err := s.llmUsageGroups(ctx,
		`SELECT model, `+totals+` FROM llm_usage `+filter+` GROUP BY model ORDER BY model`, hiveID, agentID)
	if err != nil {
		return domain.LLMUsageSummary{}, err
	}
	for i := range byModel {
		byModel[i].Model, byModel[i].AgentID = byModel[i].AgentID, ""
	}
	summary.ByAgent = byAgent
	summary.ByModel = byModel
	return summary, nil
}

func (s *Store) llmUsageGroups(ctx context.Context, query, hiveID, agentID string) ([]domain.LLMUsageGroup, error) {
	rows, err := s.Pool.Query(ctx, query, hiveID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []domain.LLMUsageGroup{}
	for rows.Next() {
		var group domain.LLMUsageGroup
		// The key column is scanned into either AgentID or Model; the caller
		// moves it to the right JSON field.
		if err = rows.Scan(&group.AgentID, &group.Requests, &group.InputTokens, &group.OutputTokens,
			&group.CacheReadTokens, &group.CacheWriteTokens, &group.CostUSD,
			&group.UnknownPriceRequests); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}
