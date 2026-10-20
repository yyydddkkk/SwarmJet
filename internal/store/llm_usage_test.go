package store

import (
	"context"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestLLMUsageCostsAndSummary(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []domain.LLMUsage{
		{AgentID: "agent-1", HiveID: "hive-1", Model: "deepseek/deepseek-chat", InputTokens: 1000, OutputTokens: 500, CostUSD: 1.25, CreatedAt: now},
		{AgentID: "agent-1", HiveID: "hive-1", Model: "deepseek/deepseek-chat", InputTokens: 2000, OutputTokens: 1000, CostUSD: 0.25, CreatedAt: now},
		{AgentID: "agent-2", HiveID: "hive-2", Model: "vendor/unknown", InputTokens: 10, OutputTokens: 5, UnknownPrice: true, CreatedAt: now},
		{AgentID: "agent-1", HiveID: "hive-1", Model: "deepseek/deepseek-chat", InputTokens: 7, OutputTokens: 3, CostUSD: 2, CreatedAt: now.Add(-48 * time.Hour)},
	}
	for _, row := range rows {
		if err := s.RecordLLMUsage(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	agentCost, err := s.AgentCost(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if agentCost != 3.5 {
		t.Fatalf("agent cost = %v", agentCost)
	}
	hiveToday, err := s.HiveCostToday(ctx, "hive-1")
	if err != nil {
		t.Fatal(err)
	}
	if hiveToday != 1.5 {
		t.Fatalf("hive cost today = %v", hiveToday)
	}

	summary, err := s.LLMUsageSummary(ctx, "hive-1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Requests != 3 || summary.Totals.CostUSD != 3.5 ||
		summary.Totals.InputTokens != 3007 || summary.Totals.OutputTokens != 1503 {
		t.Fatalf("totals = %+v", summary.Totals)
	}
	if len(summary.ByAgent) != 1 || summary.ByAgent[0].AgentID != "agent-1" {
		t.Fatalf("by agent = %+v", summary.ByAgent)
	}
	if len(summary.ByModel) != 1 || summary.ByModel[0].Model != "deepseek/deepseek-chat" {
		t.Fatalf("by model = %+v", summary.ByModel)
	}

	today, err := s.LLMUsageSummary(ctx, "hive-1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if today.Period != "today" || today.Totals.Requests != 2 || today.Totals.CostUSD != 1.5 {
		t.Fatalf("today = %+v", today)
	}

	agentSummary, err := s.LLMUsageSummary(ctx, "", "agent-2", false)
	if err != nil {
		t.Fatal(err)
	}
	if agentSummary.Totals.UnknownPriceRequests != 1 || agentSummary.Totals.CostUSD != 0 {
		t.Fatalf("unknown-price totals = %+v", agentSummary.Totals)
	}
}

func TestMigrateCreatesLLMUsageTable(t *testing.T) {
	s := testStore(t)
	var version int
	if err := s.Pool.QueryRow(context.Background(), `SELECT max(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 11 {
		t.Fatalf("schema version = %d", version)
	}
	var exists bool
	if err := s.Pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='llm_usage')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("llm_usage table was not created")
	}
}
