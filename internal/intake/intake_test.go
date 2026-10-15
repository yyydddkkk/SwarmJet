package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type captureScheduler struct {
	task        domain.Task
	requireNode bool
	calls       int
}

func (s *captureScheduler) CreateTaskScheduled(_ context.Context, task domain.Task, requireNode bool) (domain.Task, error) {
	s.task, s.requireNode = task, requireNode
	s.calls++
	return task, nil
}

type ready bool

func (r ready) ReadyForTasks(context.Context, string) bool { return bool(r) }

func TestAdmissionReadiness(t *testing.T) {
	ctx := context.Background()
	p := domain.Project{Enabled: true, MetaMode: domain.MetaModeManaged}
	if ReadyForTasks(ctx, p, nil) || ReadyForTasks(ctx, p, ready(false)) || !ReadyForTasks(ctx, p, ready(true)) {
		t.Fatal("managed Meta readiness was not enforced")
	}
	p.MetaMode = ""
	if !ReadyForTasks(ctx, p, nil) {
		t.Fatal("imported Meta unexpectedly requires initialization")
	}
	p.MetaMode = domain.MetaModeDirect
	p.Enabled = true
	if !ReadyForTasks(ctx, p, nil) {
		t.Fatal("direct single-repo unexpectedly requires initialization")
	}
	p.Enabled = false
	if ReadyForTasks(ctx, p, ready(true)) {
		t.Fatal("disabled project accepted")
	}
}

func TestScheduleFreezesAgentImageAndRoundIntent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "image-manager"), 0700); err != nil {
		t.Fatal(err)
	}
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.WriteFile(filepath.Join(dir, "image-manager", "active.json"), []byte(`{"imageId":"`+digest+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := domain.Project{ID: "p", Enabled: true, Development: domain.AgentConfig{Adapter: "opencode", Model: "old", Image: "old-image"}, Review: domain.AgentConfig{Adapter: "opencode", Model: "review", Image: "old-image"}}
	issue := domain.Issue{Provider: domain.ProviderGitHub, ProjectID: 7, IID: 3, Labels: []string{"autonomous", "agent:codex"}}
	deps := []domain.IssueRef{{Provider: domain.ProviderGitHub, ProjectID: 7, IID: 2}}
	scheduler := &captureScheduler{}
	task, err := Schedule(ctx, scheduler, p, issue, []string{"codex"}, Options{DataDir: dir, Dependencies: deps, ClaimRunCommands: true})
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.calls != 1 || scheduler.requireNode || task.ID == "" || task.Stage != "queued" || task.Executions == nil || task.TriggerNoteIDs == nil || len(task.TriggerNoteIDs) != 0 || len(task.Dependencies) != 1 || task.Dependencies[0] != deps[0] {
		t.Fatalf("incorrect Task snapshot: %+v", task)
	}
	if task.Project.Development.Adapter != "codex" || task.Project.Review.Adapter != "codex" || task.Project.Development.Model != "" || !task.Project.Development.UseCLIDefaults || task.Project.Development.Image != digest || task.Project.Review.Image != digest {
		t.Fatalf("Agent/image choices not frozen: %+v", task.Project)
	}
	if p.Development.Adapter != "opencode" || p.Development.Image != "old-image" {
		t.Fatal("source Project changed")
	}
	scheduler = &captureScheduler{}
	task, err = Schedule(ctx, scheduler, p, issue, []string{"codex"}, Options{DataDir: dir, RequireNode: true})
	if err != nil || !scheduler.requireNode || task.TriggerNoteIDs != nil || task.Project.Development.Image != "old-image" {
		t.Fatalf("Node mode must preserve configured image and first-round intent: %+v %v", task, err)
	}
}

func TestScheduleRejectsAgentBeforeScheduling(t *testing.T) {
	p := domain.Project{Development: domain.AgentConfig{Adapter: "opencode"}}
	scheduler := &captureScheduler{}
	_, err := Schedule(context.Background(), scheduler, p, domain.Issue{Labels: []string{"agent:codex"}}, []string{"opencode"}, Options{})
	var selection *AgentSelectionError
	if !errors.As(err, &selection) || !strings.Contains(err.Error(), "尚未") || scheduler.calls != 0 {
		t.Fatalf("rejected Agent reached scheduler: %v calls=%d", err, scheduler.calls)
	}
}
