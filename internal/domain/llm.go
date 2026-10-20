package domain

import "time"

// LLMUsage is one response's model usage, attributed to the Agent whose
// platform token authorized the request. CostUSD is estimated from the built-in
// price table; UnknownPrice is true when the model has no entry, in which case
// CostUSD is zero and the row is still recorded.
type LLMUsage struct {
	ID               int64     `json:"id,omitempty"`
	AgentID          string    `json:"agentId"`
	HiveID           string    `json:"hiveId"`
	Model            string    `json:"model"`
	InputTokens      int64     `json:"inputTokens"`
	OutputTokens     int64     `json:"outputTokens"`
	CacheReadTokens  int64     `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64     `json:"cacheWriteTokens,omitempty"`
	CostUSD          float64   `json:"costUsd"`
	UnknownPrice     bool      `json:"unknownPrice,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
}

// LLMUsageTotals is the aggregate of a set of LLMUsage rows.
type LLMUsageTotals struct {
	Requests             int64   `json:"requests"`
	InputTokens          int64   `json:"inputTokens"`
	OutputTokens         int64   `json:"outputTokens"`
	CacheReadTokens      int64   `json:"cacheReadTokens"`
	CacheWriteTokens     int64   `json:"cacheWriteTokens"`
	CostUSD              float64 `json:"costUsd"`
	UnknownPriceRequests int64   `json:"unknownPriceRequests"`
}

// LLMUsageGroup is one dimension of the usage breakdown.
type LLMUsageGroup struct {
	AgentID string `json:"agentId,omitempty"`
	Model   string `json:"model,omitempty"`
	LLMUsageTotals
}

// LLMUsageSummary is the console's usage view. HiveID and AgentID echo the
// filters; an empty filter means "all".
type LLMUsageSummary struct {
	HiveID  string `json:"hiveId,omitempty"`
	AgentID string `json:"agentId,omitempty"`
	// Period is "all" or "today" (since local midnight on the database clock).
	Period  string          `json:"period"`
	Totals  LLMUsageTotals  `json:"totals"`
	ByAgent []LLMUsageGroup `json:"byAgent"`
	ByModel []LLMUsageGroup `json:"byModel"`
}
