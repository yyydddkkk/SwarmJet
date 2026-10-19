package merge

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

type fakeInserter struct {
	mu   sync.Mutex
	jobs []jobs.MergeCheck
}

func (f *fakeInserter) InsertMergeCheck(_ context.Context, job jobs.MergeCheck) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	return nil
}

func (f *fakeInserter) inserted() []jobs.MergeCheck {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jobs.MergeCheck(nil), f.jobs...)
}

func readyStub() *gitHubStub {
	return &gitHubStub{
		pull:          testPull(),
		required:      []any{map[string]any{"context": "test", "app_id": 7}},
		checks:        []any{successCheck("test", "success")},
		mergeResponse: map[string]any{"merged": true},
	}
}

func TestSchedulerEnqueuesOnChecksAndVerdict(t *testing.T) {
	inserter := &fakeInserter{}
	scheduler := NewScheduler(&fakeStore{}, nil, inserter, Options{})
	hive := testHive()
	if err := scheduler.OnChecks(context.Background(), hive, 7, "head", "success"); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.OnVerdict(context.Background(), hive, passVerdict()); err != nil {
		t.Fatal(err)
	}
	got := inserter.inserted()
	if len(got) != 2 {
		t.Fatalf("jobs = %+v", got)
	}
	for _, job := range got {
		if job.HiveID != "hive-1" || job.PR != 7 || job.SHA != "head" {
			t.Fatalf("job = %+v", job)
		}
	}
}

func TestSchedulerAutoMergeEnabledMerges(t *testing.T) {
	stub := readyStub()
	store := &fakeStore{hive: testHive()}
	store.hive.Config.Verify.AutoMerge = true
	gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, store)
	scheduler := NewScheduler(store, gate, &fakeInserter{}, Options{})
	if err := scheduler.Handle(context.Background(), jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "head"}); err != nil {
		t.Fatal(err)
	}
	if !stub.mergeRequested || stub.mergeSHA != "head" {
		t.Fatalf("auto merge did not request the head SHA: %+v", stub)
	}
	if messages := store.sent(); len(messages) != 0 {
		t.Fatalf("auto merge still notified: %+v", messages)
	}
}

func TestSchedulerAutoMergeDisabledNotifiesOnce(t *testing.T) {
	stub := readyStub()
	store := &fakeStore{
		hive:  testHive(),
		owner: domain.Agent{ID: "forager-1", HiveID: "hive-1", ChainID: "chain-1"},
		inbox: map[domain.Address][]domain.Message{
			domain.ForagerAddress("forager-1"): {
				{ID: "task-1", HiveID: "hive-1", From: "local:human", To: domain.ForagerAddress("forager-1"), Kind: domain.MessageTask, Body: "fix it"},
			},
		},
	}
	gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, store)
	scheduler := NewScheduler(store, gate, &fakeInserter{}, Options{})
	job := jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "head"}
	for range 2 {
		if err := scheduler.Handle(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if stub.mergeRequested {
		t.Fatal("autoMerge is off but the scheduler merged")
	}
	messages := store.sent()
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want exactly one ready notice", messages)
	}
	message := messages[0]
	if message.To != domain.LocalAddress("human") || message.Kind != domain.MessageReport {
		t.Fatalf("message = %+v", message)
	}
	if !strings.Contains(message.Body, "可以合并") || !strings.Contains(message.Body, "head") {
		t.Fatalf("body = %q", message.Body)
	}
}

func TestSchedulerIgnoresStaleJob(t *testing.T) {
	stub := readyStub()
	store := &fakeStore{hive: testHive()}
	gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, store)
	scheduler := NewScheduler(store, gate, &fakeInserter{}, Options{})
	if err := scheduler.Handle(context.Background(), jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "other"}); err != nil {
		t.Fatal(err)
	}
	if stub.mergeRequested || len(store.sent()) != 0 {
		t.Fatalf("stale job had side effects: merge=%v messages=%+v", stub.mergeRequested, store.sent())
	}
}

func TestSchedulerDoesNotNotifyWhenNotReady(t *testing.T) {
	stub := readyStub()
	store := &fakeStore{hive: testHive()}
	gate := newTestGate(t, stub, fakeVerdicts{err: domain.ErrNotFound}, store)
	scheduler := NewScheduler(store, gate, &fakeInserter{}, Options{})
	if err := scheduler.Handle(context.Background(), jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "head"}); err != nil {
		t.Fatal(err)
	}
	if stub.mergeRequested || len(store.sent()) != 0 {
		t.Fatalf("not-ready job had side effects: merge=%v messages=%+v", stub.mergeRequested, store.sent())
	}
}
