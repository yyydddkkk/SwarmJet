package store

import (
	"context"
	"errors"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/router"
	"testing"
)

func TestSendMessagesRollbackAndIdempotency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	hive := newTestHive(t, s, "acme/batch")
	control := domain.Message{ID: "control", HiveID: hive.ID, From: domain.AddressSystem, To: domain.AddressHive, Kind: domain.MessageControl, Body: "complete"}
	invalid := domain.Message{ID: "event", HiveID: "missing", To: domain.AddressHive, Kind: domain.MessageEvent}
	if _, err := s.SendMessages(ctx, control, invalid); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("batch error = %v", err)
	}
	messages, err := s.Inbox(ctx, hive.ID, domain.AddressHive, "", 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("partial batch visible: %+v, %v", messages, err)
	}
	event := invalid
	event.HiveID = hive.ID
	event.Body = "closed event"
	for i := 0; i < 2; i++ {
		if _, err := s.SendMessages(ctx, control, event); err != nil {
			t.Fatal(err)
		}
	}
	messages, err = s.Inbox(ctx, hive.ID, domain.AddressHive, "", 10)
	if err != nil || len(messages) != 2 || messages[0].ID != control.ID || messages[1].ID != event.ID {
		t.Fatalf("batch = %+v, %v", messages, err)
	}
}

func TestRouterClosedPROwnerAtomicInbox(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "merged"}[merged], func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			hive := newTestHive(t, s, "acme/closed")
			owner, err := s.CreateForager(ctx, domain.Agent{HiveID: hive.ID, Task: "task", PRNumber: 42})
			if err != nil {
				t.Fatal(err)
			}
			e := router.Event{Type: router.EventPullRequest, Action: "closed", Repository: hive.Repository, Number: 42, Merged: merged, DeliveryID: "close"}
			for i := 0; i < 2; i++ {
				if _, err := router.New(s).Route(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			inbox, err := s.Unhandled(ctx, hive.ID, domain.ForagerAddress(owner.ID), 10)
			if err != nil || len(inbox) != 2 || inbox[0].Trust != domain.TrustSystem || inbox[1].Trust != domain.TrustExternal || inbox[1].Body != router.Digest(e) {
				t.Fatalf("Inbox = %+v, %v", inbox, err)
			}
		})
	}
}
