package managedmeta

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"swarmjet/internal/domain"
)

// maxBundle bounds a managed Meta-Repo transfer. It must stay below the node
// request limit after base64 encoding.
const maxBundle = 16 << 20

// Lock serializes managed Meta-Repo installation and reads for one project on
// one execution host. Every Manager over the same DataDir shares the same file
// lock, so concurrent tasks assigned to the same node cannot race while the
// repository is created or replaced.
func (m *Manager) Lock(ctx context.Context, projectID string) (func(), error) {
	if !idPattern.MatchString(projectID) {
		return nil, errors.New("unsafe project id")
	}
	dir := filepath.Join(m.DataDir, "managed-meta", projectID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

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
// produced by Bundle. It is idempotent and advances the working tree to ref. The
// whole operation holds the project lock, and a first install is published with
// an atomic rename so a concurrent reader never sees a partial repository.
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
	parent := filepath.Dir(root)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	unlock, err := m.Lock(ctx, projectID)
	if err != nil {
		return err
	}
	defer unlock()

	staging, err := os.MkdirTemp(parent, ".managed-install-")
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
	// First install: clone into a sibling directory, then publish atomically so
	// readers under the lock never observe a partially populated repository.
	publish, err := os.MkdirTemp(parent, ".managed-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(publish)
	cloneDir := filepath.Join(publish, "work")
	if _, err = m.git(ctx, staging, "clone", "--quiet", bundlePath, cloneDir); err != nil {
		return err
	}
	if _, err = m.git(ctx, cloneDir, "checkout", "-q", "-B", ref, "refs/heads/"+ref); err != nil {
		return err
	}
	if err = os.RemoveAll(root); err != nil {
		return err
	}
	return os.Rename(cloneDir, root)
}
