package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"swarmjet/internal/domain"
	"swarmjet/internal/integration"
	"swarmjet/internal/managedmeta"
	"swarmjet/internal/workspace"
)

// IntegrationRunner runs the task-scoped verification stack on an execution host.
type IntegrationRunner interface {
	Start(context.Context, domain.Workspace, domain.IntegrationConfig) error
	Verify(context.Context, domain.Workspace, domain.IntegrationConfig) ([]domain.Verification, error)
	Stop(context.Context, domain.Workspace, domain.IntegrationConfig) error
}

// Node is the execution environment for tasks pinned to one host. It owns the
// task workspace, agent containers, integration stacks, reports and the
// task-scoped files they produce.
//
// Every method must tolerate retries: AgentBackend.Start adopts an execution
// with the same ID, workspace preparation resumes prior state, and Cleanup and
// Retire are idempotent. Methods that take an env map receive caller-resolved
// secrets for this call only; implementations must never persist or log them.
type Node interface {
	AgentBackend
	ID() string
	// SyncManagedMeta installs the control-plane owned managed Meta-Repo on this
	// execution host before a managed task's workspace is prepared.
	SyncManagedMeta(context.Context, string, string, []byte) error
	PrepareWorkspace(context.Context, string, domain.Project, map[string]string) (domain.Workspace, error)
	InspectWorkspace(context.Context, domain.Workspace, domain.Project, map[string]string) (domain.Workspace, error)
	RefreshTargets(context.Context, domain.Workspace, domain.Project, map[string]string) (domain.Workspace, error)
	ReleaseWorkspace(context.Context, domain.Workspace) error
	StartIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) error
	VerifyIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) ([]domain.Verification, error)
	StopIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) error
	ReadReport(context.Context, string, string) ([]byte, error)
	Cleanup(context.Context, string, domain.Workspace, domain.IntegrationConfig, map[string]string) (bool, error)
	Retire(context.Context, domain.Task) error
}

// WorkspacePublisher publishes already inspected task commits using credentials
// resolved for this call only. Older nodes may not support platform publication.
type WorkspacePublisher interface {
	PushWorkspace(context.Context, domain.Workspace, domain.Project, map[string]string) error
}

// NodeResolver returns the transport for one registered execution node.
type NodeResolver interface {
	Node(context.Context, string) (Node, error)
}

// LocalNode adapts the in-process managers and is the default execution host.
// An empty ID means tasks without an assignment keep using it.
type LocalNode struct {
	StaticAnalysis func(context.Context, AnalysisParams) (AnalysisResult, error)
	NodeID         string
	AgentBackend
	Workspace   *workspace.Manager
	Integration IntegrationRunner
	DataDir     string
	Docker      func(context.Context, []string) ([]byte, error)
}

func (n *LocalNode) ID() string { return n.NodeID }

// SyncManagedMeta installs a managed Meta-Repo bundle on this host. The
// control-plane local backend shares DATA_DIR and already owns the repository.
func (n *LocalNode) SyncManagedMeta(ctx context.Context, projectID, ref string, bundle []byte) error {
	if n.NodeID == "" {
		return nil
	}
	return managedmeta.New(n.DataDir).Install(ctx, projectID, ref, bundle)
}

func (n *LocalNode) workspace(env map[string]string) *workspace.Manager {
	w := *n.Workspace
	w.Env = map[string]string{}
	for k, v := range n.Workspace.Env {
		w.Env[k] = v
	}
	for k, v := range env {
		// A remote execution node owns its credential-helper path and never lets
		// the control plane override it with a host-specific location.
		if n.NodeID != "" && k == "GIT_ASKPASS" {
			continue
		}
		w.Env[k] = v
	}
	return &w
}

func (n *LocalNode) PrepareWorkspace(ctx context.Context, taskID string, p domain.Project, env map[string]string) (domain.Workspace, error) {
	return n.workspace(env).Prepare(ctx, taskID, p)
}
func (n *LocalNode) InspectWorkspace(ctx context.Context, ws domain.Workspace, p domain.Project, env map[string]string) (domain.Workspace, error) {
	return n.workspace(env).Inspect(ctx, ws, p)
}
func (n *LocalNode) RefreshTargets(ctx context.Context, ws domain.Workspace, p domain.Project, env map[string]string) (domain.Workspace, error) {
	return n.workspace(env).RefreshTargets(ctx, ws, p)
}
func (n *LocalNode) PushWorkspace(ctx context.Context, ws domain.Workspace, p domain.Project, env map[string]string) error {
	return n.workspace(env).Push(ctx, ws, p)
}
func (n *LocalNode) ReleaseWorkspace(ctx context.Context, ws domain.Workspace) error {
	return n.Workspace.Release(ctx, ws)
}

func (n *LocalNode) integration(env map[string]string) IntegrationRunner {
	m, ok := n.Integration.(*integration.Manager)
	if !ok || len(env) == 0 {
		return n.Integration
	}
	c := *m
	c.Env = map[string]string{}
	for k, v := range m.Env {
		c.Env[k] = v
	}
	for k, v := range env {
		c.Env[k] = v
	}
	return &c
}
func (n *LocalNode) StartIntegration(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) error {
	return n.integration(env).Start(ctx, ws, cfg)
}
func (n *LocalNode) VerifyIntegration(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) ([]domain.Verification, error) {
	return n.integration(env).Verify(ctx, ws, cfg)
}
func (n *LocalNode) StopIntegration(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) error {
	return n.integration(env).Stop(ctx, ws, cfg)
}

func (n *LocalNode) ReadReport(_ context.Context, taskID, relative string) ([]byte, error) {
	p, e := reportPath(n.DataDir, taskID, relative)
	if e != nil {
		return nil, e
	}
	return os.ReadFile(p)
}

// Cleanup stops the integration stack and releases the workspace once. The
// marker makes a retry after a later failure (for example closing the Issue)
// safe without needing the deleted files.
func (n *LocalNode) Cleanup(ctx context.Context, taskID string, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) (bool, error) {
	marker := filepath.Join(n.DataDir, "runs", taskID, "cleanup-complete")
	if _, e := os.Stat(marker); e == nil {
		return true, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return false, e
	}
	if e := n.StopIntegration(ctx, ws, cfg, env); e != nil {
		return false, e
	}
	if e := n.ReleaseWorkspace(ctx, ws); e != nil {
		return false, e
	}
	if e := os.MkdirAll(filepath.Dir(marker), 0700); e != nil {
		return false, e
	}
	return false, os.WriteFile(marker, []byte("complete"), 0600)
}

func (n *LocalNode) Retire(ctx context.Context, t domain.Task) error {
	return RetireTask(ctx, RetireConfig{DataDir: n.DataDir, Executions: n.AgentBackend, Workspace: n.Workspace, Docker: n.Docker}, t)
}

func reportPath(dataDir, taskID, relative string) (string, error) {
	if !domain.IDPattern.MatchString(taskID) || relative == "" || filepath.IsAbs(relative) || strings.IndexByte(relative, 92) >= 0 {
		return "", errors.New("invalid report path")
	}
	base := filepath.Join(dataDir, "runs", taskID)
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid report path")
	}
	baseResolved, e := filepath.EvalSymlinks(base)
	if e != nil {
		return "", e
	}
	resolved, e := filepath.EvalSymlinks(filepath.Join(baseResolved, clean))
	if e != nil {
		return "", e
	}
	rel, e := filepath.Rel(baseResolved, resolved)
	if e != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("report escapes task directory")
	}
	info, e := os.Stat(resolved)
	if e != nil || !info.Mode().IsRegular() {
		return "", errors.New("report is not a regular file")
	}
	return resolved, nil
}

var _ Node = (*LocalNode)(nil)
