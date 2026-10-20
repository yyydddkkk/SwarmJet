package merge

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

func readyReadiness() Readiness {
	return Readiness{
		Ready:   true,
		HeadSHA: "head",
		Pull: Pull{
			Number: 7, State: "open", HeadSHA: "head",
			URL: "https://github.com/acme/widgets/pull/7",
		},
		Checks:  []Check{},
		Reasons: []string{},
	}
}

func TestSchedulerEnqueuesOnChecksAndAttestation(t *testing.T) {
	inserter := &fakeInserter{}
	scheduler := NewScheduler(&fakeStore{}, nil, nil, nil, inserter)
	hive := testHive()
	if err := scheduler.OnChecks(context.Background(), hive, 7, "head", "success"); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.OnAttestation(context.Background(), hive, passAttestation()); err != nil {
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
	store := &fakeStore{hive: testHive()}
	merger := &fakeMerger{outcome: Outcome{Merged: true, HeadSHA: "head"}}
	scheduler := NewScheduler(store, fakeChecker{readiness: readyReadiness()}, merger,
		fakePolicies{policy: domain.MergePolicy{Auto: true}}, &fakeInserter{})
	if err := scheduler.Handle(context.Background(), jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "head"}); err != nil {
		t.Fatal(err)
	}
	if merger.mergeCall != 1 || merger.mergeSHA != "head" {
		t.Fatalf("merge = %+v", merger)
	}
	if messages := store.sent(); len(messages) != 0 {
		t.Fatalf("auto merge still notified: %+v", messages)
	}
}

func TestSchedulerAutoMergeDisabledNotifiesOnce(t *testing.T) {
	store := &fakeStore{
		hive:  testHive(),
		owner: domain.Agent{ID: "forager-1", HiveID: "hive-1", ChainID: "chain-1"},
		inbox: map[domain.Address][]domain.Message{
			domain.ForagerAddress("forager-1"): {
				{ID: "task-1", HiveID: "hive-1", From: "local:human", To: domain.ForagerAddress("forager-1"), Kind: domain.MessageTask, Body: "fix it"},
			},
		},
	}
	merger := &fakeMerger{}
	scheduler := NewScheduler(store, fakeChecker{readiness: readyReadiness()}, merger,
		fakePolicies{policy: domain.MergePolicy{Auto: false}}, &fakeInserter{})
	job := jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "head"}
	for range 2 {
		if err := scheduler.Handle(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if merger.mergeCall != 0 {
		t.Fatal("auto is off but the scheduler merged")
	}
	messages := store.sent()
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want exactly one ready notice", messages)
	}
	message := messages[0]
	if message.To != domain.LocalAddress("human") || message.Kind != domain.MessageReport || message.Trust != domain.TrustSystem {
		t.Fatalf("message = %+v", message)
	}
	if !strings.Contains(message.Body, "可以合并") || !strings.Contains(message.Body, "head") {
		t.Fatalf("body = %q", message.Body)
	}
}

func TestSchedulerNotifiesTheBranchOwnersDispatcher(t *testing.T) {
	store := &fakeStore{
		hive:        testHive(),
		owner:       domain.Agent{ID: "verifier-1", ChainID: "wrong-chain"},
		branchOwner: domain.Agent{ID: "forager-1", ChainID: "chain-1"},
		inbox: map[domain.Address][]domain.Message{
			domain.ForagerAddress("forager-1"): {{
				ID: "task-1", HiveID: "hive-1", From: "local:human",
				To: domain.ForagerAddress("forager-1"), Kind: domain.MessageTask, Body: "fix it",
			}},
		},
	}
	readiness := readyReadiness()
	readiness.Pull.HeadRef = "feat"
	scheduler := NewScheduler(store, fakeChecker{readiness: readiness}, &fakeMerger{},
		fakePolicies{policy: domain.MergePolicy{Auto: false}}, &fakeInserter{})
	if err := scheduler.Handle(context.Background(), jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "head"}); err != nil {
		t.Fatal(err)
	}
	messages := store.sent()
	if len(messages) != 1 || messages[0].To != domain.LocalAddress("human") {
		t.Fatalf("messages = %+v, want the branch owner's dispatcher", messages)
	}
	if messages[0].ChainID != "chain-1" {
		t.Fatalf("chain = %q, want the branch owner's chain", messages[0].ChainID)
	}
}

func TestSchedulerIgnoresStaleJob(t *testing.T) {
	store := &fakeStore{hive: testHive()}
	merger := &fakeMerger{}
	scheduler := NewScheduler(store, fakeChecker{readiness: readyReadiness()}, merger,
		fakePolicies{policy: domain.MergePolicy{Auto: true}}, &fakeInserter{})
	if err := scheduler.Handle(context.Background(), jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "other"}); err != nil {
		t.Fatal(err)
	}
	if merger.mergeCall != 0 || len(store.sent()) != 0 {
		t.Fatalf("stale job had side effects: merge=%d messages=%+v", merger.mergeCall, store.sent())
	}
}

func TestSchedulerDoesNotNotifyWhenNotReady(t *testing.T) {
	store := &fakeStore{hive: testHive()}
	merger := &fakeMerger{}
	scheduler := NewScheduler(store, fakeChecker{readiness: Readiness{Ready: false, HeadSHA: "head", Reasons: []string{"no attestation"}}}, merger,
		fakePolicies{policy: domain.MergePolicy{Auto: false}}, &fakeInserter{})
	if err := scheduler.Handle(context.Background(), jobs.MergeCheck{HiveID: "hive-1", PR: 7, SHA: "head"}); err != nil {
		t.Fatal(err)
	}
	if merger.mergeCall != 0 || len(store.sent()) != 0 {
		t.Fatalf("not-ready job had side effects: merge=%d messages=%+v", merger.mergeCall, store.sent())
	}
}
