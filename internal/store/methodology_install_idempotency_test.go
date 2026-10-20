package store

import (
	"context"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

func TestBuiltinInstallPreservesActiveVersionAndTracking(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/builtin-repeat")
	manifest, files := methodology.Builtin()
	input := domain.Methodology{
		HiveID: h.ID, Manifest: manifest,
		Source: &domain.MethodologySource{URL: "builtin:minimal", Ref: "v1"},
		Note:   "original", CreatedBy: domain.LocalAddress("installer"),
	}
	first, err := s.PublishMethodology(ctx, input, files)
	if err != nil {
		t.Fatal(err)
	}
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	var activatedBefore string
	if err := s.Pool.QueryRow(ctx, `SELECT activated_at::text FROM methodologies WHERE id=$1`, first.ID).Scan(&activatedBefore); err != nil {
		t.Fatal(err)
	}
	input.Note = "repeat install"
	input.CreatedBy = domain.LocalAddress("another-installer")
	again, err := s.PublishMethodology(ctx, input, files)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Version != first.Version || !again.Active ||
		again.Note != first.Note || again.CreatedBy != first.CreatedBy || again.CreatedAt.UnixMicro() != first.CreatedAt.UnixMicro() {
		t.Fatalf("reinstall changed immutable version: first=%+v again=%+v", first, again)
	}
	var activatedAfter string
	var failures int
	if err := s.Pool.QueryRow(ctx, `SELECT activated_at::text,consecutive_failures FROM methodologies WHERE id=$1`, first.ID).Scan(&activatedAfter, &failures); err != nil {
		t.Fatal(err)
	}
	if activatedAfter != activatedBefore || failures != 2 {
		t.Fatalf("tracking changed: activation=%s/%s failures=%d", activatedBefore, activatedAfter, failures)
	}
	versions, err := s.MethodologyVersions(ctx, h.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions=%+v err=%v", versions, err)
	}
	changes, err := s.MethodologyChanges(ctx, h.ID)
	if err != nil || len(changes) != 1 || changes[0].Action != methodology.ActionPublish {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
}

func TestBuiltinInstallOnlyDeduplicatesIdenticalActiveTemplate(t *testing.T) {
	for _, changed := range []string{"digest", "ref", "url", "inactive", "ordinary-publish"} {
		t.Run(changed, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			h := newTestHive(t, s, "acme/builtin-different")
			manifest, files := methodology.Builtin()
			input := domain.Methodology{
				HiveID: h.ID, Manifest: manifest,
				Source: &domain.MethodologySource{URL: "builtin:minimal", Ref: "v1"},
			}
			if changed == "ordinary-publish" {
				input.Source.URL = "https://example.invalid/methodology"
			}
			first, err := s.PublishMethodology(ctx, input, files)
			if err != nil {
				t.Fatal(err)
			}
			wantVersion := first.Version + 1
			switch changed {
			case "digest":
				input.Manifest.StandingOrders = []string{"customized content"}
			case "ref":
				input.Source.Ref = "v2"
			case "url":
				input.Source.URL = "builtin:another-template"
			case "inactive":
				publishTestMethodology(t, s, h.ID, "different active methodology")
				wantVersion++
			}
			next, err := s.PublishMethodology(ctx, input, files)
			if err != nil {
				t.Fatal(err)
			}
			if next.ID == first.ID || next.Version != wantVersion || !next.Active {
				t.Fatalf("different install deduplicated: first=%+v next=%+v", first, next)
			}
			if changed == "digest" && next.Digest == first.Digest {
				t.Fatal("changed content retained original digest")
			}
		})
	}
}

func TestConcurrentBuiltinInstallsShareVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/builtin-concurrent")
	manifest, files := methodology.Builtin()
	const installs = 8
	results := make([]domain.Methodology, installs)
	errs := make([]error, installs)
	var wg sync.WaitGroup
	for i := range installs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.PublishMethodology(ctx, domain.Methodology{
				HiveID: h.ID, Manifest: manifest,
				Source: &domain.MethodologySource{URL: "builtin:minimal", Ref: "v1"},
			}, files)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
		if results[i].ID != results[0].ID || results[i].Version != 1 || !results[i].Active {
			t.Fatalf("install %d created duplicate: %+v", i, results[i])
		}
	}
	changes, err := s.MethodologyChanges(ctx, h.ID)
	if err != nil || len(changes) != 1 {
		t.Fatalf("concurrent install changes=%+v err=%v", changes, err)
	}
}
