package llmproxy

import (
	_ "embed"
	"encoding/json"
	"math"
	"strings"
	"sync"
)

//go:embed pricing.json
var pricingJSON []byte

// Price is a provider's published rate in USD per 1M tokens.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type priceTable struct {
	Models map[string]Price `json:"models"`
}

var (
	priceOnce sync.Once
	prices    map[string]Price
)

func loadPrices() map[string]Price {
	priceOnce.Do(func() {
		prices = map[string]Price{}
		var table priceTable
		if err := json.Unmarshal(pricingJSON, &table); err != nil {
			return
		}
		for model, price := range table.Models {
			prices[strings.ToLower(strings.TrimSpace(model))] = price
		}
	})
	return prices
}

// PriceFor resolves a model id against the embedded table. Gateway model ids
// are provider-qualified ("deepseek/deepseek-chat"); a bare id is accepted when
// exactly one table entry has that suffix.
func PriceFor(model string) (Price, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return Price{}, false
	}
	table := loadPrices()
	if price, ok := table[model]; ok {
		return price, true
	}
	if strings.Contains(model, "/") {
		return Price{}, false
	}
	var (
		found Price
		count int
	)
	for id, price := range table {
		if strings.HasSuffix(id, "/"+model) {
			found, count = price, count+1
		}
	}
	return found, count == 1
}

// EstimateCost prices one usage record. Unknown models return (0, false) so the
// caller can record the tokens and mark the row.
func EstimateCost(model string, usage TokenUsage) (float64, bool) {
	price, ok := PriceFor(model)
	if !ok {
		return 0, false
	}
	cost := float64(usage.Input)/1e6*price.Input +
		float64(usage.Output)/1e6*price.Output +
		float64(usage.CacheRead)/1e6*price.CacheRead +
		float64(usage.CacheWrite)/1e6*price.CacheWrite
	return math.Round(cost*1e6) / 1e6, true
}
