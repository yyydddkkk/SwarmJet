package llmproxy

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestExtractUsageWireFamilies(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		input  int64
		output int64
		cache  int64
	}{
		{
			name:   "openai chat",
			body:   `{"model":"deepseek/deepseek-chat","usage":{"prompt_tokens":11,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":3}}}`,
			input:  11,
			output: 7,
			cache:  3,
		},
		{
			name: "anthropic stream merges start and delta",
			body: "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude\",\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":4}}}\n\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n",
			input:  12,
			output: 9,
			cache:  4,
		},
		{
			name:   "openai responses",
			body:   `{"type":"response.completed","response":{"model":"gpt","usage":{"input_tokens":20,"output_tokens":8,"input_tokens_details":{"cached_tokens":2}}}}`,
			input:  20,
			output: 8,
			cache:  2,
		},
		{
			name:   "pi native done",
			body:   `data: {"type":"done","message":{"model":"deepseek/deepseek-chat","usage":{"input":100,"output":20,"cacheRead":5,"cacheWrite":1}}}`,
			input:  100,
			output: 20,
			cache:  5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			usage, ok := ExtractUsage([]byte(tc.body))
			if !ok {
				t.Fatalf("usage not found in %s", tc.body)
			}
			if usage.Input != tc.input || usage.Output != tc.output || usage.CacheRead != tc.cache {
				t.Fatalf("usage = %+v", usage)
			}
		})
	}
}

func TestExtractUsageIgnoresErrorBodies(t *testing.T) {
	if _, ok := ExtractUsage([]byte(`{"error":{"message":"upstream failed"}}`)); ok {
		t.Fatal("error body produced usage")
	}
}

func TestEstimateCostUsesPriceTable(t *testing.T) {
	usage := TokenUsage{Input: 1_000_000, Output: 1_000_000}
	cost, known := EstimateCost("deepseek/deepseek-chat", usage)
	if !known || cost != 0.7 {
		t.Fatalf("cost = %v known = %v", cost, known)
	}
	if _, known = EstimateCost("vendor/unknown", usage); known {
		t.Fatal("unknown model was priced")
	}
}

func TestBudgetLimitsFromMethodology(t *testing.T) {
	reader := fakeMethodology{methodology: domain.Methodology{Manifest: domain.MethodologyManifest{
		Limits: domain.Limits{MaxCostPerAgentUSD: 5, MaxCostPerDayUSD: 10},
	}}}
	limits, err := (StoreLimits{Reader: reader}).Limits(t.Context(), "hive-1")
	if err != nil {
		t.Fatal(err)
	}
	if limits.MaxCostPerAgentUSD != 5 || limits.MaxCostPerDayUSD != 10 {
		t.Fatalf("limits = %+v", limits)
	}
}

type fakeMethodology struct {
	methodology domain.Methodology
	err         error
}

func (f fakeMethodology) ActiveMethodology(context.Context, string) (domain.Methodology, []domain.MethodologyFile, error) {
	return f.methodology, nil, f.err
}
