package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
)

// Workspace prepares the host-side directory that is bind-mounted at
// /workspace in a Cell. It is an interface so the runtime can be tested without
// git; GitWorkspace is the production implementation.
//
// EnsureCloned must only run git when the directory is empty. Once a workspace
// exists, every git operation happens inside the Cell, so a Cell-writable
// .git/config or .gitattributes can never execute a command on the host.
type Workspace interface {
	EnsureCloned(ctx context.Context, spec cell.Spec, dir string) (bool, error)
}

// WorkspaceDir is the single source of truth for a Cell's workspace path.
func WorkspaceDir(dataDir, agentID string) string {
	return filepath.Join(dataDir, "cells", agentID, "workspace")
}

// GitWorkspace clones the repository once, with --no-checkout and no
// submodules. Branch selection happens inside the Cell after the container is
// running.
type GitWorkspace struct {
	Git       GitRunner
	RemoteURL func(repository string) string
	Tokens    TokenSource
}

func (w *GitWorkspace) git() GitRunner {
	if w.Git != nil {
		return w.Git
	}
	return &Git{}
}

func (w *GitWorkspace) remoteURL(repository string) string {
	if w.RemoteURL != nil {
		return w.RemoteURL(repository)
	}
	if g, ok := w.Git.(*Git); ok {
		return g.URL(repository)
	}
	return (&Git{}).URL(repository)
}

func (w *GitWorkspace) auth(ctx context.Context, repository string) (*gitAuth, error) {
	if w.Tokens == nil {
		return &gitAuth{}, nil
	}
	token, err := w.Tokens(ctx, repository)
	if err != nil {
		return nil, err
	}
	return newGitAuth(token)
}

// EnsureCloned clones the repository when dir is empty. If dir already holds a
// repository it returns false without running any git command.
func (w *GitWorkspace) EnsureCloned(ctx context.Context, spec cell.Spec, dir string) (bool, error) {
	if err := validAgentID(spec.AgentID); err != nil {
		return false, err
	}
	if err := validRepository(spec.Repository); err != nil {
		return false, err
	}
	if err := validBranch(spec.Branch); err != nil {
		return false, err
	}
	if spec.BaseBranch != "" {
		if err := validBranch(spec.BaseBranch); err != nil {
			return false, err
		}
	}
	if spec.Revision != "" {
		if err := validRevision(spec.Revision); err != nil {
			return false, err
		}
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("cell: inspect workspace: %w", err)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return false, errors.New("cell: workspace exists but is not a git repository")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("cell: inspect workspace: %w", err)
	}

	auth, err := w.auth(ctx, spec.Repository)
	if err != nil {
		return false, fmt.Errorf("cell: git credential: %w", err)
	}
	defer auth.Close()

	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return false, fmt.Errorf("cell: create cell directory: %w", err)
	}
	git := w.git()
	url := w.remoteURL(spec.Repository)
	if _, err := git.Run(ctx, "", auth.env, "clone", "--no-checkout", "--no-recurse-submodules", "--", url, dir); err != nil {
		return false, fmt.Errorf("cell: clone %s: %w", spec.Repository, err)
	}
	return true, nil
}

// validateWorkspacePath guards deletions so a workspace can only be removed
// below the platform's cells directory.
func validateWorkspacePath(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("cell: refusing to remove a workspace outside the data directory")
	}
	return nil
}
