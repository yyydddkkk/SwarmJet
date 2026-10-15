package managedmeta

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// SyncRemote imports the remote ref into a temporary bare repo before installing
// it locally. It never checks out or executes remote files, runs hooks, or pushes.
func (m *Manager) SyncRemote(ctx context.Context, projectID, ref, remote string, credentials map[string]string) error {
	if !idPattern.MatchString(projectID) || !domain.ValidBranch(ref) {
		return errors.New("invalid managed Meta identity")
	}
	stage, err := os.MkdirTemp(m.DataDir, ".meta-remote-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	repo := filepath.Join(stage, "repo.git")
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(stage, "home"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:http", "LANG=C"}
	for _, key := range []string{"GIT_ASKPASS", "GITLAB_TOKEN"} {
		if value := credentials[key]; value != "" {
			env = append(env, key+"="+value)
		}
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "http.followRedirects=false"}, args...)...)
		cmd.Env = env
		// Do not surface remote-controlled stderr or credential-bearing diagnostics.
		if cmd.Run() != nil {
			return errors.New("无法同步 GitLab Meta 仓库，请检查网络、凭证和远程分支")
		}
		return nil
	}
	if err = run("init", "--bare", "-q", repo); err != nil {
		return err
	}
	if err = run("-C", repo, "fetch", "--no-tags", "--no-recurse-submodules", remote, "refs/heads/"+ref+":refs/heads/"+ref); err != nil {
		return err
	}
	bundle := filepath.Join(stage, "meta.bundle")
	if err = run("-C", repo, "bundle", "create", bundle, "--all"); err != nil {
		return err
	}
	body, err := os.ReadFile(bundle)
	if err != nil {
		return err
	}
	return m.Install(ctx, projectID, ref, body)
}
