package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func recordAttestation(t *testing.T, s *Store, a domain.Attestation) domain.Attestation {
	t.Helper()
	got, created, err := s.RecordAttestation(context.Background(), a)
	if err != nil {
		t.Fatalf("RecordAttestation: %v", err)
	}
	if !created {
		t.Fatalf("RecordAttestation(%s/%s) was not created", a.HeadSHA, a.AttesterID)
	}
	return got
}

func TestRecordAttestationIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/attest")
	owner, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, Role: domain.RoleForager, PRNumber: 7})
	if err != nil {
		t.Fatal(err)
	}
	first := domain.Attestation{
		HiveID: h.ID, PRNumber: 7, HeadSHA: "head-1", AttesterID: "verifier-1",
		AuthorID: owner.ID, Result: domain.AttestPass, Summary: "looks good",
	}
	got := recordAttestation(t, s, first)
	if got.ID == "" || got.Superseded || got.CreatedAt.IsZero() {
		t.Fatalf("stored attestation = %+v", got)
	}
	if got.IsAuthor {
		t.Fatal("verifier was recorded as the author")
	}

	// The same attester and head must return the first record unchanged, even
	// though the retry carries a different summary.
	second := first
	second.Summary = "a retry must not rewrite history"
	again, created, err := s.RecordAttestation(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != got.ID || again.Summary != "looks good" {
		t.Fatalf("retry = %+v created=%v, want the original %+v", again, created, got)
	}

	// A different attester is a different conclusion.
	other := first
	other.AttesterID = "verifier-2"
	other.Result = domain.AttestFail
	recordAttestation(t, s, other)

	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM attestations WHERE hive_id=$1 AND pr_number=7`, h.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("attestation rows = %d, want 2", count)
	}
}

func TestLatestAttestationAndSupersede(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/supersede")
	now := time.Now().UTC()

	old := recordAttestation(t, s, domain.Attestation{
		HiveID: h.ID, PRNumber: 7, HeadSHA: "head-old", AttesterID: "verifier-1",
		Result: domain.AttestPass, Summary: "old", CreatedAt: now.Add(-2 * time.Minute),
	})
	current := recordAttestation(t, s, domain.Attestation{
		HiveID: h.ID, PRNumber: 7, HeadSHA: "head-new", AttesterID: "verifier-1",
		Result: domain.AttestPass, Summary: "new", CreatedAt: now.Add(-time.Minute),
	})

	latest, err := s.LatestAttestation(ctx, h.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != current.ID || latest.HeadSHA != "head-new" {
		t.Fatalf("latest = %+v, want %s", latest, current.ID)
	}

	if err = s.SupersedeAttestations(ctx, h.ID, 7, "head-new"); err != nil {
		t.Fatal(err)
	}
	all, err := s.Attestations(ctx, h.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("history = %+v, want both rows", all)
	}
	for _, a := range all {
		switch a.ID {
		case old.ID:
			if !a.Superseded {
				t.Fatal("old head was not superseded")
			}
		case current.ID:
			if a.Superseded {
				t.Fatal("current head was superseded")
			}
		}
	}

	// Superseding again is a no-op for the current head, and removing the
	// current head leaves no un-superseded conclusion.
	if err = s.SupersedeAttestations(ctx, h.ID, 7, "head-newer"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LatestAttestation(ctx, h.ID, 7); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LatestAttestation after superseding everything = %v, want ErrNotFound", err)
	}
}

func TestPRAuthorIncludesTerminalForager(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/author")
	owner, err := s.CreateForager(ctx, domain.Agent{
		HiveID: h.ID, Role: domain.RoleForager, State: domain.AgentCompleted, PRNumber: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PRAuthor(ctx, h.ID, 9)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != owner.ID {
		t.Fatalf("PRAuthor = %+v, want %s", got, owner.ID)
	}
	if _, err = s.PRAuthor(ctx, h.ID, 10); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("PRAuthor for an unknown PR = %v, want ErrNotFound", err)
	}
}
