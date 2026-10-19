package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TokenSource returns a short-lived Git credential for a repository in
// "owner/name" form. The token is only ever passed to git through the
// environment and an askpass helper; it never appears in a remote URL, on the
// command line or in a log line.
type TokenSource func(ctx context.Context, repository string) (string, error)

// GitRunner runs one git command. It exists so tests can observe the exact
// arguments and environment without a network or a real repository.
type GitRunner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
}

// Git runs host-side git commands with repository-controlled behaviour
// disabled. The platform owns the workspace directory; the Cell only writes
// files in it.
type Git struct {
	Binary string
	// Remote maps "owner/name" to a clone URL. It defaults to GitHub HTTPS.
	Remote func(repository string) string
	// Home is HOME for git subprocesses. Empty inherits the platform's HOME.
	Home string
}

// URL returns the credential-free clone URL for a repository.
func (g *Git) URL(repository string) string {
	if g.Remote != nil {
		return g.Remote(repository)
	}
	return "https://github.com/" + repository + ".git"
}

func (g *Git) binary() string {
	if g.Binary != "" {
		return g.Binary
	}
	return "git"
}

// Run executes git with hooks, credential helpers, URL rewrites and external
// diff drivers disabled. extra env entries win over the base environment.
func (g *Git) Run(ctx context.Context, dir string, extra []string, args ...string) ([]byte, error) {
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=file:https:http:ssh",
	}
	if home := g.Home; home != "" {
		base = append(base, "HOME="+home)
	} else if home := os.Getenv("HOME"); home != "" {
		base = append(base, "HOME="+home)
	}

	flags := []string{
		"-c", "core.askpass=",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "credential.helper=",
		"-c", "core.sshCommand=ssh -F /dev/null",
		"-c", "core.gitProxy=",
		"-c", "diff.external=",
		"-c", "protocol.ext.allow=never",
		"-c", "http.sslVerify=true",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "submodule.recurse=false",
	}
	cmd := exec.CommandContext(ctx, g.binary(), append(flags, args...)...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(base, extra)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s failed: %w: %s", gitVerb(args), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func gitVerb(args []string) string {
	for _, arg := range args {
		if arg != "--" && !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return "command"
}

func mergeEnv(base, extra []string) []string {
	merged := make(map[string]string, len(base)+len(extra))
	order := make([]string, 0, len(base)+len(extra))
	add := func(entry string) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return
		}
		if _, seen := merged[key]; !seen {
			order = append(order, key)
		}
		merged[key] = value
	}
	for _, entry := range base {
		add(entry)
	}
	for _, entry := range extra {
		add(entry)
	}
	sort.Strings(order)
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+merged[key])
	}
	return out
}

const askpassScript = `#!/bin/sh
case "${1:-}" in
  *Username*|*username*) printf '%s\n' x-access-token ;;
  *Password*|*password*) printf '%s\n' "${SWARMJET_GIT_TOKEN:?Git token is required}" ;;
  *) exit 1 ;;
esac
`

// gitAuth owns a temporary askpass helper. The helper contains no secret; the
// token lives in the child environment named SWARMJET_GIT_TOKEN.
type gitAuth struct {
	dir string
	env []string
}

func newGitAuth(token string) (*gitAuth, error) {
	if token == "" {
		return &gitAuth{}, nil
	}
	dir, err := os.MkdirTemp("", "swarmjet-git-auth-")
	if err != nil {
		return nil, fmt.Errorf("create git auth directory: %w", err)
	}
	path := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(path, []byte(askpassScript), 0700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write git askpass helper: %w", err)
	}
	return &gitAuth{dir: dir, env: []string{
		"GIT_ASKPASS=" + path,
		"GIT_TERMINAL_PROMPT=0",
		"SWARMJET_GIT_TOKEN=" + token,
	}}, nil
}

func (a *gitAuth) Close() {
	if a == nil || a.dir == "" {
		return
	}
	_ = os.RemoveAll(a.dir)
}

// redact removes credential values from diagnostics.
func redact(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
}

// specSecrets returns every value that must never reach a log or error message,
// longest first so a secret containing another is removed intact.
func specSecrets(secrets map[string]string, extra ...string) []string {
	out := make([]string, 0, len(secrets)+len(extra))
	for _, value := range secrets {
		if value != "" {
			out = append(out, value)
		}
	}
	for _, value := range extra {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

var (
	agentIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	branchPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	envKeyPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func validAgentID(id string) error {
	if !agentIDPattern.MatchString(id) {
		return errors.New("cell: invalid agent ID")
	}
	return nil
}

func validRepository(repository string) error {
	if !repositoryPattern.MatchString(repository) {
		return errors.New("cell: repository must be owner/name")
	}
	return nil
}

// validBranch rejects refs that git could interpret as options or that are not
// valid branch names. The Publisher relies on this before building a refspec.
func validBranch(branch string) error {
	if !branchPattern.MatchString(branch) {
		return fmt.Errorf("cell: invalid branch %q", branch)
	}
	if branch == "HEAD" || strings.Contains(branch, "..") || strings.Contains(branch, "//") ||
		strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, ".lock") {
		return fmt.Errorf("cell: invalid branch %q", branch)
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return fmt.Errorf("cell: invalid branch %q", branch)
		}
	}
	return nil
}
