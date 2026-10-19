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
type Workspace interface {
	Prepare(ctx context.Context, spec cell.Spec, dir string) error
}

// WorkspaceDir is the single source of truth for a Cell's workspace path.
func WorkspaceDir(dataDir, agentID string) string {
	return filepath.Join(dataDir, "cells", agentID, "workspace")
}

// GitWorkspace clones the repository once and then keeps the Agent's own branch
// checked out. Uncommitted work is never discarded.
type GitWorkspace struct {
	Git    *Git
	Tokens TokenSource
}

func (w *GitWorkspace) git() *Git {
	if w.Git != nil {
		return w.Git
	}
	return &Git{}
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

// Prepare creates the workspace on spec.Branch, or leaves an existing one
// alone when it is already on that branch.
func (w *GitWorkspace) Prepare(ctx context.Context, spec cell.Spec, dir string) error {
	if err := validAgentID(spec.AgentID); err != nil {
		return err
	}
	if err := validRepository(spec.Repository); err != nil {
		return err
	}
	if err := validBranch(spec.Branch); err != nil {
		return err
	}
	if spec.BaseBranch != "" {
		if err := validBranch(spec.BaseBranch); err != nil {
			return err
		}
	}
	git := w.git()

	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return w.update(ctx, git, spec, dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cell: inspect workspace: %w", err)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return errors.New("cell: workspace exists but is not a git repository")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cell: inspect workspace: %w", err)
	}

	auth, err := w.auth(ctx, spec.Repository)
	if err != nil {
		return fmt.Errorf("cell: git credential: %w", err)
	}
	defer auth.Close()

	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return fmt.Errorf("cell: create cell directory: %w", err)
	}
	url := git.URL(spec.Repository)
	if _, err := git.Run(ctx, "", auth.env, "clone", "--no-checkout", "--", url, dir); err != nil {
		return fmt.Errorf("cell: clone %s: %w", spec.Repository, err)
	}

	base, err := baseBranch(ctx, git, dir, spec.BaseBranch)
	if err != nil {
		return err
	}
	switch {
	case hasRef(ctx, git, dir, "refs/remotes/origin/"+spec.Branch):
		if err := checkout(ctx, git, dir, spec.Branch, "origin/"+spec.Branch); err != nil {
			return err
		}
	case hasRef(ctx, git, dir, "refs/remotes/origin/"+base):
		if err := checkout(ctx, git, dir, spec.Branch, "origin/"+base); err != nil {
			return err
		}
	default:
		return fmt.Errorf("cell: base branch %q is not present in the clone", base)
	}
	return nil
}

func (w *GitWorkspace) update(ctx context.Context, git *Git, spec cell.Spec, dir string) error {
	origin, err := git.Run(ctx, dir, nil, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("cell: read workspace origin: %w", err)
	}
	if strings.TrimSpace(string(origin)) != git.URL(spec.Repository) {
		return errors.New("cell: workspace origin does not match the Agent repository")
	}
	current, err := git.Run(ctx, dir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil && strings.TrimSpace(string(current)) == spec.Branch {
		return nil
	}
	status, err := git.Run(ctx, dir, nil, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("cell: inspect workspace status: %w", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		return errors.New("cell: workspace has uncommitted changes on another branch")
	}
	switch {
	case hasRef(ctx, git, dir, "refs/heads/"+spec.Branch):
		// Keep commits made on the local branch; only switch to it.
		if _, err := git.Run(ctx, dir, nil, "checkout", "--quiet", spec.Branch); err != nil {
			return fmt.Errorf("cell: checkout %s: %w", spec.Branch, err)
		}
		return nil
	case hasRef(ctx, git, dir, "refs/remotes/origin/"+spec.Branch):
		return checkout(ctx, git, dir, spec.Branch, "origin/"+spec.Branch)
	default:
		base, err := baseBranch(ctx, git, dir, spec.BaseBranch)
		if err != nil {
			return err
		}
		return checkout(ctx, git, dir, spec.Branch, "origin/"+base)
	}
}

func checkout(ctx context.Context, git *Git, dir, branch, start string) error {
	if _, err := git.Run(ctx, dir, nil, "checkout", "--quiet", "-B", branch, start); err != nil {
		return fmt.Errorf("cell: checkout %s from %s: %w", branch, start, err)
	}
	return nil
}

func hasRef(ctx context.Context, git *Git, dir, ref string) bool {
	_, err := git.Run(ctx, dir, nil, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// baseBranch resolves the branch named by BaseBranch, or the remote default
// branch when it is empty.
func baseBranch(ctx context.Context, git *Git, dir, explicit string) (string, error) {
	if explicit != "" {
		if err := validBranch(explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}
	out, err := git.Run(ctx, dir, nil, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		name := strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/")
		if name != "" && validBranch(name) == nil {
			return name, nil
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if hasRef(ctx, git, dir, "refs/remotes/origin/"+candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("cell: cannot determine the repository default branch")
}
