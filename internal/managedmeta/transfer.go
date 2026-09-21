package managedmeta

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"swarmjet/internal/domain"
)

// maxBundle bounds a managed Meta-Repo transfer. It must stay below the node
// request limit after base64 encoding.
const maxBundle = 16 << 20

// Bundle exports the managed repository as a git bundle so an execution node can
// materialize it locally. The control plane is the source of truth.
func (m *Manager) Bundle(ctx context.Context, projectID string) ([]byte, error) {
	root, err := m.Root(projectID)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil, errors.New("managed Meta-Repo is not initialized")
	}
	f, err := os.CreateTemp(m.DataDir, ".managed-bundle-*")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)
	if _, err = m.git(ctx, root, "bundle", "create", "--quiet", name, "--all"); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > maxBundle {
		return nil, fmt.Errorf("managed Meta-Repo bundle size %d is outside the supported range", len(body))
	}
	return body, nil
}

// Install materializes the managed repository on an execution node from a bundle
// produced by Bundle. It is idempotent and advances the working tree to ref.
func (m *Manager) Install(ctx context.Context, projectID, ref string, bundle []byte) error {
	if !idPattern.MatchString(projectID) || !domain.ValidBranch(ref) {
		return errors.New("invalid managed Meta-Repo identity")
	}
	if len(bundle) == 0 || len(bundle) > maxBundle {
		return errors.New("invalid managed Meta-Repo bundle")
	}
	root, err := m.Root(projectID)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(root), ".managed-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	bundlePath := filepath.Join(staging, "managed.bundle")
	if err = os.WriteFile(bundlePath, bundle, 0600); err != nil {
		return err
	}
	// Validate the bundle before touching an existing repository. list-heads
	// works without a repository, unlike bundle verify.
	heads, err := m.git(ctx, staging, "bundle", "list-heads", bundlePath)
	if err != nil || strings.TrimSpace(heads) == "" {
		return fmt.Errorf("managed Meta-Repo bundle is invalid: %w", err)
	}
	if _, err = os.Stat(filepath.Join(root, ".git")); err == nil {
		// Fetch into a private namespace: git refuses to update a branch that is
		// checked out in the working tree.
		if _, err = m.git(ctx, root, "fetch", bundlePath, "+refs/heads/*:refs/swarmjet/heads/*"); err != nil {
			return err
		}
		if _, err = m.git(ctx, root, "checkout", "-q", "-B", ref, "refs/swarmjet/heads/"+ref); err != nil {
			// Discard any node-local divergence; the control plane owns the repo.
			if _, resetErr := m.git(ctx, root, "reset", "--hard", "refs/swarmjet/heads/"+ref); resetErr != nil {
				return err
			}
			if _, err = m.git(ctx, root, "checkout", "-q", "-B", ref, "refs/swarmjet/heads/"+ref); err != nil {
				return err
			}
		}
		if _, err = m.git(ctx, root, "reset", "--hard"); err != nil {
			return err
		}
		return nil
	}
	if err = os.RemoveAll(root); err != nil {
		return err
	}
	if _, err = m.git(ctx, staging, "clone", "--quiet", bundlePath, root); err != nil {
		return err
	}
	if _, err = m.git(ctx, root, "checkout", "-q", "-B", ref, "refs/heads/"+ref); err != nil {
		return err
	}
	return nil
}
