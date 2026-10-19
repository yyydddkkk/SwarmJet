package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
)

const (
	cellBundleDir  = "/workspace/.swarmjet"
	cellBundleName = "publish.bundle"
	cellBundlePath = cellBundleDir + "/" + cellBundleName
	cellOutputCap  = 4 << 20
)

// CellRunner runs one command inside a running Cell. It must never add model,
// MCP or Git credentials to the child environment: the Cell's own configuration
// is allowed to execute there, but only inside the container.
type CellRunner interface {
	Run(ctx context.Context, container, workdir string, argv []string) (Result, error)
}

// DockerCellRunner is the production CellRunner. It uses `docker exec` without
// `-e` for any secret; only the CLI's safe HOME/XDG locations are supplied.
type DockerCellRunner struct {
	Binary string
}

func (r *DockerCellRunner) Run(ctx context.Context, container, workdir string, argv []string) (Result, error) {
	if container == "" {
		return Result{}, errors.New("cell: docker exec requires a container name")
	}
	if len(argv) == 0 {
		return Result{}, errors.New("cell: docker exec requires a command")
	}
	binary := r.Binary
	if binary == "" {
		binary = "docker"
	}
	args := []string{"exec", "-w", workdir,
		"-e", "HOME=/cell/home",
		"-e", "XDG_CONFIG_HOME=/cell/config",
		"-e", "XDG_DATA_HOME=/cell/data",
		"-e", "XDG_STATE_HOME=/cell/state",
		"-e", "GIT_TERMINAL_PROMPT=0",
		container,
	}
	args = append(args, argv...)

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = os.Environ()
	stdout := newLimitedBuffer(cellOutputCap)
	stderr := newLimitedBuffer(cellOutputCap)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}

// limitedBuffer keeps a bounded prefix of output so a malicious hook or git
// alias cannot make the platform allocate unbounded memory.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func newLimitedBuffer(max int) *limitedBuffer {
	return &limitedBuffer{max: max}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if remaining := b.max - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.buf.Write(p)
	} else {
		b.truncated = true
	}
	return n, nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]byte(nil), b.buf.Bytes()...)
	if b.truncated {
		out = append(out, []byte("\n[output truncated]")...)
	}
	return out
}

type cellGit struct {
	runner    CellRunner
	container string
}

func (g *cellGit) run(ctx context.Context, argv ...string) (Result, error) {
	if g.runner == nil {
		return Result{}, errors.New("cell: no Cell command runner configured")
	}
	return g.runner.Run(ctx, g.container, cellWorkdir, argv)
}

func (g *cellGit) git(ctx context.Context, args ...string) (Result, error) {
	return g.run(ctx, append([]string{"git"}, args...)...)
}

// prepareCell puts an existing workspace on the Agent branch from inside the
// container. After the first host-side clone, the host never runs git against
// the workspace again, so repository-controlled config cannot execute there.
func (r *Runtime) prepareCell(ctx context.Context, spec cell.Spec, fresh bool) error {
	secrets := specSecrets(spec.Env, spec.MCPToken)
	g := &cellGit{runner: r.cellRunner(), container: containerName(spec.AgentID)}

	origin, err := g.git(ctx, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("cell: inspect workspace origin: %s", redact(err.Error(), secrets...))
	}
	if origin.ExitCode != 0 {
		return errors.New("cell: workspace has no origin remote")
	}
	if got := strings.TrimSpace(string(origin.Stdout)); got != r.git().URL(spec.Repository) {
		return errors.New("cell: workspace origin does not match the Agent repository")
	}

	// A role pinned to a revision checks out that exact commit detached. Branch
	// then only names the local branch, which is never pushed by a read-only
	// Cell.
	if spec.Revision != "" {
		return cellCheckoutRevision(ctx, g, spec)
	}

	if fresh {
		// A fresh clone uses --no-checkout, so the index is empty even though
		// HEAD may resolve. Populate the working tree without a dirty check.
		return cellCheckoutBranch(ctx, g, spec)
	}
	head, err := g.git(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return fmt.Errorf("cell: inspect workspace HEAD: %s", redact(err.Error(), secrets...))
	}
	if head.ExitCode != 0 {
		return errors.New("cell: existing workspace has no HEAD commit")
	}
	empty, err := cellIndexEmpty(ctx, g)
	if err != nil {
		return err
	}
	if empty {
		// A previous process may have cloned but failed before the container
		// checkout. Populate the (empty) index instead of treating the
		// resulting deletions as dirty work.
		return cellCheckoutBranch(ctx, g, spec)
	}

	current, err := g.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("cell: inspect current branch: %s", redact(err.Error(), secrets...))
	}
	if current.ExitCode == 0 && strings.TrimSpace(string(current.Stdout)) == spec.Branch {
		return nil
	}
	status, err := g.git(ctx, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("cell: inspect workspace status: %s", redact(err.Error(), secrets...))
	}
	if status.ExitCode != 0 {
		return fmt.Errorf("cell: inspect workspace status: %s", strings.TrimSpace(string(status.Stderr)))
	}
	if strings.TrimSpace(string(status.Stdout)) != "" {
		return errors.New("cell: workspace has uncommitted changes on another branch")
	}
	if exists, err := cellRefExists(ctx, g, "refs/heads/"+spec.Branch); err != nil {
		return err
	} else if exists {
		if _, err := g.git(ctx, "checkout", "--quiet", spec.Branch); err != nil {
			return fmt.Errorf("cell: checkout %s: %s", spec.Branch, redact(err.Error(), secrets...))
		}
		return nil
	}
	if exists, err := cellRefExists(ctx, g, "refs/remotes/origin/"+spec.Branch); err != nil {
		return err
	} else if exists {
		return cellCheckout(ctx, g, spec.Branch, "origin/"+spec.Branch)
	}
	base, err := cellBaseBranch(ctx, g, spec.BaseBranch)
	if err != nil {
		return err
	}
	return cellCheckout(ctx, g, spec.Branch, "origin/"+base)
}

func cellCheckoutRevision(ctx context.Context, g *cellGit, spec cell.Spec) error {
	res, err := g.git(ctx, "cat-file", "-e", spec.Revision+"^{commit}")
	if err != nil {
		return fmt.Errorf("cell: inspect revision %s: %w", spec.Revision, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cell: revision %q is not present in the workspace", spec.Revision)
	}
	if _, err := g.git(ctx, "checkout", "--quiet", "-B", spec.Branch, spec.Revision); err != nil {
		return fmt.Errorf("cell: checkout %s at %s: %w", spec.Branch, spec.Revision, err)
	}
	if _, err := g.git(ctx, "checkout", "--quiet", "--detach", spec.Revision); err != nil {
		return fmt.Errorf("cell: detach at %s: %w", spec.Revision, err)
	}
	return nil
}

func cellCheckoutBranch(ctx context.Context, g *cellGit, spec cell.Spec) error {
	if exists, err := cellRefExists(ctx, g, "refs/remotes/origin/"+spec.Branch); err != nil {
		return err
	} else if exists {
		return cellCheckout(ctx, g, spec.Branch, "origin/"+spec.Branch)
	}
	base, err := cellBaseBranch(ctx, g, spec.BaseBranch)
	if err != nil {
		return err
	}
	return cellCheckout(ctx, g, spec.Branch, "origin/"+base)
}

func cellIndexEmpty(ctx context.Context, g *cellGit) (bool, error) {
	res, err := g.git(ctx, "ls-files", "--stage")
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("cell: inspect workspace index: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return strings.TrimSpace(string(res.Stdout)) == "", nil
}

func cellCheckout(ctx context.Context, g *cellGit, branch, start string) error {
	if _, err := g.git(ctx, "checkout", "--quiet", "-B", branch, start); err != nil {
		return fmt.Errorf("cell: checkout %s from %s: %w", branch, start, err)
	}
	return nil
}

func cellRefExists(ctx context.Context, g *cellGit, ref string) (bool, error) {
	res, err := g.git(ctx, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("cell: inspect ref %s: %s", ref, strings.TrimSpace(string(res.Stderr)))
	}
}

// cellBaseBranch resolves BaseBranch or the container's remote default branch.
// The container's refs come from the first trusted clone.
func cellBaseBranch(ctx context.Context, g *cellGit, explicit string) (string, error) {
	if explicit != "" {
		if err := validBranch(explicit); err != nil {
			return "", err
		}
		exists, err := cellRefExists(ctx, g, "refs/remotes/origin/"+explicit)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("cell: base branch %q is not present in the workspace", explicit)
		}
		return explicit, nil
	}
	res, err := g.git(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	if res.ExitCode == 0 {
		name := strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "origin/")
		if name != "" && validBranch(name) == nil {
			if exists, err := cellRefExists(ctx, g, "refs/remotes/origin/"+name); err == nil && exists {
				return name, nil
			}
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if exists, err := cellRefExists(ctx, g, "refs/remotes/origin/"+candidate); err != nil {
			return "", err
		} else if exists {
			return candidate, nil
		}
	}
	return "", errors.New("cell: cannot determine the repository default branch")
}
