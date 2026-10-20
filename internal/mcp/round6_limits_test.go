package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestMethodologyCannotRelaxHiveLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		config domain.HiveConfig
		limits domain.Limits
		want error
	}{
		{"hive concurrency", domain.HiveConfig{MaxConcurrentForagers: 1}, domain.Limits{MaxConcurrentAgents: 4}, domain.ErrHiveLimit},
		{"methodology concurrency", domain.HiveConfig{MaxConcurrentForagers: 4}, domain.Limits{MaxConcurrentAgents: 1}, domain.ErrHiveLimit},
		{"hive chain", domain.HiveConfig{MaxForagersPerChain: 1}, domain.Limits{MaxAgentsPerChain: 4}, domain.ErrChainLimit},
		{"methodology chain", domain.HiveConfig{MaxForagersPerChain: 4}, domain.Limits{MaxAgentsPerChain: 1}, domain.ErrChainLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := newHive(t, tc.config)
			ctx := context.Background()
			if _, err := s.CreateForagerWithLimits(ctx, domain.Agent{HiveID: h.ID, ChainID: "shared"}, tc.limits); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateForagerWithLimits(ctx, domain.Agent{HiveID: h.ID, ChainID: "shared"}, tc.limits); !errors.Is(err, tc.want) {
				t.Fatalf("second spawn = %v, want %v", err, tc.want)
			}
		})
	}
}
