package platformwrite

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// TrustedRepo is a control-plane checkout containing the exact commit. Dir
// must not be writable by a Runner. URL comes from administrator configuration.
type TrustedRepo struct {
	ProjectID int64
	Dir       string
	URL       string
}

type GitPusher struct {
	Repositories map[string]TrustedRepo
	// AuthEnv is resolved in the trusted process only (for example GIT_ASKPASS).
	AuthEnv map[string]string
}

func (p GitPusher) PushFixedSHA(ctx context.Context, w store.KubernetesWriteIntent) error {
	repo, ok := p.Repositories[w.RepositoryID]
	if !ok || repo.ProjectID != w.ProjectID || !filepath.IsAbs(repo.Dir) {
		return errors.New("trusted repository does not match write intent")
	}
	u, err := url.Parse(repo.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid trusted repository URL")
	}
	if (len(w.HeadSHA) != 40 && len(w.HeadSHA) != 64) || len(w.SourceBranch) == 0 || strings.HasPrefix(w.SourceBranch, "-") {
		return errors.New("invalid fixed revision")
	}
	if _, err := hex.DecodeString(w.HeadSHA); err != nil {
		return errors.New("invalid fixed revision")
	}
	// A task checkout may contain Git configuration supplied by the repository.
	// Reject URL rewrites and disable hooks before credentials enter Git.
	if out, err := p.git(ctx, repo.Dir, "config", "--local", "--no-includes", "--name-only", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`); err == nil && strings.TrimSpace(out) != "" {
		return errors.New("Git URL rewrites are forbidden")
	} else if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return err
		}
	}
	kind, err := p.git(ctx, repo.Dir, "cat-file", "-t", w.HeadSHA)
	if err != nil {
		return err
	}
	if strings.TrimSpace(kind) != "commit" {
		return errors.New("fixed revision is not a commit")
	}
	_, err = p.git(ctx, repo.Dir, "-c", "core.hooksPath=/dev/null", "-c", "http.followRedirects=false", "push", "--porcelain", "--recurse-submodules=no", "--", repo.URL, w.HeadSHA+":refs/heads/"+w.SourceBranch)
	return err
}

func (p GitPusher) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") && !strings.HasPrefix(key, "LD_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	for k, v := range p.AuthEnv {
		if k != "GIT_ASKPASS" && k != "SWARMJET_GIT_TOKEN" {
			return "", fmt.Errorf("unsupported Git auth variable %q", k)
		}
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Git output can contain credential-bearing URLs; never return it.
		return "", fmt.Errorf("trusted Git operation failed: %w", err)
	}
	return string(out), nil
}

var _ FixedSHAPusher = GitPusher{}
