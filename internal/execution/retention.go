package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"swarmjet/internal/domain"
)

var retentionID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// Executions lists supervisor-owned execution records for a task.
type Executions interface {
	TaskExecutions(context.Context, string) ([]domain.Execution, error)
}

// WorkspaceReleaser releases one task workspace.
type WorkspaceReleaser interface {
	Release(context.Context, domain.Workspace) error
}

// RetireConfig supplies the node-local resources needed to retire a task.
type RetireConfig struct {
	DataDir    string
	Executions Executions
	Workspace  WorkspaceReleaser
	Docker     func(context.Context, []string) ([]byte, error)
}

func retirePath(dataDir, category, id string) (string, error) {
	if !retentionID.MatchString(id) {
		return "", errors.New("unsafe retention identifier")
	}
	base, e := filepath.Abs(dataDir)
	if e != nil {
		return "", e
	}
	p := filepath.Join(base, category, id)
	for q := p; q != base; q = filepath.Dir(q) {
		st, e := os.Lstat(q)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return "", e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("retention path is symlink: %s", q)
		}
	}
	return p, nil
}

func retireDocker(ctx context.Context, run func(context.Context, []string) ([]byte, error), args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if run != nil {
		return run(c, args)
	}
	cmd := exec.CommandContext(c, "docker", args...)
	env := []string{}
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	cmd.Env = env
	return cmd.CombinedOutput()
}

func retireStopped(ctx context.Context, cfg RetireConfig, ex domain.Execution) (bool, error) {
	if !retentionID.MatchString(ex.ID) || ex.ContainerID != "sj-agent-"+ex.ID {
		return false, errors.New("supervisor container identity mismatch")
	}
	b, e := retireDocker(ctx, cfg.Docker, "container", "inspect", ex.ContainerID)
	if e != nil {
		message := strings.ToLower(string(b))
		if strings.Contains(message, "no such container") || strings.Contains(message, "no such object") {
			return false, nil
		}
		return false, fmt.Errorf("cannot inspect supervisor container: %w", e)
	}
	var found []struct {
		Name  string
		State struct {
			Running bool
			Status  string
		}
		Config struct{ Labels map[string]string }
	}
	if e = json.Unmarshal(b, &found); e != nil || len(found) != 1 {
		return false, errors.New("invalid container inspection")
	}
	c := found[0]
	if strings.TrimPrefix(c.Name, "/") != ex.ContainerID || c.Config.Labels["swarmjet.execution"] != ex.ID {
		return false, errors.New("container ownership label/name mismatch")
	}
	if c.State.Running || (c.State.Status != "exited" && c.State.Status != "dead") {
		return false, errors.New("task has an active container; preserving all artifacts")
	}
	return true, nil
}

// RetireTask removes node-local artifacts for an expired terminal task. It
// refuses active or paused tasks and unowned containers, so retention can never
// delete another task's files or a running task's workspace.
func RetireTask(ctx context.Context, cfg RetireConfig, t domain.Task) error {
	if !retentionID.MatchString(t.ID) {
		return errors.New("unsafe task identifier")
	}
	if t.Paused || (t.Stage != "completed" && t.Stage != "cancelled") {
		return errors.New("active or paused task cannot be retired")
	}
	paths := []string{}
	for _, category := range []string{"task-sources", "opencode", "agent-profiles", "runs"} {
		p, e := retirePath(cfg.DataDir, category, t.ID)
		if e != nil {
			return e
		}
		paths = append(paths, p)
	}
	if cfg.Executions == nil {
		return errors.New("agent supervisor is required")
	}
	executions, e := cfg.Executions.TaskExecutions(ctx, t.ID)
	if e != nil {
		return e
	}
	containers := []string{}
	records := []string{}
	for _, ex := range executions {
		present, e := retireStopped(ctx, cfg, ex)
		if e != nil {
			return e
		}
		if present {
			containers = append(containers, ex.ContainerID)
		}
		p, e := retirePath(cfg.DataDir, "agent-executions", ex.ID)
		if e != nil {
			return e
		}
		p += ".json"
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return errors.New("supervisor record must be a regular file")
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		var rec struct {
			TaskID    string           `json:"taskId"`
			Execution domain.Execution `json:"execution"`
		}
		if e = json.Unmarshal(b, &rec); e != nil {
			return e
		}
		if rec.TaskID != t.ID || rec.Execution.ID != ex.ID || rec.Execution.ContainerID != ex.ContainerID {
			return errors.New("supervisor record ownership mismatch")
		}
		records = append(records, p)
	}
	if t.Stage == "cancelled" {
		if t.Workspace.Root != "" {
			if t.Workspace.TaskID != t.ID || filepath.Clean(t.Workspace.Root) != filepath.Join(paths[0], "meta", ".worktrees", t.ID) {
				return errors.New("cancelled workspace identity/path mismatch")
			}
			if _, e = os.Stat(t.Workspace.Root); e == nil {
				if cfg.Workspace == nil {
					return errors.New("workspace manager is required")
				}
				if e = cfg.Workspace.Release(ctx, t.Workspace); e != nil {
					return fmt.Errorf("cancelled workspace retained: %w", e)
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
		} else if _, e = os.Stat(paths[0]); e == nil {
			return errors.New("cancelled task sources have no workspace ownership snapshot")
		}
	}
	for _, name := range containers {
		if b, e := retireDocker(ctx, cfg.Docker, "container", "rm", name); e != nil {
			return fmt.Errorf("remove stopped container: %w: %s", e, strings.TrimSpace(string(b)))
		}
	}
	for _, p := range paths {
		if e = os.RemoveAll(p); e != nil {
			return e
		}
	}
	for _, p := range records {
		if e = os.Remove(p); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	return nil
}
