package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestSendMessageValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	if _, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, From: domain.DroneAddress("a"), To: domain.DroneAddress("b"), Kind: domain.MessageEvent}); !errors.Is(err, domain.ErrPeerMessage) {
		t.Fatalf("peer message accepted: %v", err)
	}
	if _, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive, Kind: domain.MessageEvent, Hop: 11}); !errors.Is(err, domain.ErrHopLimit) {
		t.Fatalf("hop limit not enforced: %v", err)
	}
	if _, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive, Kind: domain.MessageEvent, Hop: 10, Body: "at limit"}); err != nil {
		t.Fatalf("hop at limit rejected: %v", err)
	}
	if _, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, To: domain.AddressHive, Body: strings.Repeat("é", domain.MaxMessageBody+1)}); !errors.Is(err, domain.ErrMessageTooLarge) {
		t.Fatalf("oversized body accepted: %v", err)
	}
	if _, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, To: domain.AddressHive, Body: strings.Repeat("é", domain.MaxMessageBody)}); err != nil {
		t.Fatalf("body at character limit rejected: %v", err)
	}
	if _, err := s.SendMessage(ctx, domain.Message{HiveID: "missing", To: domain.AddressHive}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing hive accepted: %v", err)
	}
}

func TestSendMessageIdempotencyAndConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	m := domain.Message{ID: "fixed-1", HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressQueen, Kind: domain.MessageEvent,
		ChainID: "c1", Body: "hello", Artifacts: []domain.ArtifactRef{{Kind: "pr", Ref: "42"}}}
	first, err := s.SendMessage(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SendMessage(ctx, m)
	if err != nil || second.ID != first.ID || second.Body != first.Body {
		t.Fatalf("not idempotent: %+v %v", second, err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id=$1`, "fixed-1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
	conflict := m
	conflict.Body = "different"
	if _, err = s.SendMessage(ctx, conflict); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("conflicting content accepted: %v", err)
	}
	a, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, To: domain.AddressHive, Body: "a"})
	if err != nil || a.ID == "" {
		t.Fatalf("%+v %v", a, err)
	}
	b, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, To: domain.AddressHive, Body: "b"})
	if err != nil || b.ID == a.ID || !(a.ID < b.ID) {
		t.Fatalf("generated ids not ordered: %q %q (%v)", a.ID, b.ID, err)
	}
}

func TestInboxPaginationOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	for _, id := range []string{"m1", "m2", "m3"} {
		if _, err := s.SendMessage(ctx, domain.Message{ID: id, HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageEvent, Body: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SendMessage(ctx, domain.Message{ID: "z1", HiveID: h.ID, To: domain.AddressQueen, Kind: domain.MessageEvent, Body: "z"}); err != nil {
		t.Fatal(err)
	}
	page1, err := s.Inbox(ctx, h.ID, domain.AddressHive, "", 2)
	if err != nil || len(page1) != 2 || page1[0].ID != "m1" || page1[1].ID != "m2" {
		t.Fatalf("%+v %v", page1, err)
	}
	page2, err := s.Inbox(ctx, h.ID, domain.AddressHive, page1[1].ID, 2)
	if err != nil || len(page2) != 1 || page2[0].ID != "m3" {
		t.Fatalf("%+v %v", page2, err)
	}
	queen, err := s.Inbox(ctx, h.ID, domain.AddressQueen, "", 10)
	if err != nil || len(queen) != 1 || queen[0].ID != "z1" {
		t.Fatalf("%+v %v", queen, err)
	}
	all, err := s.Inbox(ctx, h.ID, domain.AddressHive, "", 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("zero limit did not use default: %+v %v", all, err)
	}
}

func TestClaimMessageLease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	m, err := s.SendMessage(ctx, domain.Message{ID: "claim-1", HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageEvent})
	if err != nil {
		t.Fatal(err)
	}
	owners := []string{"queen", "local:a"}
	wins := make([]bool, len(owners))
	var wg sync.WaitGroup
	for i := range owners {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := s.ClaimMessage(ctx, m.ID, owners[i], time.Minute)
			if err != nil {
				t.Error(err)
			}
			wins[i] = ok
		}(i)
	}
	wg.Wait()
	if wins[0] == wins[1] {
		t.Fatalf("claim was not exclusive: %v", wins)
	}
	winner, loser := owners[0], owners[1]
	if !wins[0] {
		winner, loser = owners[1], owners[0]
	}
	if ok, err := s.ClaimMessage(ctx, m.ID, winner, time.Minute); err != nil || !ok {
		t.Fatalf("renew failed: %v %v", ok, err)
	}
	if ok, err := s.ClaimMessage(ctx, m.ID, loser, time.Minute); err != nil || ok {
		t.Fatalf("live lease double-claimed: %v %v", ok, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE messages SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, m.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimMessage(ctx, m.ID, loser, time.Minute); err != nil || !ok {
		t.Fatalf("expired lease not reclaimable: %v %v", ok, err)
	}
	if err := s.MarkMessageHandled(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimMessage(ctx, m.ID, "third", time.Minute); err != nil || ok {
		t.Fatalf("handled message claimed: %v %v", ok, err)
	}
	if err := s.MarkMessageHandled(ctx, m.ID); err != nil {
		t.Fatalf("repeat handle: %v", err)
	}
	if err := s.MarkMessageHandled(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing handle: %v", err)
	}
}

func TestWaitInboxFastPathAndWake(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	// A message that already exists returns immediately.
	if _, err := s.SendMessage(ctx, domain.Message{ID: "w1", HiveID: h.ID, To: domain.AddressQueen, Kind: domain.MessageEvent}); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitInbox(ctx, h.ID, domain.AddressQueen, ""); err != nil {
		t.Fatalf("fast path: %v", err)
	}
	// A new message wakes a waiter blocked on an empty Inbox.
	wakeCtx, cancelWake := context.WithTimeout(ctx, 10*time.Second)
	defer cancelWake()
	woke := make(chan error, 1)
	go func() { woke <- s.WaitInbox(wakeCtx, h.ID, domain.AddressHive, "") }()
	time.Sleep(200 * time.Millisecond)
	if _, err := s.SendMessage(ctx, domain.Message{ID: "w2", HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageEvent}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-woke:
		if err != nil {
			t.Fatalf("waiter woke with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitInbox was not woken by a new message")
	}
	// With nothing new, the wait ends when the context does.
	shortCtx, cancelShort := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelShort()
	start := time.Now()
	err := s.WaitInbox(shortCtx, h.ID, domain.DroneAddress("nobody"), "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("wait returned early after %s", time.Since(start))
	}
}

func TestDeliverMessageSink(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	m, err := s.SendMessage(ctx, domain.Message{ID: "d1", HiveID: h.ID, To: domain.AddressQueen, Kind: domain.MessageTask, Body: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if m.State != domain.MessagePending {
		t.Fatalf("new message state %s", m.State)
	}
	var delivered []domain.Message
	s.MessageSink = func(_ context.Context, msg domain.Message) error {
		delivered = append(delivered, msg)
		return nil
	}
	if err = s.DeliverMessage(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].ID != m.ID || delivered[0].State != domain.MessageDelivered {
		t.Fatalf("sink got %+v", delivered)
	}
	got, err := s.Inbox(ctx, h.ID, domain.AddressQueen, "", 10)
	if err != nil || len(got) != 1 || got[0].State != domain.MessageDelivered {
		t.Fatalf("%+v %v", got, err)
	}
	if err = s.DeliverMessage(ctx, "missing"); err != nil {
		t.Fatalf("delivering a missing message: %v", err)
	}
}
