package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

func TestSendMessageValidation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	if _, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, From: domain.ForagerAddress("a"), To: domain.ForagerAddress("b"), Kind: domain.MessageEvent}); !errors.Is(err, domain.ErrPeerMessage) {
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
	if err != nil || b.ID == a.ID {
		t.Fatalf("generated ids not unique: %q %q (%v)", a.ID, b.ID, err)
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

func TestInboxCursorUsesInsertionSequence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	// The Router picks IDs like "gh-<hash>", which sort after the generated
	// "0..." IDs. Insertion order, not text order, must drive the cursor.
	if _, err := s.SendMessage(ctx, domain.Message{ID: "gh-ffffffffffff", HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageEvent, Body: "first"}); err != nil {
		t.Fatal(err)
	}
	later, err := s.SendMessage(ctx, domain.Message{HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageEvent, Body: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if later.ID >= "gh-ffffffffffff" {
		t.Fatalf("test precondition: generated id %q should sort before the caller id", later.ID)
	}
	page, err := s.Inbox(ctx, h.ID, domain.AddressHive, "gh-ffffffffffff", 10)
	if err != nil || len(page) != 1 || page[0].ID != later.ID {
		t.Fatalf("cursor by caller ID lost the later message: %+v %v", page, err)
	}
	all, err := s.Inbox(ctx, h.ID, domain.AddressHive, "", 10)
	if err != nil || len(all) != 2 || all[0].ID != "gh-ffffffffffff" || all[1].ID != later.ID {
		t.Fatalf("messages not in insertion order: %+v %v", all, err)
	}
	// WaitInbox uses the same seq-based existence check.
	if err := s.WaitInbox(ctx, h.ID, domain.AddressHive, "gh-ffffffffffff"); err != nil {
		t.Fatalf("wait after caller id: %v", err)
	}
	// A cursor that no longer exists cannot be resumed.
	if _, err := s.Inbox(ctx, h.ID, domain.AddressHive, "gh-000000000000", 10); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing cursor: %v", err)
	}
	if err := s.WaitInbox(ctx, h.ID, domain.AddressHive, "gh-000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing wait cursor: %v", err)
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
	err := s.WaitInbox(shortCtx, h.ID, domain.ForagerAddress("nobody"), "")
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

func TestUnhandledReturnsPendingAndDeliveredInOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/unhandled")
	other := newTestHive(t, s, "acme/unhandled-other")
	for _, id := range []string{"z-first", "a-second", "m-third"} {
		if _, err := s.SendMessage(ctx, domain.Message{ID: id, HiveID: h.ID, To: domain.AddressQueen, Kind: domain.MessageTask, Body: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkMessageHandled(ctx, "a-second"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeliverMessage(ctx, "m-third"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Unhandled(ctx, h.ID, domain.AddressQueen, 10)
	if err != nil || len(got) != 2 || got[0].ID != "z-first" || got[1].ID != "m-third" {
		t.Fatalf("%+v %v", got, err)
	}
	if got[0].State != domain.MessagePending || got[1].State != domain.MessageDelivered {
		t.Fatalf("states: %s %s", got[0].State, got[1].State)
	}
	limited, err := s.Unhandled(ctx, h.ID, domain.AddressQueen, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != "z-first" {
		t.Fatalf("%+v %v", limited, err)
	}
	if empty, err := s.Unhandled(ctx, other.ID, domain.AddressQueen, 10); err != nil || len(empty) != 0 {
		t.Fatalf("%+v %v", empty, err)
	}
	if empty, err := s.Unhandled(ctx, h.ID, domain.AddressHive, 10); err != nil || len(empty) != 0 {
		t.Fatalf("%+v %v", empty, err)
	}
}

// jobsWithoutMessages implements the old Inserter surface but not message
// delivery, which the Store must report instead of silently skipping.
type jobsWithoutMessages struct{}

func (jobsWithoutMessages) InsertWebhook(context.Context, pgx.Tx, jobs.Webhook) error { return nil }

func TestSendMessageRequiresDeliveryQueue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/queue")
	for _, jobsValue := range []jobs.Inserter{nil, jobsWithoutMessages{}} {
		s.Jobs = jobsValue
		if _, err := s.SendMessage(ctx, domain.Message{ID: "no-queue", HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageTask}); !errors.Is(err, ErrMessageQueueUnavailable) {
			t.Fatalf("jobs=%T: %v", jobsValue, err)
		}
		var count int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id='no-queue'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("message stranded without a queue: %d %v", count, err)
		}
	}
	queue := &memoryJobs{}
	s.Jobs = queue
	if _, err := s.SendMessage(ctx, domain.Message{ID: "queued", HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageTask}); err != nil {
		t.Fatal(err)
	}
	if got := queue.delivered(); len(got) != 1 || got[0] != "queued" {
		t.Fatalf("delivery queue got %v", got)
	}
}

func TestInboxCursorNeverSkipsConcurrentCommit(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h := newTestHive(t, s, "acme/order")
	// Delay the first row after its seq is assigned but before its transaction
	// commits. Without the per-Hive advisory lock, "fast" would commit with a
	// newer seq first and a cursor reader would skip "slow" forever.
	if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION test_slow_message() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.id = 'slow' THEN PERFORM pg_sleep(1); END IF;
	RETURN NEW;
END
$$`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `CREATE TRIGGER test_slow_message AFTER INSERT ON messages FOR EACH ROW EXECUTE FUNCTION test_slow_message()`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.SendMessage(ctx, domain.Message{ID: "slow", HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageEvent, Body: "slow"})
		errs <- err
	}()
	time.Sleep(150 * time.Millisecond)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := s.SendMessage(ctx, domain.Message{ID: "fast", HiveID: h.ID, To: domain.AddressHive, Kind: domain.MessageEvent, Body: "fast"})
		errs <- err
	}()

	var observed []string
	cursor := ""
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && len(observed) < 2 {
		page, err := s.Inbox(ctx, h.ID, domain.AddressHive, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range page {
			observed = append(observed, message.ID)
			cursor = message.ID
		}
		if len(observed) < 2 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(observed) != 2 || observed[0] != "slow" || observed[1] != "fast" {
		t.Fatalf("cursor skipped a concurrent commit: %v", observed)
	}
}

// listenerPIDs returns the PIDs of this Store's shared LISTEN connections.
func listenerPIDs(t *testing.T, s *Store) []int32 {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(), `SELECT pid FROM pg_stat_activity
		WHERE query LIKE 'LISTEN '||$1||'%' AND pid <> pg_backend_pid()
		ORDER BY pid`, inboxChannel)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var pids []int32
	for rows.Next() {
		var pid int32
		if err = rows.Scan(&pid); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return pids
}

func waitForListener(t *testing.T, s *Store, notPID int32) int32 {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, pid := range listenerPIDs(t, s) {
			if pid != notPID {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no LISTEN connection (excluding %d)", notPID)
	return 0
}

func TestWaitInboxSharesOneListener(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newTestHive(t, s, "acme/listener")
	const waiters = 50
	results := make(chan error, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.WaitInbox(ctx, h.ID, domain.AddressQueen, "")
		}()
	}
	waitForListener(t, s, 0)
	// Every waiter shares that one LISTEN connection; they never each open one.
	if got := listenerPIDs(t, s); len(got) != 1 {
		t.Fatalf("listener connections=%v", got)
	}
	if _, err := s.SendMessage(ctx, domain.Message{ID: "wake-all", HiveID: h.ID, To: domain.AddressQueen, Kind: domain.MessageTask}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("waiters were not woken")
	}
	for i := 0; i < waiters; i++ {
		if err := <-results; err != nil {
			t.Fatalf("waiter %d: %v", i, err)
		}
	}
	if got := listenerPIDs(t, s); len(got) != 1 {
		t.Fatalf("listener connections after wake=%v", got)
	}
}

func TestWaitInboxListenerReconnects(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newTestHive(t, s, "acme/reconnect")
	woke := make(chan error, 1)
	go func() { woke <- s.WaitInbox(ctx, h.ID, domain.AddressQueen, "") }()
	oldPID := waitForListener(t, s, 0)
	time.Sleep(100 * time.Millisecond) // let the waiter register
	if _, err := s.Pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, oldPID); err != nil {
		t.Fatal(err)
	}
	newPID := waitForListener(t, s, oldPID)
	if newPID == oldPID {
		t.Fatal("listener did not reconnect")
	}
	// Once the replacement connection is listening, a notification must wake
	// the waiter well before the periodic re-check interval.
	start := time.Now()
	if _, err := s.SendMessage(ctx, domain.Message{ID: "after-reconnect", HiveID: h.ID, To: domain.AddressQueen, Kind: domain.MessageTask}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-woke:
		if err != nil {
			t.Fatalf("waiter: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
			t.Fatalf("wake took %s, probably fell back to the periodic check", elapsed)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("waiter was not woken after reconnect")
	}
}
