// Package maintenance retires artifacts only for expired terminal tasks.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"swarmjet/internal/agent"
	"swarmjet/internal/domain"
	"swarmjet/internal/store"
	"swarmjet/internal/workspace"
)

type DockerExecutor func(context.Context, []string) ([]byte, error)
type Manager struct {
	Store                  *store.Store
	Workspace              *workspace.Manager
	Agents                 *agent.Runtime
	DataDir                string
	FailedDays, ReportDays int
	Docker                 DockerExecutor
	// Event permits a recording sink in isolated filesystem tests.
	Event func(context.Context, string, string, string) error
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (m *Manager) days() (int, int) {
	failed, reports := m.FailedDays, m.ReportDays
	if failed <= 0 {
		failed = 7
	}
	if reports <= 0 {
		reports = 30
	}
	return failed, reports
}
func (m *Manager) eligible(t domain.Task, updated, now time.Time) bool {
	if t.Paused {
		return false
	}
	failed, reports := m.days()
	days := 0
	switch t.Stage {
	case "completed":
		days = reports
	case "cancelled":
		days = failed
	default:
		return false
	}
	return updated.Before(now.Add(-time.Duration(days) * 24 * time.Hour))
}
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if e := m.Sweep(ctx); e != nil && !errors.Is(e, context.Canceled) {
			slog.Error("artifact retention sweep failed", "error", e)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) Sweep(ctx context.Context) error {
	if m.Store == nil || m.Store.Pool == nil {
		return errors.New("maintenance store is required")
	}
	if m.Agents == nil || m.Workspace == nil {
		return errors.New("maintenance agent/workspace managers are required")
	}
	dir := filepath.Join(m.DataDir, "maintenance-state")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(e, syscall.EWOULDBLOCK) {
		return nil
	} else if e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	failed, reports := m.days()
	now := time.Now().UTC()
	rows, e := m.Store.Pool.Query(ctx, `SELECT id FROM tasks WHERE terminal AND COALESCE((body->>'paused')::boolean,false)=false AND ((stage='completed' AND updated_at<$1) OR (stage='cancelled' AND updated_at<$2)) ORDER BY updated_at`, now.Add(-time.Duration(reports)*24*time.Hour), now.Add(-time.Duration(failed)*24*time.Hour))
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var failures []error
	for _, id := range ids {
		if e = m.sweepLocked(ctx, id, now); e != nil {
			failures = append(failures, fmt.Errorf("task %s: %w", id, e))
		}
	}
	return errors.Join(failures...)
}
func (m *Manager) sweepLocked(ctx context.Context, id string, now time.Time) error {
	if !identifier.MatchString(id) {
		return errors.New("unsafe task identifier")
	}
	if _, e := os.Stat(m.marker(id)); e == nil {
		return nil
	}
	tx, e := m.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var body []byte
	var updated time.Time
	var stage string
	var terminal bool
	if e = tx.QueryRow(ctx, "SELECT body,updated_at,stage,terminal FROM tasks WHERE id=$1 FOR UPDATE", id).Scan(&body, &updated, &stage, &terminal); e != nil {
		return e
	}
	var task domain.Task
	if e = json.Unmarshal(body, &task); e != nil {
		return e
	}
	if task.ID != id || task.Stage != stage {
		return errors.New("task identity/stage disagrees with stored row")
	}
	if !terminal || !m.eligible(task, updated, now) {
		return nil
	}
	if e = m.cleanup(ctx, task); e != nil {
		_ = tx.Rollback(ctx)
		warning := m.emit(ctx, id, "retention_warning", e.Error())
		return errors.Join(e, warning)
	}
	// Commit eligibility lock before the normal event helper obtains a connection.
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if e = m.emit(ctx, id, "artifact_retention", "Expired task containers, private sources, sessions, and reports removed; task history retained."); e != nil {
		return e
	}
	return m.mark(id)
}
func (m *Manager) marker(id string) string {
	return filepath.Join(m.DataDir, "maintenance-state", id+".complete")
}
func (m *Manager) mark(id string) error {
	if !identifier.MatchString(id) {
		return errors.New("unsafe task identifier")
	}
	p := m.marker(id)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".retention-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.WriteString(time.Now().UTC().Format(time.RFC3339) + "\n")
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), p)
}
func (m *Manager) emit(ctx context.Context, id, kind, msg string) error {
	if m.Event != nil {
		return m.Event(ctx, id, kind, msg)
	}
	if m.Store == nil {
		return errors.New("maintenance event store unavailable")
	}
	return m.Store.Event(ctx, id, kind, msg)
}
func (m *Manager) path(category, id string) (string, error) {
	if !identifier.MatchString(id) {
		return "", errors.New("unsafe retention identifier")
	}
	base, e := filepath.Abs(m.DataDir)
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
func (m *Manager) docker(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if m.Docker != nil {
		return m.Docker(ctx, args)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	env := []string{}
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	cmd.Env = env
	return cmd.CombinedOutput()
}

type container struct {
	Name  string
	State struct {
		Running bool
		Status  string
	}
	Config struct{ Labels map[string]string }
}

func (m *Manager) stopped(ctx context.Context, ex domain.Execution) (bool, error) {
	if !identifier.MatchString(ex.ID) || ex.ContainerID != "sj-agent-"+ex.ID {
		return false, errors.New("supervisor container identity mismatch")
	}
	b, e := m.docker(ctx, "container", "inspect", ex.ContainerID)
	if e != nil {
		message := strings.ToLower(string(b))
		if strings.Contains(message, "no such container") || strings.Contains(message, "no such object") {
			return false, nil
		}
		return false, fmt.Errorf("cannot inspect supervisor container: %w", e)
	}
	var found []container
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
func (m *Manager) cleanup(ctx context.Context, t domain.Task) error {
	if !identifier.MatchString(t.ID) {
		return errors.New("unsafe task identifier")
	}
	if t.Paused || (t.Stage != "completed" && t.Stage != "cancelled") {
		return errors.New("active or paused task cannot be retired")
	}
	if m.Agents == nil {
		return errors.New("agent supervisor is required")
	}
	paths := []string{}
	for _, category := range []string{"task-sources", "opencode", "runs"} {
		p, e := m.path(category, t.ID)
		if e != nil {
			return e
		}
		paths = append(paths, p)
	}
	// Read only supervisor-owned execution records; task JSON may omit old rework runs.
	executions, e := m.Agents.TaskExecutions(ctx, t.ID)
	if e != nil {
		return e
	}
	containers := []string{}
	records := []string{}
	for _, ex := range executions {
		present, e := m.stopped(ctx, ex)
		if e != nil {
			return e
		}
		if present {
			containers = append(containers, ex.ContainerID)
		}
		p, e := m.path("agent-executions", ex.ID)
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
				if m.Workspace == nil {
					return errors.New("workspace manager is required")
				}
				if e = m.Workspace.Release(ctx, t.Workspace); e != nil {
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
		if b, e := m.docker(ctx, "container", "rm", name); e != nil {
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
