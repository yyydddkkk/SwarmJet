package store

import (
	"context"
	"errors"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"testing"
)

func TestManagedNodeSchedulingSnapshotsAndDrain(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := testProject()
	p.Development.Adapter = "codex"
	p.Review.Adapter = "codex"
	p.Development.Image = ""
	p.Review.Image = ""
	p.Nodes = domain.NodeSelection{Mode: "selected", IDs: []string{"a"}}
	// Save a valid legacy project; task creation resolves the per-node image.
	saved := testProject()
	if _, e := s.SaveProject(ctx, saved); e != nil {
		t.Fatal(e)
	}
	net, e := s.SaveNetwork(ctx, domain.NetworkProfile{ID: "proxy", Name: "proxy", HTTPProxy: "http://proxy:8080"}, "")
	if e != nil {
		t.Fatal(e)
	}
	digest := "registry.test/swarmjet/agent@sha256:" + strings.Repeat("a", 64)
	for _, id := range []string{"a", "b"} {
		if e = s.HeartbeatNode(ctx, domain.Node{ID: id, Address: "http://" + id, Capacity: 2}); e != nil {
			t.Fatal(e)
		}
		cfg, e := s.SaveNodeConfig(ctx, id, domain.NodeConfig{Name: id, Enabled: true, Capacity: 2, Image: digest, Network: domain.NetworkChoice{Mode: "profile", ID: net.ID}})
		if e != nil {
			t.Fatal(e)
		}
		ready := domain.NodeReadiness{ConfigVersion: cfg.Version, Image: digest, Status: "ready", Agents: []string{"codex"}, Network: domain.NetworkVersion{ID: net.ID, Version: net.Version}}
		if e = s.HeartbeatNode(ctx, domain.Node{ID: id, Address: "http://" + id, Capacity: 2, Readiness: ready}); e != nil {
			t.Fatal(e)
		}
	}
	task, e := s.CreateTaskScheduled(ctx, testTask("managed-1", p, 1), true)
	if e != nil {
		t.Fatal(e)
	}
	if task.NodeID != "a" || task.Project.Development.Image != digest || task.Network.Version != 1 {
		t.Fatalf("bad snapshot %+v", task)
	}
	cfg, e := s.NodeConfig(ctx, "a")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Enabled = false
	cfg, e = s.SaveNodeConfig(ctx, "a", cfg)
	if e != nil {
		t.Fatal(e)
	}
	waiting, e := s.CreateTaskScheduled(ctx, testTask("managed-2", p, 2), true)
	if e != nil || waiting.NodeID != "" || waiting.AdmissionStatus != "waiting" {
		t.Fatalf("drained/other project node selected: %+v %v", waiting, e)
	}
	cfg.Archived = true
	if _, e = s.SaveNodeConfig(ctx, "a", cfg); e == nil {
		t.Fatal("archived node with active task")
	}
	got, e := s.Task(ctx, task.ID)
	if e != nil || got.Network != task.Network || got.Project.Development.Image != digest {
		t.Fatalf("task snapshot changed: %v", e)
	}
	net.HTTPProxy = "http://new-proxy:8080"
	if _, e = s.SaveNetwork(ctx, net, ""); e != nil {
		t.Fatal(e)
	}
	p.Nodes = domain.NodeSelection{Mode: "auto"}
	waiting, e = s.CreateTaskScheduled(ctx, testTask("managed-3", p, 3), true)
	if e != nil || waiting.NodeID != "" || waiting.AdmissionStatus != "waiting" {
		t.Fatalf("stale network readiness selected: %+v %v", waiting, e)
	}
}
func TestNetworkVersionConflictAndDirect(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := domain.NetworkProfile{ID: "n", Name: "first"}
	v, e := s.SaveNetwork(ctx, p, "encrypted")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveNetwork(ctx, p, ""); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.SetSetting(ctx, "default_network", v.ID); e != nil {
		t.Fatal(e)
	}
	ref, e := s.ResolveNetwork(ctx, domain.NetworkChoice{Mode: "default"})
	if e != nil || ref.Version != 1 {
		t.Fatal(ref, e)
	}
	ref, e = s.ResolveNetwork(ctx, domain.NetworkChoice{Mode: "direct"})
	if e != nil || ref.ID != "" {
		t.Fatal(ref, e)
	}
}

func TestDeleteNetworkRemovesVersionsAndResetsDefault(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, e := s.SaveNetwork(ctx, domain.NetworkProfile{ID: "unused", Name: "unused"}, "encrypted")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetSetting(ctx, "default_network", p.ID); e != nil {
		t.Fatal(e)
	}
	newer, e := s.SaveNetwork(ctx, p, "encrypted")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteNetwork(ctx, p.ID, p.Version); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale delete: %v", e)
	}
	if e = s.DeleteNetwork(ctx, p.ID, newer.Version); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.NetworkProfile(ctx, domain.NetworkVersion{ID: p.ID, Version: p.Version}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("old version remains: %v", e)
	}
	var defaultID string
	if e = s.Setting(ctx, "default_network", &defaultID); e != nil || defaultID != "" {
		t.Fatalf("default not reset: %q, %v", defaultID, e)
	}
}

func TestBuildNetworkChoicePrioritySnapshotAndDeletion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	platform, e := s.SaveNetwork(ctx, domain.NetworkProfile{ID: "platform", Name: "platform", HTTPProxy: "http://platform:8080"}, "encrypted-platform")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetSetting(ctx, "default_network", platform.ID); e != nil {
		t.Fatal(e)
	}
	choice, e := s.BuildkitNetworkChoice(ctx)
	if e != nil || choice.Mode != "default" {
		t.Fatalf("legacy build default: %+v %v", choice, e)
	}
	ref, e := s.ResolveBuildNetwork(ctx, domain.NetworkChoice{Mode: "default"})
	if e != nil || ref.ID != platform.ID || ref.Version != platform.Version {
		t.Fatalf("inherited platform default: %+v %v", ref, e)
	}
	build, e := s.SaveNetwork(ctx, domain.NetworkProfile{ID: "build", Name: "build", HTTPProxy: "http://build:8080"}, "encrypted-build")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetBuildkitNetworkChoice(ctx, domain.NetworkChoice{Mode: "profile", ID: build.ID}); e != nil {
		t.Fatal(e)
	}
	ref, e = s.ResolveBuildNetwork(ctx, domain.NetworkChoice{})
	if e != nil || ref.ID != build.ID || ref.Version != 1 {
		t.Fatalf("build default: %+v %v", ref, e)
	}
	oldRef := ref
	explicit, e := s.ResolveBuildNetwork(ctx, domain.NetworkChoice{Mode: "profile", ID: platform.ID})
	if e != nil || explicit.ID != platform.ID || explicit.Version != 1 {
		t.Fatalf("explicit profile priority: %+v %v", explicit, e)
	}
	direct, e := s.ResolveBuildNetwork(ctx, domain.NetworkChoice{Mode: "direct"})
	if e != nil || direct.ID != "" {
		t.Fatalf("explicit direct priority: %+v %v", direct, e)
	}
	build.HTTPProxy = "http://changed:8080"
	build, e = s.SaveNetwork(ctx, build, "encrypted-build")
	if e != nil {
		t.Fatal(e)
	}
	current, e := s.ResolveBuildNetwork(ctx, domain.NetworkChoice{Mode: "default"})
	if e != nil || current.Version != 2 || oldRef.Version != 1 {
		t.Fatalf("profile version snapshot: old=%+v current=%+v %v", oldRef, current, e)
	}
	oldProfile, oldSecret, e := s.NetworkProfile(ctx, oldRef)
	if e != nil || oldProfile.HTTPProxy != "http://build:8080" || oldSecret != "encrypted-build" {
		t.Fatalf("old version changed: %+v %q %v", oldProfile, oldSecret, e)
	}
	if e = s.DeleteNetwork(ctx, build.ID, build.Version); e != nil {
		t.Fatal(e)
	}
	choice, e = s.BuildkitNetworkChoice(ctx)
	if e != nil || choice.Mode != "default" || choice.ID != "" {
		t.Fatalf("deleted build default: %+v %v", choice, e)
	}
	ref, e = s.ResolveBuildNetwork(ctx, domain.NetworkChoice{Mode: "default"})
	if e != nil || ref.ID != platform.ID {
		t.Fatalf("inherit after deletion: %+v %v", ref, e)
	}
	if e = s.SetBuildkitNetworkChoice(ctx, domain.NetworkChoice{Mode: "direct"}); e != nil {
		t.Fatal(e)
	}
	ref, e = s.ResolveBuildNetwork(ctx, domain.NetworkChoice{})
	if e != nil || ref.ID != "" {
		t.Fatalf("direct build default: %+v %v", ref, e)
	}
	if e = s.SetBuildkitNetworkChoice(ctx, domain.NetworkChoice{Mode: "profile", ID: build.ID}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("deleted profile selected: %v", e)
	}
}

func TestNodeSupportsDifferentImagesForTaskRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := testProject()
	if _, e := s.SaveProject(ctx, p); e != nil {
		t.Fatal(e)
	}
	one := "registry.test/agent@sha256:" + strings.Repeat("1", 64)
	two := "registry.test/agent@sha256:" + strings.Repeat("2", 64)
	p.Development.Image = one
	p.Development.Adapter = "codex"
	p.Review.Image = two
	p.Review.Adapter = "codex"
	if e := s.HeartbeatNode(ctx, domain.Node{ID: "multi", Address: "http://multi", Capacity: 2}); e != nil {
		t.Fatal(e)
	}
	cfg, e := s.SaveNodeConfig(ctx, "multi", domain.NodeConfig{Enabled: true, Capacity: 2, Image: one, Images: []string{two}, Network: domain.NetworkChoice{Mode: "direct"}})
	if e != nil {
		t.Fatal(e)
	}
	ready := domain.NodeReadiness{Status: "ready", ConfigVersion: cfg.Version, Image: one, Images: []domain.ReadyImage{{Reference: one, Status: "ready", Agents: []string{"codex"}}, {Reference: two, Status: "ready", Agents: []string{"codex"}}}}
	if e = s.HeartbeatNode(ctx, domain.Node{ID: "multi", Address: "http://multi", Capacity: 2, Readiness: ready}); e != nil {
		t.Fatal(e)
	}
	task, e := s.CreateTaskScheduled(ctx, testTask("multi-image", p, 1), true)
	if e != nil {
		t.Fatal(e)
	}
	if task.Project.Development.Image != one || task.Project.Review.Image != two || task.Project.Development.DockerNetwork != "sj-node-multi" {
		t.Fatal(task.Project)
	}
}
