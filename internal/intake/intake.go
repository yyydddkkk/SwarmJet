// Package intake turns an eligible Issue into a scheduled Task with a fixed
// Project snapshot. Callers retain ownership of Issue discovery and feedback.
package intake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/imagebuild"
	"github.com/yyydddkkk/SwarmJet/internal/observability"
)

// Readiness is supplied by the persisted managed Meta initialization.
type Readiness interface {
	ReadyForTasks(context.Context, string) bool
}

// ReadyForTasks is the common admission check used before reading Issues.
func ReadyForTasks(ctx context.Context, p domain.Project, init Readiness) bool {
	return p.Enabled && (!p.ManagedMeta() || init != nil && init.ReadyForTasks(ctx, p.ID))
}

// Scheduler keeps node selection, round assignment, command claiming, and
// outbox creation in the store transaction.
type Scheduler interface {
	CreateTaskScheduled(context.Context, domain.Task, bool) (domain.Task, error)
}

type KubernetesScheduler interface {
	CreateKubernetesTask(context.Context, domain.Task, string, domain.RunnerResources) (domain.Task, error)
}

type Options struct {
	DataDir      string
	RequireNode  bool
	Dependencies []domain.IssueRef
	// ClaimRunCommands distinguishes a new round from a first Task. A non-nil
	// TriggerNoteIDs asks the store to claim commands atomically with scheduling.
	ClaimRunCommands bool
	KubernetesPool   string
	RunnerResources  domain.RunnerResources
	RunnerImage      string
}

type AgentSelectionError struct{ Err error }

func (e *AgentSelectionError) Error() string { return e.Err.Error() }
func (e *AgentSelectionError) Unwrap() error { return e.Err }

// Schedule freezes Agent and legacy image choices into the Task snapshot, then
// delegates the atomic scheduling decision to the store.
func Schedule(ctx context.Context, scheduler Scheduler, p domain.Project, issue domain.Issue, enabled []string, options Options) (domain.Task, error) {
	project, err := domain.ResolveIssueAgent(p, issue, enabled)
	if err != nil {
		return domain.Task{}, &AgentSelectionError{Err: err}
	}
	if options.KubernetesPool == "" && !options.RequireNode && project.Nodes.Mode == "" {
		project, err = imagebuild.ApplyToProject(options.DataDir, project)
		if err != nil {
			return domain.Task{}, err
		}
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return domain.Task{}, err
	}
	now := time.Now().UTC()
	t := domain.Task{
		ID: hex.EncodeToString(id), Project: project, Issue: issue,
		TraceParent:  observability.TraceParent(ctx),
		Dependencies: options.Dependencies, Stage: "queued",
		Executions: map[string]domain.Execution{}, Verification: []domain.Verification{},
		Assumptions: []string{}, Notes: []string{}, CreatedAt: now, UpdatedAt: now,
	}
	if options.ClaimRunCommands {
		t.TriggerNoteIDs = []int64{}
	}
	if options.KubernetesPool != "" {
		kubernetes, ok := scheduler.(KubernetesScheduler)
		if !ok {
			return domain.Task{}, fmt.Errorf("scheduler does not support Kubernetes admission")
		}
		t.RunnerImage = options.RunnerImage
		return kubernetes.CreateKubernetesTask(ctx, t, options.KubernetesPool, options.RunnerResources)
	}
	return scheduler.CreateTaskScheduled(ctx, t, options.RequireNode)
}
