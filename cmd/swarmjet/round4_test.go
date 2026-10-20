package main

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/router"
)

// TestPlatformHeadChangeSupersedesOldAttestations exercises the production
// wiring: the Router's OnHead hook calls the same store method the server
// installs, and no Agent is spawned or woken.
func TestPlatformHeadChangeSupersedesOldAttestations(t *testing.T) {
	s := openRound2Store(t)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "alpha", Repository: "acme/hive"})
	if err != nil {
		t.Fatal(err)
	}
	forager, err := s.CreateForager(ctx, domain.Agent{
		HiveID: hive.ID, Role: domain.RoleForager, State: domain.AgentActive,
		PRNumber: 34, Branch: "swarm/acme-hive/forager-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.RecordAttestation(ctx, domain.Attestation{
		HiveID: hive.ID, PRNumber: 34, HeadSHA: "old-head",
		AttesterID: "verifier-1", AuthorID: forager.ID,
		Result: domain.AttestPass, Summary: "old",
	}); err != nil {
		t.Fatal(err)
	}

	r := router.NewWithOptions(s, router.Options{
		AppSlug: "swarmjet",
		OnHead: func(ctx context.Context, hive domain.Hive, _ domain.Agent, pr int64, sha string) error {
			return s.SupersedeAttestations(ctx, hive.ID, pr, sha)
		},
	})
	payload := []byte(`{
		"action":"synchronize",
		"number":34,
		"pull_request":{
			"number":34,
			"state":"open",
			"merged":false,
			"html_url":"https://github.com/acme/hive/pull/34",
			"head":{"ref":"swarm/acme-hive/forager-1","sha":"new-head"},
			"base":{"ref":"dev","sha":"base"}
		},
		"repository":{"full_name":"acme/hive","html_url":"https://github.com/acme/hive"},
		"sender":{"login":"swarmjet[bot]","type":"Bot"}
	}`)
	event, ok := router.NormalizeDelivery(router.EventPullRequest, "delivery-head-change", payload)
	if !ok {
		t.Fatal("NormalizeDelivery rejected the payload")
	}
	decision, err := r.Route(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != router.ActionIgnored {
		t.Fatalf("decision = %+v, want ignored", decision)
	}
	attestations, err := s.Attestations(ctx, hive.ID, 34)
	if err != nil || len(attestations) != 1 || !attestations[0].Superseded {
		t.Fatalf("attestations = %+v err=%v, want the old head superseded", attestations, err)
	}
	if _, err = s.LatestAttestation(ctx, hive.ID, 34); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LatestAttestation = %v, want ErrNotFound", err)
	}
	// The mechanism must not deliver a message to the owner.
	messages, err := s.Inbox(ctx, hive.ID, domain.ForagerAddress(forager.ID), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("head change woke the owner: %+v", messages)
	}
}
