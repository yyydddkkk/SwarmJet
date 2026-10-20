package store

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestCreateForagerPersistsRoleRevisionAndParent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/round4-agent")

	parent, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: "chain-parent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateForager(ctx, domain.Agent{
		HiveID:   h.ID,
		ChainID:  "chain-parent",
		RoleName: "verifier",
		Revision: "0123456789abcdef",
		ParentID: parent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RoleName != "verifier" || got.Revision != "0123456789abcdef" || got.ParentID != parent.ID {
		t.Fatalf("round-4 agent fields not persisted: %+v", got)
	}
	if got.ChainID != "chain-parent" {
		t.Fatalf("chain = %q, want inherited chain-parent", got.ChainID)
	}
}

func TestCreateForagerWithMethodologyLimits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	t.Run("concurrency", func(t *testing.T) {
		h := newTestHive(t, s, "acme/limits-concurrency")
		if _, err := s.CreateForagerWithLimits(ctx, domain.Agent{HiveID: h.ID, ChainID: "a"}, domain.Limits{MaxConcurrentAgents: 1}); err != nil {
			t.Fatal(err)
		}
		_, err := s.CreateForagerWithLimits(ctx, domain.Agent{HiveID: h.ID, ChainID: "b"}, domain.Limits{MaxConcurrentAgents: 1})
		if !errors.Is(err, domain.ErrHiveLimit) {
			t.Fatalf("err = %v, want ErrHiveLimit", err)
		}
	})

	t.Run("chain", func(t *testing.T) {
		h := newTestHive(t, s, "acme/limits-chain")
		limits := domain.Limits{MaxConcurrentAgents: 10, MaxAgentsPerChain: 1}
		if _, err := s.CreateForagerWithLimits(ctx, domain.Agent{HiveID: h.ID, ChainID: "chain-1"}, limits); err != nil {
			t.Fatal(err)
		}
		_, err := s.CreateForagerWithLimits(ctx, domain.Agent{HiveID: h.ID, ChainID: "chain-1"}, limits)
		if !errors.Is(err, domain.ErrChainLimit) {
			t.Fatalf("err = %v, want ErrChainLimit", err)
		}
	})

	t.Run("zero limits keep hive config", func(t *testing.T) {
		h := newTestHive(t, s, "acme/limits-default")
		if _, err := s.CreateForagerWithLimits(ctx, domain.Agent{HiveID: h.ID, ChainID: "c1"}, domain.Limits{}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSendMessageStoresTrust(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/trust")

	cases := []struct {
		name  string
		from  domain.Address
		trust domain.MessageTrust
	}{
		{"user", domain.LocalAddress("token-1"), domain.TrustUser},
		{"agent", domain.ForagerAddress("agent-1"), domain.TrustAgent},
		{"system", domain.AddressSystem, domain.TrustSystem},
		{"external explicit", domain.AddressSystem, domain.TrustExternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := domain.Message{HiveID: h.ID, From: tc.from, To: domain.AddressHive, Kind: domain.MessageEvent, Body: tc.name}
			if tc.trust == domain.TrustExternal {
				msg.Trust = tc.trust
			}
			sent, err := s.SendMessage(ctx, msg)
			if err != nil {
				t.Fatal(err)
			}
			if sent.Trust != tc.trust {
				t.Fatalf("returned trust = %q, want %q", sent.Trust, tc.trust)
			}
			inbox, err := s.Inbox(ctx, h.ID, domain.AddressHive, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, got := range inbox {
				if got.ID == sent.ID {
					found = true
					if got.Trust != tc.trust {
						t.Fatalf("stored trust = %q, want %q", got.Trust, tc.trust)
					}
				}
			}
			if !found {
				t.Fatalf("message %s not found in Inbox", sent.ID)
			}
		})
	}

	t.Run("explicit trust is part of idempotency", func(t *testing.T) {
		base := domain.Message{ID: "trust-conflict", HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive, Kind: domain.MessageEvent, Body: "x", Trust: domain.TrustExternal}
		if _, err := s.SendMessage(ctx, base); err != nil {
			t.Fatal(err)
		}
		changed := base
		changed.Trust = domain.TrustSystem
		if _, err := s.SendMessage(ctx, changed); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
	})
}
