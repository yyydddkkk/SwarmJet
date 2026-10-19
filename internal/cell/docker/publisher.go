package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
)

const (
	wipAuthorName  = "SwarmJet Forager"
	wipAuthorEmail = "forager@swarmjet.invalid"

	// maxBundleBytes bounds how much untrusted bundle data the platform reads
	// from a Cell workspace.
	maxBundleBytes = 2 << 30

	defaultPublishTimeout = 5 * time.Minute
)

// PullRequests creates or updates a pull request. *github.Client implements it.
type PullRequests interface {
	CreateOrUpdatePR(ctx context.Context, id int64, source, target, title, body string) (forge.MergeRequest, error)
}

// RepositoryResolver maps "owner/name" to the forge's numeric repository ID.
type RepositoryResolver func(ctx context.Context, fullName string) (int64, error)

// Publisher moves an Agent's work from its Cell to GitHub.
//
// Git runs in two separated security domains:
//
//   - Inside the Cell: staging, the WIP commit and `git bundle create`. The Cell
//     may have arbitrary repository config, but that config executes only in
//     the container.
//   - On the host: a platform-owned bare mirror receives the bundle and pushes
//     the Agent branch with platform credentials. The host never runs git
//     against the Cell-writable workspace.
type Publisher struct {
	DataDir string
	// Git runs host-side commands. It is used only for the mirror and bundle.
	Git GitRunner
	// RemoteURL maps a repository to the clone/push URL. It defaults to the
	// GitHub HTTPS URL of Git when Git is a *Git.
	RemoteURL  func(repository string) string
	Tokens     TokenSource
	Pulls      PullRequests
	Repository RepositoryResolver
	// Cell runs commands inside the Agent's container.
	Cell CellRunner
	// CommandTimeout bounds one Publish call; default 5 minutes.
	CommandTimeout time.Duration
}

var _ cell.Publisher = (*Publisher)(nil)

func (p *Publisher) git() GitRunner {
	if p.Git != nil {
		return p.Git
	}
	return &Git{}
}

func (p *Publisher) cellRunner() CellRunner {
	if p.Cell != nil {
		return p.Cell
	}
	return &DockerCellRunner{}
}

func (p *Publisher) remoteURL(repository string) string {
	if p.RemoteURL != nil {
		return p.RemoteURL(repository)
	}
	if git, ok := p.Git.(*Git); ok {
		return git.URL(repository)
	}
	return (&Git{}).URL(repository)
}

func (p *Publisher) timeout() time.Duration {
	if p.CommandTimeout > 0 {
		return p.CommandTimeout
	}
	return defaultPublishTimeout
}

func (p *Publisher) run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := p.git().Run(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Publish stages the Cell's changes, commits a WIP commit when needed, bundles
// exactly the Agent branch, imports the bundle into the platform mirror, checks
// the base ancestry, and pushes the mirror branch to the remote.
func (p *Publisher) Publish(ctx context.Context, spec cell.Spec, title, body string) (int64, error) {
	if spec.ReadOnly {
		return 0, errors.New("cell: refusing to publish a read-only Cell")
	}
	if err := validAgentID(spec.AgentID); err != nil {
		return 0, err
	}
	if err := validRepository(spec.Repository); err != nil {
		return 0, err
	}
	if err := validBranch(spec.Branch); err != nil {
		return 0, err
	}
	if spec.BaseBranch != "" {
		if err := validBranch(spec.BaseBranch); err != nil {
			return 0, err
		}
	}
	secrets := specSecrets(spec.Env, spec.MCPToken, spec.LLMToken)

	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	var token string
	if p.Tokens != nil {
		value, err := p.Tokens(ctx, spec.Repository)
		if err != nil {
			return 0, fmt.Errorf("cell: git credential: %w", err)
		}
		token = value
	}
	auth, err := newGitAuth(token)
	if err != nil {
		return 0, err
	}
	defer auth.Close()
	secrets = append(secrets, token)

	cell := &cellGit{runner: p.cellRunner(), container: containerName(spec.AgentID)}
	workspace := WorkspaceDir(p.DataDir, spec.AgentID)
	mirror := filepath.Join(p.DataDir, "cells", spec.AgentID, "mirror.git")

	if err := p.ensureMirror(ctx, mirror, secrets); err != nil {
		return 0, err
	}
	base, baseSHA, err := p.resolveBase(ctx, mirror, spec, auth.env, secrets)
	if err != nil {
		return 0, err
	}
	if base == spec.Branch {
		return 0, fmt.Errorf("cell: refusing to publish the base branch %q", base)
	}
	if err := p.stageAndCommit(ctx, cell, spec, secrets); err != nil {
		return 0, err
	}
	if err := p.createBundle(ctx, cell, spec, secrets); err != nil {
		return 0, err
	}
	bundle, cleanup, err := copyBundle(workspace, filepath.Dir(mirror), secrets)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	if err := p.fetchBundle(ctx, mirror, bundle, spec.Branch, secrets); err != nil {
		return 0, err
	}
	if err := p.verifyAncestry(ctx, mirror, baseSHA, spec.Branch, secrets); err != nil {
		return 0, err
	}
	aheadText, err := p.run(ctx, mirror, nil, "rev-list", "--count", baseSHA+"..refs/heads/"+spec.Branch)
	if err != nil {
		return 0, fmt.Errorf("cell: compare with base: %s", redact(err.Error(), secrets...))
	}
	ahead, err := strconv.Atoi(aheadText)
	if err != nil {
		return 0, fmt.Errorf("cell: invalid commit count %q", aheadText)
	}
	if ahead == 0 {
		return 0, nil
	}

	// Push exactly one refspec from the platform mirror. Never --all, --mirror
	// or another branch: this is the first line of the Guard branch-ownership
	// rule.
	refspec := "refs/heads/" + spec.Branch + ":refs/heads/" + spec.Branch
	if _, err := p.run(ctx, mirror, auth.env, "push", "--porcelain", "--", p.remoteURL(spec.Repository), refspec); err != nil {
		return 0, fmt.Errorf("cell: push %s: %s", spec.Branch, redact(err.Error(), secrets...))
	}

	if p.Pulls == nil {
		return 0, errors.New("cell: no pull request client configured")
	}
	if p.Repository == nil {
		return 0, errors.New("cell: no repository resolver configured")
	}
	repoID, err := p.Repository(ctx, spec.Repository)
	if err != nil {
		return 0, fmt.Errorf("cell: resolve repository: %w", err)
	}
	if title == "" {
		title = "Forager " + spec.AgentID
	}
	mr, err := p.Pulls.CreateOrUpdatePR(ctx, repoID, spec.Branch, base, title, body)
	if err != nil {
		return 0, fmt.Errorf("cell: create pull request: %w", err)
	}
	return mr.IID, nil
}

func (p *Publisher) stageAndCommit(ctx context.Context, cellGit *cellGit, spec cell.Spec, secrets []string) error {
	head, err := cellGit.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("cell: inspect branch: %s", redact(err.Error(), secrets...))
	}
	if head.ExitCode != 0 || strings.TrimSpace(string(head.Stdout)) != spec.Branch {
		return fmt.Errorf("cell: workspace is on %q, not the Agent branch %q", redact(strings.TrimSpace(string(head.Stdout)), secrets...), spec.Branch)
	}
	res, err := cellGit.git(ctx, "add", "-A", "--", ".", ":(exclude).swarmjet")
	if err != nil {
		return fmt.Errorf("cell: stage workspace: %s", redact(err.Error(), secrets...))
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cell: stage workspace: %s", redact(strings.TrimSpace(string(res.Stderr)), secrets...))
	}
	staged, err := cellGit.git(ctx, "diff", "--cached", "--quiet")
	if err != nil {
		return fmt.Errorf("cell: inspect staged changes: %s", redact(err.Error(), secrets...))
	}
	switch staged.ExitCode {
	case 0:
		return nil
	case 1:
		commit, err := cellGit.git(ctx,
			"-c", "user.name="+wipAuthorName,
			"-c", "user.email="+wipAuthorEmail,
			"commit", "--no-verify", "-m", "wip: forager "+spec.AgentID,
		)
		if err != nil {
			return fmt.Errorf("cell: commit workspace: %s", redact(err.Error(), secrets...))
		}
		if commit.ExitCode != 0 {
			return fmt.Errorf("cell: commit workspace: %s", redact(strings.TrimSpace(string(commit.Stderr)), secrets...))
		}
		return nil
	default:
		return fmt.Errorf("cell: inspect staged changes: %s", redact(strings.TrimSpace(string(staged.Stderr)), secrets...))
	}
}

func (p *Publisher) createBundle(ctx context.Context, cellGit *cellGit, spec cell.Spec, secrets []string) error {
	if res, err := cellGit.run(ctx, "mkdir", "-p", cellBundleDir); err != nil {
		return fmt.Errorf("cell: create bundle directory: %s", redact(err.Error(), secrets...))
	} else if res.ExitCode != 0 {
		return fmt.Errorf("cell: create bundle directory: %s", redact(strings.TrimSpace(string(res.Stderr)), secrets...))
	}
	res, err := cellGit.git(ctx, "bundle", "create", cellBundlePath, "refs/heads/"+spec.Branch)
	if err != nil {
		return fmt.Errorf("cell: create bundle: %s", redact(err.Error(), secrets...))
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cell: create bundle: %s", redact(strings.TrimSpace(string(res.Stderr)), secrets...))
	}
	return nil
}

func (p *Publisher) ensureMirror(ctx context.Context, mirror string, secrets []string) error {
	if info, err := os.Lstat(mirror); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("cell: mirror is not a platform-owned directory")
		}
		out, err := p.run(ctx, mirror, nil, "rev-parse", "--is-bare-repository")
		if err != nil {
			return fmt.Errorf("cell: inspect mirror: %s", redact(err.Error(), secrets...))
		}
		if out != "true" {
			return errors.New("cell: mirror is not a bare repository")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cell: inspect mirror: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0700); err != nil {
		return fmt.Errorf("cell: create mirror directory: %w", err)
	}
	if _, err := p.run(ctx, filepath.Dir(mirror), nil, "init", "--bare", "--quiet", filepath.Base(mirror)); err != nil {
		return fmt.Errorf("cell: create mirror: %s", redact(err.Error(), secrets...))
	}
	return nil
}

// copyBundle moves the Cell-owned bundle to a platform-owned temporary file.
// It refuses symlinks and non-regular files, so a Cell cannot point the host at
// another path through the workspace.
func copyBundle(workspace, destDir string, secrets []string) (string, func(), error) {
	src := filepath.Join(workspace, ".swarmjet", cellBundleName)
	info, err := os.Lstat(src)
	if err != nil {
		return "", nil, fmt.Errorf("cell: read bundle: %s", redact(err.Error(), secrets...))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, errors.New("cell: publish bundle must be a regular file")
	}
	if info.Size() > maxBundleBytes {
		return "", nil, errors.New("cell: publish bundle is too large")
	}
	file, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", nil, fmt.Errorf("cell: read bundle: %s", redact(err.Error(), secrets...))
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("cell: inspect bundle: %s", redact(err.Error(), secrets...))
	}
	if !stat.Mode().IsRegular() || stat.Size() > maxBundleBytes {
		return "", nil, errors.New("cell: publish bundle must be a regular file within the size limit")
	}

	tmp, err := os.CreateTemp(destDir, "publish-*.bundle")
	if err != nil {
		return "", nil, fmt.Errorf("cell: create bundle copy: %w", err)
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	n, copyErr := io.CopyN(tmp, file, maxBundleBytes+1)
	closeErr := tmp.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		cleanup()
		return "", nil, fmt.Errorf("cell: copy bundle: %s", redact(copyErr.Error(), secrets...))
	}
	if n > maxBundleBytes {
		cleanup()
		return "", nil, errors.New("cell: publish bundle is too large")
	}
	if closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("cell: close bundle copy: %w", closeErr)
	}
	// Best effort: the next Publish overwrites it, and git add excludes the
	// directory in any case.
	_ = os.Remove(src)
	return name, cleanup, nil
}

func (p *Publisher) resolveBase(ctx context.Context, mirror string, spec cell.Spec, authEnv, secrets []string) (string, string, error) {
	url := p.remoteURL(spec.Repository)
	base := spec.BaseBranch
	if base == "" {
		var err error
		base, err = p.remoteDefaultBranch(ctx, mirror, url, authEnv, secrets)
		if err != nil {
			return "", "", err
		}
	}
	if err := validBranch(base); err != nil {
		return "", "", err
	}
	if _, err := p.run(ctx, mirror, authEnv, "fetch", "--no-tags", "--no-recurse-submodules", "--", url,
		"+refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
		return "", "", fmt.Errorf("cell: fetch base %s: %s", base, redact(err.Error(), secrets...))
	}
	sha, err := p.run(ctx, mirror, nil, "rev-parse", "--verify", "refs/remotes/origin/"+base+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("cell: resolve base %s: %s", base, redact(err.Error(), secrets...))
	}
	return base, sha, nil
}

func (p *Publisher) remoteDefaultBranch(ctx context.Context, mirror, url string, authEnv, secrets []string) (string, error) {
	// Run in the platform-owned bare mirror, never in a directory that could
	// belong to an unrelated repository with attacker-controlled config.
	out, err := p.run(ctx, mirror, authEnv, "ls-remote", "--symref", "--", url, "HEAD")
	if err != nil {
		return "", fmt.Errorf("cell: resolve remote default branch: %s", redact(err.Error(), secrets...))
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "ref:" && strings.HasPrefix(fields[1], "refs/heads/") {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", errors.New("cell: cannot determine the remote default branch")
}

func (p *Publisher) fetchBundle(ctx context.Context, mirror, bundle, branch string, secrets []string) error {
	branchRef := "refs/heads/" + branch
	out, err := p.run(ctx, mirror, nil, "bundle", "list-heads", "--", bundle)
	if err != nil {
		return fmt.Errorf("cell: inspect bundle refs: %s", redact(err.Error(), secrets...))
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "-") {
			return errors.New("cell: bundle contains prerequisites; refusing to fetch")
		}
		if len(fields) != 2 {
			return errors.New("cell: bundle has an unexpected ref line")
		}
		if fields[1] != branchRef {
			return fmt.Errorf("cell: bundle contains unexpected ref %q", fields[1])
		}
		if found {
			return fmt.Errorf("cell: bundle lists %s more than once", branchRef)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("cell: bundle does not contain %s", branchRef)
	}
	if _, err := p.run(ctx, mirror, nil, "fetch", "--no-tags", "--no-recurse-submodules", "--", bundle,
		"+"+branchRef+":"+branchRef); err != nil {
		return fmt.Errorf("cell: fetch bundle: %s", redact(err.Error(), secrets...))
	}
	return nil
}

func (p *Publisher) verifyAncestry(ctx context.Context, mirror, baseSHA, branch string, secrets []string) error {
	if _, err := p.run(ctx, mirror, nil, "cat-file", "-e", baseSHA+"^{commit}"); err != nil {
		return fmt.Errorf("cell: base commit is not in the bundle: %s", redact(err.Error(), secrets...))
	}
	mergeBase, err := p.run(ctx, mirror, nil, "merge-base", baseSHA, "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("cell: base is not an ancestor of %s: %s", branch, redact(err.Error(), secrets...))
	}
	if mergeBase != baseSHA {
		return fmt.Errorf("cell: base is not an ancestor of %s", branch)
	}
	return nil
}
