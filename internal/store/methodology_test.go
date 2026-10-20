package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

func publishTestMethodology(t *testing.T, s *Store, hiveID, note string) domain.Methodology {
	t.Helper()
	manifest, files := methodology.Builtin()
	manifest.StandingOrders = []string{"order for " + note}
	published, err := s.PublishMethodology(context.Background(), domain.Methodology{
		HiveID:    hiveID,
		Manifest:  manifest,
		Note:      note,
		CreatedBy: domain.LocalAddress("tester"),
	}, files)
	if err != nil {
		t.Fatalf("PublishMethodology(%s): %v", note, err)
	}
	return published
}

func TestActiveMethodologyMissingReturnsNotFound(t *testing.T) {
	s := testStore(t)
	h := newTestHive(t, s, "acme/methodology")
	if _, _, err := s.ActiveMethodology(context.Background(), h.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ActiveMethodology without a version = %v", err)
	}
	if _, err := s.MethodologyVersions(context.Background(), h.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPublishActivateHistoryAndRollback(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/methodology")

	first := publishTestMethodology(t, s, h.ID, "first")
	if first.Version != 1 || !first.Active || first.Digest == "" {
		t.Fatalf("first version = %+v", first)
	}
	expectedDigest, err := methodology.Digest(first.Manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != expectedDigest {
		t.Fatalf("stored digest = %s, want %s", first.Digest, expectedDigest)
	}
	second := publishTestMethodology(t, s, h.ID, "second")
	if second.Version != 2 || !second.Active {
		t.Fatalf("second version = %+v", second)
	}

	active, files, err := s.ActiveMethodology(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != 2 || active.Note != "second" || len(files) != 0 {
		t.Fatalf("active = %+v files=%v", active, files)
	}

	versions, err := s.MethodologyVersions(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || !versions[0].Active || versions[1].Active {
		t.Fatalf("versions = %+v", versions)
	}

	// Roll back to version 1.
	rolledBack, err := s.ActivateMethodology(ctx, h.ID, 1, domain.LocalAddress("tester"), "rollback")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Version != 1 || !rolledBack.Active {
		t.Fatalf("activated = %+v", rolledBack)
	}
	active, _, err = s.ActiveMethodology(ctx, h.ID)
	if err != nil || active.Version != 1 {
		t.Fatalf("active after rollback = %+v, %v", active, err)
	}
	versions, err = s.MethodologyVersions(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	activeCount := 0
	for _, version := range versions {
		if version.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("active versions = %d: %+v", activeCount, versions)
	}

	changes, err := s.MethodologyChanges(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Action != methodology.ActionPublish || changes[0].Version != 1 {
		t.Fatalf("change 0 = %+v", changes[0])
	}
	if changes[2].Action != methodology.ActionActivate || changes[2].Version != 1 ||
		changes[2].Actor != domain.LocalAddress("tester") || changes[2].Note != "rollback" {
		t.Fatalf("change 2 = %+v", changes[2])
	}

	version, _, err := s.MethodologyVersion(ctx, h.ID, 1)
	if err != nil || version.Note != "first" {
		t.Fatalf("version 1 = %+v, %v", version, err)
	}
	if _, _, err = s.MethodologyVersion(ctx, h.ID, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing version = %v", err)
	}
	if _, err = s.ActivateMethodology(ctx, h.ID, 99, domain.LocalAddress("tester"), ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("activating missing version = %v", err)
	}
	if _, err = s.ActivateMethodology(ctx, "missing-hive", 1, domain.LocalAddress("tester"), ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("activating missing hive = %v", err)
	}
}

func TestConcurrentPublishesAllocateUniqueVersions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/concurrent")

	const publishes = 12
	results := make([]domain.Methodology, publishes)
	errs := make([]error, publishes)
	var wg sync.WaitGroup
	for i := 0; i < publishes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			manifest, files := methodology.Builtin()
			manifest.StandingOrders = []string{"concurrent"}
			results[i], errs[i] = s.PublishMethodology(ctx, domain.Methodology{
				HiveID:    h.ID,
				Manifest:  manifest,
				Note:      "concurrent",
				CreatedBy: domain.LocalAddress("tester"),
			}, files)
		}(i)
	}
	wg.Wait()

	seen := map[int]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		if seen[results[i].Version] {
			t.Fatalf("duplicate version %d", results[i].Version)
		}
		seen[results[i].Version] = true
	}
	for version := 1; version <= publishes; version++ {
		if !seen[version] {
			t.Fatalf("missing version %d: seen=%v", version, seen)
		}
	}

	versions, err := s.MethodologyVersions(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, version := range versions {
		if version.Active {
			active++
		}
	}
	if len(versions) != publishes || active != 1 {
		t.Fatalf("versions=%d active=%d", len(versions), active)
	}
	if _, _, err = s.ActiveMethodology(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentActivationsLeaveOneActiveVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/activate")

	const versions = 6
	for i := 0; i < versions; i++ {
		publishTestMethodology(t, s, h.ID, "version")
	}
	var wg sync.WaitGroup
	errs := make([]error, versions)
	for i := 0; i < versions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.ActivateMethodology(ctx, h.ID, (i%versions)+1, domain.LocalAddress("tester"), "race")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("activate %d: %v", i, err)
		}
	}

	list, err := s.MethodologyVersions(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, version := range list {
		if version.Active {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active versions = %d", active)
	}
	changes, err := s.MethodologyChanges(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != versions*2 {
		t.Fatalf("changes = %d, want %d", len(changes), versions*2)
	}
}

func TestPublishRejectsInvalidMethodology(t *testing.T) {
	s := testStore(t)
	h := newTestHive(t, s, "acme/invalid")
	manifest, files := methodology.Builtin()
	manifest.DefaultRole = "missing"
	if _, err := s.PublishMethodology(context.Background(), domain.Methodology{HiveID: h.ID, Manifest: manifest}, files); err == nil {
		t.Fatal("invalid methodology published")
	}
	if _, _, err := s.ActiveMethodology(context.Background(), h.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid publish left an active version: %v", err)
	}
}
