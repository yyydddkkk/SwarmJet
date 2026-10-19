package docker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
)

const (
	wipAuthorName  = "SwarmJet Forager"
	wipAuthorEmail = "forager@swarmjet.invalid"
)

// PullRequests creates or updates a pull request. *github.Client implements it.
type PullRequests interface {
	CreateOrUpdatePR(ctx context.Context, id int64, source, target, title, body string) (forge.MergeRequest, error)
}

// RepositoryResolver maps "owner/name" to the forge's numeric repository ID.
type RepositoryResolver func(ctx context.Context, fullName string) (int64, error)

// Publisher moves an Agent's work from its host workspace to GitHub. It runs on
// the platform side with platform credentials; a Cell never holds Git
// credentials.
type Publisher struct {
	DataDir    string
	Git        *Git
	Tokens     TokenSource
	Pulls      PullRequests
	Repository RepositoryResolver
}

var _ cell.Publisher = (*Publisher)(nil)

func (p *Publisher) git() *Git {
	if p.Git != nil {
		return p.Git
	}
	return &Git{}
}

func (p *Publisher) run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := p.git().Run(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Publish commits uncommitted changes as a WIP commit, pushes exactly
// Spec.Branch when it is ahead of the base, and creates or updates the pull
// request. It returns 0 when there is nothing to publish.
func (p *Publisher) Publish(ctx context.Context, spec cell.Spec, title, body string) (int64, error) {
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

	var token string
	if p.Tokens != nil {
		var err error
		token, err = p.Tokens(ctx, spec.Repository)
		if err != nil {
			return 0, fmt.Errorf("cell: git credential: %w", err)
		}
	}
	auth, err := newGitAuth(token)
	if err != nil {
		return 0, err
	}
	defer auth.Close()
	secrets := []string{token}

	workspace := WorkspaceDir(p.DataDir, spec.AgentID)
	git := p.git()

	origin, err := p.run(ctx, workspace, nil, "remote", "get-url", "origin")
	if err != nil {
		return 0, fmt.Errorf("cell: read workspace origin: %s", redact(err.Error(), secrets...))
	}
	if origin != git.URL(spec.Repository) {
		return 0, errors.New("cell: workspace origin does not match the Agent repository")
	}

	head, err := p.run(ctx, workspace, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return 0, fmt.Errorf("cell: workspace is not on a branch: %s", redact(err.Error(), secrets...))
	}
	if head != spec.Branch {
		return 0, fmt.Errorf("cell: workspace is on %q, not the Agent branch %q", head, spec.Branch)
	}
	base, err := baseBranch(ctx, git, workspace, spec.BaseBranch)
	if err != nil {
		return 0, err
	}
	if base == spec.Branch {
		return 0, fmt.Errorf("cell: refusing to publish the base branch %q", base)
	}

	status, err := p.run(ctx, workspace, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return 0, fmt.Errorf("cell: inspect workspace: %s", redact(err.Error(), secrets...))
	}
	if status != "" {
		if _, err := p.run(ctx, workspace, nil, "add", "-A"); err != nil {
			return 0, fmt.Errorf("cell: stage workspace: %s", redact(err.Error(), secrets...))
		}
		if _, err := p.run(ctx, workspace, nil,
			"-c", "user.name="+wipAuthorName,
			"-c", "user.email="+wipAuthorEmail,
			"commit", "--no-verify", "-m", "wip: forager "+spec.AgentID,
		); err != nil {
			return 0, fmt.Errorf("cell: commit workspace: %s", redact(err.Error(), secrets...))
		}
	}

	if _, err := p.run(ctx, workspace, auth.env, "fetch", "--quiet", "origin", base); err != nil {
		return 0, fmt.Errorf("cell: fetch %s: %s", base, redact(err.Error(), secrets...))
	}
	aheadText, err := p.run(ctx, workspace, nil, "rev-list", "--count", "origin/"+base+"..HEAD")
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

	// Push exactly one refspec. Never --all, --mirror or another branch: this is
	// the first line of the Guard branch-ownership rule.
	refspec := "HEAD:refs/heads/" + spec.Branch
	if _, err := p.run(ctx, workspace, auth.env, "push", "--porcelain", "--", git.URL(spec.Repository), refspec); err != nil {
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
