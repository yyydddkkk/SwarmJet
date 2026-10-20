package merge

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestReadyNotificationTarget(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, sender := range []domain.Address{"local:human", "forager:parent", domain.AddressSystem, ""} {
			hive := testHive()
			hive.QueenEnabled = enabled
			backend := &fakeStore{
				hive:  hive,
				owner: domain.Agent{ID: "worker", ChainID: "chain"},
				inbox: map[domain.Address][]domain.Message{
					"forager:worker": {{Kind: domain.MessageTask, From: sender}},
				},
			}
			scheduler := NewScheduler(backend, nil, nil, nil, nil)
			if err := scheduler.notifyReady(context.Background(), hive, readyReadiness()); err != nil {
				t.Fatal(err)
			}
			want := sender
			if want == "" || want == domain.AddressSystem {
				want = domain.AddressHive
			}
			if enabled {
				want = domain.AddressQueen
			}
			messages := backend.sent()
			if len(messages) != 1 || messages[0].To != want || messages[0].ChainID != "chain" || messages[0].Trust != domain.TrustSystem {
				t.Fatalf("enabled=%v sender=%q: messages=%+v, want %q", enabled, sender, messages, want)
			}
		}
	}
}
