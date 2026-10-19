package merge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func requireAllPolicy() domain.MergePolicy {
	return domain.MergePolicy{
		RequireAttestation: true,
		RequireNonAuthor:   true,
		RequireChecks:      true,
	}
}

func TestCheckReady(t *testing.T) {
	stub := readyStub()
	attestations := &fakeAttestations{latest: passAttestation()}
	store := &fakeStore{hive: testHive(), owner: domain.Agent{ID: "forager-1", HiveID: "hive-1"}}
	svc := newTestService(t, stub, store, attestations, methodologyWith(requireAllPolicy()), nil)

	readiness, err := svc.Check(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || len(readiness.Reasons) != 0 {
		t.Fatalf("readiness = %+v", readiness)
	}
	if readiness.HeadSHA != "head" || !readiness.AttestationFound || readiness.Attestation.Result != domain.AttestPass {
		t.Fatalf("readiness = %+v", readiness)
	}
	if len(readiness.Checks) != 1 || readiness.Checks[0].Name != "test" {
		t.Fatalf("checks = %+v", readiness.Checks)
	}
}

func TestCheckRuleReasons(t *testing.T) {
	cases := []struct {
		name       string
		policy     domain.MergePolicy
		latest     domain.Attestation
		latestErr  error
		mutate     func(*gitHubStub)
		wantReason string
	}{
		{
			name: "already merged", policy: requireAllPolicy(),
			mutate:     func(s *gitHubStub) { s.pull["state"] = "closed"; s.pull["merged"] = true },
			wantReason: "already merged",
		},
		{
			name: "closed", policy: requireAllPolicy(),
			mutate:     func(s *gitHubStub) { s.pull["state"] = "closed" },
			wantReason: "not open",
		},
		{
			name: "draft", policy: requireAllPolicy(),
			mutate:     func(s *gitHubStub) { s.pull["draft"] = true },
			wantReason: "draft",
		},
		{
			name: "wrong base repository", policy: requireAllPolicy(),
			mutate: func(s *gitHubStub) {
				s.pull["base"] = map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/other"}}
			},
			wantReason: "not the Hive repository",
		},
		{
			name: "missing attestation", policy: requireAllPolicy(),
			latestErr: domain.ErrNotFound, wantReason: "no attestation",
		},
		{
			name: "stale attestation", policy: requireAllPolicy(),
			latest:     func() domain.Attestation { a := passAttestation(); a.HeadSHA = "old"; return a }(),
			wantReason: "latest attestation is for",
		},
		{
			name: "fail attestation", policy: requireAllPolicy(),
			latest:     func() domain.Attestation { a := passAttestation(); a.Result = domain.AttestFail; return a }(),
			wantReason: "attestation is fail",
		},
		{
			name: "blocked attestation", policy: requireAllPolicy(),
			latest:     func() domain.Attestation { a := passAttestation(); a.Result = domain.AttestBlocked; return a }(),
			wantReason: "attestation is blocked",
		},
		{
			name: "author attestation", policy: requireAllPolicy(),
			latest:     func() domain.Attestation { a := passAttestation(); a.IsAuthor = true; return a }(),
			wantReason: "from the pull request author",
		},
		{
			name: "required check failed", policy: requireAllPolicy(),
			mutate: func(s *gitHubStub) {
				s.required = []any{map[string]any{"context": "test", "app_id": 7}}
				s.checks = []any{successCheck("test", "failure")}
			},
			wantReason: "required check failed",
		},
		{
			name: "conflict", policy: requireAllPolicy(),
			mutate: func(s *gitHubStub) {
				s.pull["mergeable"] = false
				s.pull["mergeable_state"] = "dirty"
			},
			wantReason: "conflict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := readyStub()
			if tc.mutate != nil {
				tc.mutate(stub)
			}
			latest := tc.latest
			if latest.ID == "" && tc.latestErr == nil {
				latest = passAttestation()
			}
			attestations := &fakeAttestations{latest: latest, latestErr: tc.latestErr}
			store := &fakeStore{hive: testHive(), owner: domain.Agent{ID: "forager-1", HiveID: "hive-1"}}
			svc := newTestService(t, stub, store, attestations, methodologyWith(tc.policy), nil)

			readiness, err := svc.Check(context.Background(), testHive(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.Ready {
				t.Fatalf("ready = true for %s", tc.name)
			}
			if joined := strings.Join(readiness.Reasons, "\n"); !strings.Contains(joined, tc.wantReason) {
				t.Fatalf("reasons = %q, want %q", joined, tc.wantReason)
			}
		})
	}
}

func TestCheckNoRequiredChecksFallback(t *testing.T) {
	cases := []struct {
		name   string
		checks []any
		ready  bool
		reason string
	}{
		{name: "all passed", checks: []any{successCheck("build", "success"), successCheck("lint", "skipped")}, ready: true},
		{name: "one failed", checks: []any{successCheck("build", "success"), successCheck("lint", "failure")}, ready: false, reason: "failed"},
		{name: "none", checks: []any{}, ready: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &gitHubStub{pull: testPull(), required: nil, checks: tc.checks}
			policy := domain.MergePolicy{RequireChecks: true}
			svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{}, methodologyWith(policy), nil)
			readiness, err := svc.Check(context.Background(), testHive(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.Ready != tc.ready {
				t.Fatalf("ready = %v, reasons = %v", readiness.Ready, readiness.Reasons)
			}
			if tc.reason != "" && !strings.Contains(strings.Join(readiness.Reasons, "\n"), tc.reason) {
				t.Fatalf("reasons = %v, want %q", readiness.Reasons, tc.reason)
			}
		})
	}
}

func TestCheckAcceptsPassNotesAndSkipsUnneededAttestation(t *testing.T) {
	notes := passAttestation()
	notes.Result = domain.AttestPassNotes
	stub := &gitHubStub{
		pull:     testPull(),
		required: []any{map[string]any{"context": "test", "app_id": 7}},
		checks:   []any{successCheck("test", "success")},
	}
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{latest: notes}, methodologyWith(requireAllPolicy()), nil)
	readiness, err := svc.Check(context.Background(), testHive(), 7)
	if err != nil || !readiness.Ready {
		t.Fatalf("pass_notes rejected: %+v %v", readiness.Reasons, err)
	}

	// A policy that does not require attestations must not demand one.
	policy := domain.MergePolicy{RequireChecks: true}
	svc = newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{}, methodologyWith(policy), nil)
	readiness, err = svc.Check(context.Background(), testHive(), 7)
	if err != nil || !readiness.Ready {
		t.Fatalf("attestation was demanded: %+v %v", readiness.Reasons, err)
	}
}

func TestCheckToleratesUnreadableCINotRequiredByTheMethodology(t *testing.T) {
	stub := readyStub()
	stub.checkStatus = http.StatusInternalServerError

	// Without requireChecks, an unreadable CI endpoint is not a merge
	// condition and must not block the gate.
	policy := domain.MergePolicy{RequireAttestation: false, RequireChecks: false}
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{}, methodologyWith(policy), nil)
	readiness, err := svc.Check(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready {
		t.Fatalf("CI was treated as a condition: %v", readiness.Reasons)
	}

	// With requireChecks the same failure is an explicit error, not a silent
	// "not ready" answer.
	svc = newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{}, methodologyWith(domain.MergePolicy{RequireChecks: true}), nil)
	if _, err = svc.Check(context.Background(), testHive(), 7); err == nil {
		t.Fatal("unreadable required checks were accepted")
	}
}

func TestMergePinsHeadSHAAndReportsGitHubRefusal(t *testing.T) {
	stub := readyStub()
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{latest: passAttestation()}, methodologyWith(requireAllPolicy()), nil)
	outcome, err := svc.Merge(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Merged || outcome.Queued || outcome.Method != "squash" || outcome.HeadSHA != "head" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !stub.mergeRequested || stub.mergeSHA != "head" || stub.mergeMethod != "squash" {
		t.Fatalf("merge request = %+v sha=%q method=%q", stub.mergeRequested, stub.mergeSHA, stub.mergeMethod)
	}

	refused := readyStub()
	refused.mergeStatus = http.StatusMethodNotAllowed
	svc = newTestService(t, refused, &fakeStore{hive: testHive()}, &fakeAttestations{latest: passAttestation()}, methodologyWith(requireAllPolicy()), nil)
	_, err = svc.Merge(context.Background(), testHive(), 7)
	if err == nil || !strings.Contains(err.Error(), "405") {
		t.Fatalf("err = %v, want GitHub refusal with its status", err)
	}
}

func TestMergeQueueReportsQueued(t *testing.T) {
	stub := readyStub()
	stub.rules = []any{map[string]any{"type": "merge_queue"}}
	stub.queueEntry = map[string]any{"id": "entry"}
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{latest: passAttestation()}, methodologyWith(requireAllPolicy()), nil)
	outcome, err := svc.Merge(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Queued || outcome.Merged || outcome.QueueEntryID != "entry" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.HeadSHA != "head" || stub.mergeSHA != "" {
		t.Fatalf("outcome = %+v, mergeSHA = %q", outcome, stub.mergeSHA)
	}
}

func TestMergeRefusesStaleAttestation(t *testing.T) {
	stub := readyStub()
	stale := passAttestation()
	stale.HeadSHA = "old"
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{latest: stale}, methodologyWith(requireAllPolicy()), nil)
	_, err := svc.Merge(context.Background(), testHive(), 7)
	var notReady *NotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("err = %v, want NotReadyError", err)
	}
	if stub.mergeRequested {
		t.Fatal("stale attestation still merged")
	}
}

func TestPolicyAndPermissionsFallBackToBuiltin(t *testing.T) {
	svc := New(Options{})
	policy, err := svc.Policy(context.Background(), "hive-1")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.RequireChecks || policy.RequireAttestation || policy.Auto || policy.Method != "squash" {
		t.Fatalf("builtin policy = %+v", policy)
	}
	permissions, err := svc.Permissions(context.Background(), "hive-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !permissions.Publish || !permissions.Message || permissions.Attest || permissions.Merge {
		t.Fatalf("builtin worker permissions = %+v", permissions)
	}
	if _, err = svc.Permissions(context.Background(), "hive-1", "verifier"); err == nil {
		t.Fatal("builtin methodology invented a verifier role")
	}
}

func TestPermissionsUseMethodologyRoles(t *testing.T) {
	svc := New(Options{Methodology: methodologyWith(requireAllPolicy())})
	verifier, err := svc.Permissions(context.Background(), "hive-1", "verifier")
	if err != nil || !verifier.Attest || verifier.Merge {
		t.Fatalf("verifier permissions = %+v %v", verifier, err)
	}
	merger, err := svc.Permissions(context.Background(), "hive-1", "merger")
	if err != nil || !merger.Merge || merger.Attest {
		t.Fatalf("merger permissions = %+v %v", merger, err)
	}
	if _, err = svc.Permissions(context.Background(), "hive-1", "missing"); err == nil {
		t.Fatal("unknown role was accepted")
	}
}
