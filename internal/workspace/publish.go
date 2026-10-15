package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Push publishes the inspected commits without allowing Git to resolve a moving
// local branch. Retrying the same SHA is harmless; divergent remote branches are
// rejected by the normal fast-forward check. Credentials exist only on Manager.Env.
func (m *Manager) Push(ctx context.Context, ws domain.Workspace, p domain.Project) error {
	s, err := m.load(ws.TaskID)
	if err != nil {
		return err
	}
	unlock, err := m.lock(s.Project.ID)
	if err != nil {
		return err
	}
	defer unlock()
	if ws.Root != s.Workspace.Root || p.ID != s.Project.ID || s.Released {
		return errors.New("workspace/project differs from active recorded task")
	}
	if len(ws.Revisions) != len(s.Workspace.Revisions) {
		return errors.New("workspace revisions differ from inspection")
	}
	type publication struct{ dir, url, sha, branch string }
	var pending []publication
	for i, recorded := range s.Workspace.Revisions {
		r := ws.Revisions[i]
		if r.RepositoryID != recorded.RepositoryID || r.Path != recorded.Path || r.Branch != recorded.Branch || r.TargetBranch != recorded.TargetBranch || r.HeadSHA != recorded.HeadSHA || r.Changed != recorded.Changed {
			return fmt.Errorf("repository %s differs from inspection", recorded.RepositoryID)
		}
		if !r.Changed || r.Merged {
			continue
		}
		if r.Branch != s.MetaBranch || r.Branch == r.TargetBranch || r.HeadSHA == "" {
			return errors.New("publication must use the recorded task branch and commit")
		}
		url := ""
		for _, repo := range s.Project.Repositories {
			if repo.ID == r.RepositoryID {
				url = repo.URL
				break
			}
		}
		if err := validURL(url); err != nil {
			return err
		}
		dir := filepath.Join(ws.Root, r.Path)
		head, err := m.git(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if head != r.HeadSHA {
			return fmt.Errorf("repository %s changed since inspection", r.RepositoryID)
		}
		branch, err := m.git(ctx, dir, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return err
		}
		if branch != r.Branch {
			return fmt.Errorf("repository %s branch changed since inspection", r.RepositoryID)
		}
		origin, err := m.git(ctx, dir, "remote", "get-url", "origin")
		if err != nil {
			return err
		}
		if origin != url {
			return errors.New("repository origin differs from registered URL")
		}
		// A literal push URL can still be redirected by repository-controlled
		// URL rewrite settings. Reject those before introducing write credentials.
		keys, err := m.git(ctx, dir, "config", "--no-includes", "--name-only", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
		if err == nil && keys != "" {
			return errors.New("Git URL rewrites forbidden for publication")
		}
		pending = append(pending, publication{dir, url, r.HeadSHA, r.Branch})
	}
	// Validate every repository before publishing any of them. Multi-repository
	// publication can still be partial after a network error and is retryable.
	for _, item := range pending {
		if _, err := m.git(ctx, item.dir, "-c", "http.followRedirects=false", "push", "--recurse-submodules=no", "--", item.url, item.sha+":refs/heads/"+item.branch); err != nil {
			return err
		}
	}
	return nil
}
