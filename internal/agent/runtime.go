// Package agent supervises durable CLI containers. Adapter contracts and
// configuration isolation are documented in README.md.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
)

// TaskExecutions enumerates supervisor-owned records, never agent-owned artifacts.
func (r *Runtime) TaskExecutions(ctx context.Context, taskID string) ([]domain.Execution, error) {
	if !identifier.MatchString(taskID) {
		return nil, errors.New("invalid task ID")
	}
	entries, e := os.ReadDir(filepath.Join(r.DataDir, "agent-executions"))
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	records := []record{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, e := r.read(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return nil, e
		}
		if rec.TaskID == taskID {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StartedAt.Before(records[j].StartedAt) })
	out := []domain.Execution{}
	for _, rec := range records {
		out = append(out, rec.Execution)
	}
	return out, nil
}
func (r *Runtime) CancelTask(ctx context.Context, taskID string) error {
	if !identifier.MatchString(taskID) {
		return errors.New("invalid task ID")
	}
	entries, e := os.ReadDir(filepath.Join(r.DataDir, "agent-executions"))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, e := r.read(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return e
		}
		if rec.TaskID == taskID && !terminal(rec.Execution.Status) {
			if e = r.Cancel(ctx, rec.Execution.ID); e != nil {
				return e
			}
		}
	}
	return nil
}

type Executor interface {
	Run(context.Context, []string, []string) ([]byte, error)
}
type dockerExecutor struct{ binary string }
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 16<<20 {
		remaining := (16 << 20) - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func (e dockerExecutor) Run(ctx context.Context, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.binary, args...)
	cmd.Env = append(os.Environ(), env...)
	var out cappedBuffer
	cmd.Stdout = &out
	// Docker logs relays container stderr on the CLI's stderr; retain both streams.
	if len(args) > 0 && args[0] == "logs" {
		cmd.Stderr = &out
	} else {
		cmd.Stderr = io.Discard
	}
	err := cmd.Run()
	return out.Bytes(), err
}

type Runtime struct {
	DataDir      string
	DockerBinary string
	Executor     Executor
}

var _ execution.AgentBackend = (*Runtime)(nil)

func New(dataDir string) *Runtime {
	p, _ := filepath.Abs(dataDir)
	return &Runtime{DataDir: p, DockerBinary: "docker"}
}

type record struct {
	CLIFailure       string           `json:"cliFailure,omitempty"`
	Adapter          string           `json:"adapter,omitempty"`
	ProfileHome      string           `json:"profileHome,omitempty"`
	ProfileSnapshot  string           `json:"profileSnapshot,omitempty"`
	Execution        domain.Execution `json:"execution"`
	TaskID           string           `json:"taskId"`
	Role             string           `json:"role"`
	ProfileScope     string           `json:"profileScope,omitempty"`
	Workspace        string           `json:"workspace"`
	StartedAt        time.Time        `json:"startedAt"`
	Timeout          int              `json:"timeout"`
	EnvNames         []string         `json:"envNames"`
	ResultPath       string           `json:"resultPath"`
	AuthPath         string           `json:"authPath,omitempty"`
	AuthSnapshotPath string           `json:"authSnapshotPath,omitempty"`
	AuthRequired     bool             `json:"authRequired,omitempty"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (r *Runtime) run(ctx context.Context, args, env []string) ([]byte, error) {
	if r.Executor != nil {
		return r.Executor.Run(ctx, args, env)
	}
	binary := r.DockerBinary
	if binary == "" {
		binary = "docker"
	}
	return (dockerExecutor{binary}).Run(ctx, args, env)
}
func (r *Runtime) path(id string) string {
	return filepath.Join(r.DataDir, "agent-executions", id+".json")
}
func (r *Runtime) lock(ctx context.Context) (func(), error) {
	dir := filepath.Join(r.DataDir, "agent-executions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func (r *Runtime) save(rec record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return atomicWrite(r.path(rec.Execution.ID), b)
}
func (r *Runtime) read(id string) (record, error) {
	var rec record
	if !identifier.MatchString(id) {
		return rec, errors.New("invalid execution ID")
	}
	b, err := os.ReadFile(r.path(id))
	if err == nil {
		err = json.Unmarshal(b, &rec)
	}
	return rec, err
}
func terminal(s string) bool {
	return s == "succeeded" || s == "failed" || s == "cancelled" || s == "timed_out"
}

func requestScope(req domain.AgentRequest) string {
	if req.ProfileScope != "" {
		return req.ProfileScope
	}
	return roleScope(req.Role)
}
func (r *Runtime) Resume(ctx context.Context, req domain.AgentRequest) (domain.Execution, error) {
	if req.SessionID == "" {
		return domain.Execution{}, errors.New("resume requires exact session ID")
	}
	return r.Start(ctx, req)
}
func (r *Runtime) Start(ctx context.Context, req domain.AgentRequest) (domain.Execution, error) {
	adapter, adapterErr := adapterFor(req.Config.Adapter)
	if adapterErr != nil {
		return domain.Execution{}, adapterErr
	}
	if strings.HasPrefix(req.SessionID, "-") || strings.ContainsAny(req.SessionID, "\x00\r\n") {
		return domain.Execution{}, errors.New("invalid session ID")
	}
	if req.Config.Image == "" {
		return domain.Execution{}, errors.New("agent image is required")
	}
	if !identifier.MatchString(req.ExecutionID) || !identifier.MatchString(req.TaskID) {
		return domain.Execution{}, errors.New("invalid task or execution ID")
	}
	if req.Role != "dev" && req.Role != "review" && req.Role != "merge" && req.Role != "init" {
		return domain.Execution{}, errors.New("invalid agent role")
	}
	if req.ProfileScope != "" && (req.Role != "merge" || req.ProfileScope != "dev") {
		return domain.Execution{}, errors.New("invalid Agent profile scope")
	}
	workspace, err := filepath.Abs(req.Workspace)
	if err != nil {
		return domain.Execution{}, err
	}
	taskRoot := filepath.Join(r.DataDir, "task-sources", req.TaskID)
	if req.Role == "init" {
		taskRoot = filepath.Join(r.DataDir, "initializations", req.TaskID)
	}
	if err = safeDirectory(taskRoot, false); err != nil {
		return domain.Execution{}, fmt.Errorf("task source directory: %w", err)
	}
	rel, err := filepath.Rel(taskRoot, workspace)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return domain.Execution{}, errors.New("workspace must be inside this task's private source directory")
	}
	if err = safeDirectory(workspace, false); err != nil {
		return domain.Execution{}, fmt.Errorf("workspace: %w", err)
	}
	generated := ""
	if req.Role == "init" {
		generated, err = filepath.Abs(req.GeneratedDir)
		if err != nil {
			return domain.Execution{}, err
		}
		grel, gerr := filepath.Rel(taskRoot, generated)
		if gerr != nil || grel == "." || grel == ".." || strings.HasPrefix(grel, ".."+string(os.PathSeparator)) {
			return domain.Execution{}, errors.New("generated directory must be inside this initialization")
		}
		if err = safeDirectory(generated, true); err != nil {
			return domain.Execution{}, fmt.Errorf("generated directory: %w", err)
		}
	}
	unlock, err := r.lock(ctx)
	if err != nil {
		return domain.Execution{}, err
	}
	defer unlock()
	rec, err := r.read(req.ExecutionID)
	originalStart := rec.StartedAt
	if err == nil {
		if rec.TaskID != req.TaskID || rec.Workspace != workspace || rec.Role != req.Role || rec.ProfileScope != req.ProfileScope || (rec.Adapter != "" && rec.Adapter != req.Config.Adapter) {
			return domain.Execution{}, errors.New("execution ID belongs to another request")
		}
		if terminal(rec.Execution.Status) {
			return rec.Execution, nil
		}
		if _, inspectErr := r.inspectContainer(ctx, rec.Execution.ContainerID); inspectErr == nil || rec.Execution.Status != "starting" {
			return r.reconcile(ctx, rec)
		}
		// Reconstruct the same named create after a crash before Docker create.
		err = os.ErrNotExist
	}
	if !errors.Is(err, os.ErrNotExist) {
		return domain.Execution{}, err
	}
	dir := filepath.Join(r.DataDir, "runs", req.TaskID, "executions", req.ExecutionID)
	if err = safeDirectory(dir, true); err != nil {
		return domain.Execution{}, err
	}
	req.Env, err = prepareEnvironment(dir, req.Env)
	if err != nil {
		return domain.Execution{}, err
	}
	resultPath := filepath.Join(dir, "result.json")
	promptPath := filepath.Join(dir, "prompt.txt")
	prompt := req.Prompt + "\n\nWrite your final structured result as a JSON object to " + resultPath + ". Required fields: outcome (string), summary (non-empty string), assumptions (string array), findings (objects with string fields repositoryId, path, message), needsHelp (string; use an empty string when no help is needed, never a boolean). Allowed outcomes for role " + req.Role + ": " + outcomes(req.Role) + ". When outcome is blocked, needsHelp must explain the concrete obstacle. The artifact is required even when blocked."
	if err = atomicWrite(promptPath, []byte(prompt)); err != nil {
		return domain.Execution{}, err
	}
	rec = record{Adapter: req.Config.Adapter, Execution: domain.Execution{ID: req.ExecutionID, SessionID: req.SessionID, ContainerID: "sj-agent-" + req.ExecutionID, Status: "starting", ExitCode: -1, LogPath: filepath.Join(dir, "output.jsonl"), ResultRepair: req.ReadOnlyWorkspace}, TaskID: req.TaskID, Role: req.Role, ProfileScope: req.ProfileScope, Workspace: workspace, StartedAt: time.Now().UTC(), Timeout: req.Config.TimeoutSeconds, ResultPath: resultPath}
	if !originalStart.IsZero() {
		rec.StartedAt = originalStart
	}
	args := []string{"create", "--name", rec.Execution.ContainerID, "--label", "swarmjet.execution=" + req.ExecutionID, "--mount", bindMount(dir, false), "--workdir", workspace}
	if req.Config.DockerNetwork != "" && !identifier.MatchString(req.Config.DockerNetwork) {
		return rec.Execution, errors.New("invalid task network")
	}
	args = append(args, ContainerPolicyArgs(req.Config)...)
	if req.Role == "init" {
		// The analyzed business sources are read-only; only the generated
		// directory is writable. This is enforced by mounts, not by prompt text.
		args = append(args, "--mount", bindMount(workspace, true), "--mount", bindMount(generated, false))
	} else {
		args = append(args, "--mount", bindMount(taskRoot, req.ReadOnlyWorkspace))
	}
	// Match the supervisor so native workers can read/reuse agent-created files.
	args = append(args, "--user", ContainerUser())
	reports := filepath.Join(r.DataDir, "runs", req.TaskID, "integration")
	if err = safeDirectory(reports, true); err != nil {
		return domain.Execution{}, err
	}
	args = append(args, "--mount", bindMount(reports, true))
	if req.Config.UseCLIDefaults || adapter.ID != "opencode" {
		home, snapshot, profileErr := r.prepareCLIProfile(req, adapter)
		if profileErr != nil {
			return domain.Execution{}, profileErr
		}
		rec.ProfileHome, rec.ProfileSnapshot = home, snapshot
		args = append(args, "--mount", bindMount(home, false))
	}
	dstGitLab := adapter.ID == "dst" && req.Env["GITLAB_TOKEN"] != "" && req.Env["GITLAB_URI"] != ""
	if dstGitLab {
		if err = prepareDSTGitLab(dir, req.Env); err != nil {
			return domain.Execution{}, err
		}
		args = append(args, "--tmpfs", fmt.Sprintf("%s:rw,mode=0700,uid=%d,gid=%d", dstGitLabConfigDir, os.Getuid(), os.Getgid()))
	}
	// Persist only credential names; docker receives values via its process environment.
	env := []string{}
	for k := range req.Env {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(k) {
			return domain.Execution{}, errors.New("invalid environment variable name")
		}
		rec.EnvNames = append(rec.EnvNames, k)
	}
	sort.Strings(rec.EnvNames)
	for _, k := range rec.EnvNames {
		args = append(args, "--env", k)
		env = append(env, k+"="+req.Env[k])
	}
	if rec.ProfileHome != "" {
		// Runtime-owned homes win over any deployment/request environment entry.
		for _, entry := range adapter.environment(rec.ProfileHome) {
			args = append(args, "--env", entry)
		}
	}
	if rec.ProfileHome == "" {
		// Developer history is isolated; merge reuses only this task's review scope.
		home, config, authPath, snapshotPath, hasAuth, profileErr := r.prepareProfile(req.TaskID, requestScope(req), req.ExecutionID)
		if profileErr != nil {
			return domain.Execution{}, profileErr
		}
		rec.AuthPath = authPath
		rec.AuthSnapshotPath = snapshotPath
		rec.AuthRequired = hasAuth
		// OpenCode creates startup files (including .gitignore) in XDG_CONFIG_HOME.
		// This is a private task/role copy; the shared profile remains unmounted.
		args = append(args, "--mount", bindMount(home, false), "--env", "HOME="+home, "--env", "XDG_DATA_HOME="+home, "--mount", bindMount(config, false), "--env", "XDG_CONFIG_HOME="+config)
	}
	if req.Role == "init" {
		// Static initialization may read sources and write generated documents,
		// but cannot run repository commands or delegate execution.
		args = append(args, "--env", `OPENCODE_CONFIG_CONTENT={"permission":{"*":"deny","read":"allow","glob":"allow","grep":"allow","list":"allow","edit":"allow","write":"allow","external_directory":"allow","bash":"deny","task":"deny","skill":"deny"}}`)
	}
	command, commandErr := adapter.command(req, promptPath, rec.ProfileHome)
	if commandErr != nil {
		return domain.Execution{}, commandErr
	}
	entrypoint := adapter.Executable
	if dstGitLab {
		entrypoint = "/bin/sh"
		command = append([]string{"-c", dstGitLabBootstrap, "swarmjet-dst-auth"}, command...)
	}
	args = append(args, "--entrypoint", entrypoint, req.Config.Image)
	args = append(args, command...)
	if err = r.save(rec); err != nil {
		return rec.Execution, err
	}
	if _, err = r.run(ctx, args, env); err != nil { // A create can succeed even if its client response is lost.
		if ctx.Err() != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if cleanupErr := r.cancelRecord(cleanupCtx, &rec); cleanupErr != nil {
				return rec.Execution, fmt.Errorf("create interrupted; cleanup needs recovery: %w", cleanupErr)
			}
			return rec.Execution, ctx.Err()
		}
		if _, inspectErr := r.inspectContainer(ctx, rec.Execution.ContainerID); inspectErr != nil {
			return rec.Execution, errors.New("docker create failed; execution retained for recovery")
		}
	}
	execution, reconcileErr := r.reconcile(ctx, rec)
	if ctx.Err() != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cleanupErr := r.cancelRecord(cleanupCtx, &rec); cleanupErr != nil {
			return rec.Execution, fmt.Errorf("start interrupted; cleanup needs recovery: %w", cleanupErr)
		}
		return rec.Execution, ctx.Err()
	}
	return execution, reconcileErr
}
func outcomes(role string) string {
	switch role {
	case "dev":
		return "changed, no_change, blocked"
	case "review":
		return "approved, changes_requested, blocked"
	case "merge":
		return "merged, blocked"
	case "init":
		return "ready, needs_input, blocked"
	}
	return ""
}

type container struct {
	State struct {
		Status     string
		Running    bool
		ExitCode   int
		FinishedAt time.Time
	}
	Config struct {
		Env    []string
		Labels map[string]string
	}
}

func (r *Runtime) inspectContainer(ctx context.Context, id string) (container, error) {
	var c container
	b, err := r.run(ctx, []string{"inspect", id}, nil)
	if err != nil {
		if strings.TrimSpace(string(b)) == "[]" {
			return c, os.ErrNotExist
		}
		return c, errors.New("docker inspect failed")
	}
	var list []container
	if err = json.Unmarshal(b, &list); err != nil || len(list) != 1 {
		return c, errors.New("invalid docker inspect response")
	}
	return list[0], nil
}
func (r *Runtime) Inspect(ctx context.Context, id string) (domain.Execution, error) {
	unlock, err := r.lock(ctx)
	if err != nil {
		return domain.Execution{}, err
	}
	defer unlock()
	rec, err := r.read(id)
	if err != nil {
		return domain.Execution{}, err
	}
	if terminal(rec.Execution.Status) {
		return rec.Execution, nil
	}
	return r.reconcile(ctx, rec)
}
func (r *Runtime) reconcile(ctx context.Context, rec record) (domain.Execution, error) {
	c, err := r.inspectContainer(ctx, rec.Execution.ContainerID)
	if err != nil {
		return rec.Execution, err
	}
	if c.Config.Labels["swarmjet.execution"] != rec.Execution.ID {
		return rec.Execution, errors.New("container ownership mismatch")
	}
	if rec.Timeout > 0 && time.Since(rec.StartedAt) >= time.Duration(rec.Timeout)*time.Second && (c.State.Running || c.State.Status == "created" || c.State.FinishedAt.After(rec.StartedAt.Add(time.Duration(rec.Timeout)*time.Second))) {
		if err = r.finishStopped(ctx, &rec, c, "timed_out"); err != nil {
			return rec.Execution, err
		}
		return rec.Execution, nil
	}
	if c.State.Status == "created" {
		if _, err = r.run(ctx, []string{"start", rec.Execution.ContainerID}, nil); err != nil {
			return rec.Execution, fmt.Errorf("cannot start %s; ensure its CLI is installed in the task image (AGENT_CLIS) and executable", rec.Adapter)
		}
		rec.Execution.Status = "running"
		return rec.Execution, r.save(rec)
	}
	secrets, err := r.collectLogs(ctx, &rec, c)
	if err != nil {
		return rec.Execution, err
	}
	if c.State.Running {
		rec.Execution.Status = "running"
	} else {
		rec.Execution.ExitCode = c.State.ExitCode
		rec.Execution.Status = "failed"
		rec.Execution.FailureReason = fmt.Sprintf("agent %s process exited with code %d; inspect execution logs for configuration or authentication errors", rec.Adapter, c.State.ExitCode)
		if c.State.ExitCode == 127 {
			rec.Execution.FailureReason = "Agent CLI is not installed in the task image; rebuild with the selected AGENT_CLIS"
		}
		if c.State.ExitCode == 0 && rec.CLIFailure != "" {
			rec.Execution.FailureReason = rec.CLIFailure
		}
		if c.State.ExitCode == 0 && rec.CLIFailure == "" {
			result, resultErr := ReadResult(rec.ResultPath, rec.Role)
			if resultErr != nil {
				rec.Execution.FailureReason = "invalid result.json: " + redact(resultErr.Error(), secrets)
				rec.Execution.FailureCode = "invalid_result"
			}
			if resultErr == nil {
				// Treat agent-authored results as untrusted output as well as logs.
				result.Summary = redact(result.Summary, secrets)
				result.NeedsHelp = redact(result.NeedsHelp, secrets)
				for i := range result.Assumptions {
					result.Assumptions[i] = redact(result.Assumptions[i], secrets)
				}
				for i := range result.Findings {
					result.Findings[i].RepositoryID = redact(result.Findings[i].RepositoryID, secrets)
					result.Findings[i].Path = redact(result.Findings[i].Path, secrets)
					result.Findings[i].Message = redact(result.Findings[i].Message, secrets)
				}
				// The downloadable structured artifact must match the redacted metadata.
				artifact, marshalErr := json.Marshal(result)
				if marshalErr != nil {
					return rec.Execution, marshalErr
				}
				if err = atomicWrite(rec.ResultPath, artifact); err != nil {
					return rec.Execution, err
				}
				rec.Execution.Result = &result
				rec.Execution.Status = "succeeded"
				rec.Execution.FailureReason = ""
				rec.Execution.FailureCode = ""
			}
		}
	}
	return rec.Execution, r.save(rec)
}
func (r *Runtime) collectLogs(ctx context.Context, rec *record, c container) ([]string, error) {
	profileValues, err := r.profileSecrets(*rec)
	if err != nil {
		return nil, err
	}
	logs, err := r.run(ctx, []string{"logs", "--tail", "10000", rec.Execution.ContainerID}, nil)
	if err != nil {
		return nil, errors.New("docker logs failed")
	}
	secrets := append([]string{}, profileValues...)
	for _, entry := range c.Config.Env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && v != "" && secretEnvironment(k) {
			for _, name := range rec.EnvNames {
				if k == name {
					secrets = append(secrets, v)
				}
			}
		}
	}
	clean, session := ParseJSONL(logs, secrets)
	if rec.Adapter != "" {
		adapter, err := adapterFor(rec.Adapter)
		if err != nil {
			return nil, err
		}
		session = adapter.session(logs)
		rec.CLIFailure = adapter.terminalFailure(logs)
	}
	if session != "" {
		rec.Execution.SessionID = session
	}
	if err = atomicWrite(rec.Execution.LogPath, clean); err != nil {
		return nil, err
	}
	return secrets, nil
}
func (r *Runtime) stop(ctx context.Context, id string) error {
	_, err := r.run(ctx, []string{"stop", "--time", "10", id}, nil)
	if err != nil {
		_, err = r.run(ctx, []string{"kill", id}, nil)
	}
	if err != nil {
		return errors.New("unable to stop agent container")
	}
	return nil
}
func (r *Runtime) Cancel(ctx context.Context, id string) error {
	unlock, err := r.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	rec, err := r.read(id)
	if err != nil {
		return err
	}
	if terminal(rec.Execution.Status) {
		return nil
	}
	return r.cancelRecord(ctx, &rec)
}

// safeDirectory rejects symlinks in every ancestor before creating or mounting a directory.
func safeDirectory(path string, create bool) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if strings.ContainsAny(abs, ",\n\r") {
		return errors.New("unsupported mount path")
	}
	current := string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, current), string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && create {
			if err = os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("mount directory or ancestor is not a real directory")
		}
	}
	return nil
}
func bindMount(path string, readOnly bool) string {
	mount := "type=bind,src=" + path + ",dst=" + path
	if readOnly {
		mount += ",readonly"
	}
	return mount
}
func (r *Runtime) cancelRecord(ctx context.Context, rec *record) error {
	c, err := r.inspectContainer(ctx, rec.Execution.ContainerID)
	if errors.Is(err, os.ErrNotExist) && rec.Execution.Status == "starting" {
		rec.Execution.Status = "cancelled"
		return r.save(*rec)
	}
	if err != nil {
		return err
	}
	return r.finishStopped(ctx, rec, c, "cancelled")
}
func (r *Runtime) finishStopped(ctx context.Context, rec *record, c container, status string) error {
	if c.Config.Labels["swarmjet.execution"] != rec.Execution.ID {
		return errors.New("container ownership mismatch")
	}
	// Created containers cannot be stopped; collect their empty logs then remove them.
	if c.State.Running {
		if err := r.stop(ctx, rec.Execution.ContainerID); err != nil {
			return err
		}
	}
	if _, err := r.collectLogs(ctx, rec, c); err != nil {
		return err
	}
	if c.State.Status == "created" {
		if _, err := r.run(ctx, []string{"rm", "--force", rec.Execution.ContainerID}, nil); err != nil {
			return errors.New("unable to remove unstarted agent container")
		}
	} else {
		stopped, err := r.inspectContainer(ctx, rec.Execution.ContainerID)
		if err != nil {
			return err
		}
		if stopped.State.Running {
			return errors.New("agent container is still running")
		}
		rec.Execution.ExitCode = stopped.State.ExitCode
	}
	rec.Execution.Status = status
	return r.save(*rec)
}

// ParseJSONL scans bounded event lines, retains redacted logs, and recovers the exact session.
func ParseJSONL(raw []byte, secrets []string) ([]byte, string) {
	text := string(raw)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	text = redact(text, secrets)
	// Also redact JSON-escaped credentials (quotes, newlines, backslashes).
	for _, secret := range secrets {
		encoded, _ := json.Marshal(secret)
		if len(encoded) > 2 {
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		}
	}
	// Parse the original event stream; redaction must never alter resumable session IDs or JSON syntax.
	s := bufio.NewScanner(bytes.NewReader(raw))
	s.Buffer(make([]byte, 4096), 1<<20)
	session := ""
	for s.Scan() {
		var event map[string]any
		if json.Unmarshal(s.Bytes(), &event) != nil {
			continue
		}
		for _, key := range []string{"sessionID", "sessionId", "session_id"} {
			if id, ok := event[key].(string); ok && id != "" {
				session = id
			}
		}
		if part, ok := event["part"].(map[string]any); ok {
			if id, ok := part["sessionID"].(string); ok && id != "" {
				session = id
			}
		}
	}
	return []byte(text), session
}

func secretEnvironment(name string) bool {
	switch name {
	case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GITLAB_HOST", "GITLAB_URI", "GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "GLAB_CONFIG_DIR":
		return false
	default:
		return true
	}
}
func ReadResult(path, role string) (domain.AgentResult, error) {
	var result domain.AgentResult
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return result, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return result, err
	}
	if len(b) > 1<<20 {
		return result, errors.New("result exceeds size limit")
	}
	var wire struct {
		domain.AgentResult
		NeedsHelp       json.RawMessage `json:"needsHelp"`
		HumanAcceptance json.RawMessage `json:"humanAcceptance"`
	}
	if err = json.Unmarshal(b, &wire); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return result, fmt.Errorf("field %s must be %s", typeErr.Field, typeErr.Type)
		}
		return result, errors.New("expected a valid JSON object")
	}
	result = wire.AgentResult
	acceptance := bytes.TrimSpace(wire.HumanAcceptance)
	if len(acceptance) > 0 && !bytes.Equal(acceptance, []byte("null")) {
		var guide domain.ManualAcceptance
		if parseErr := json.Unmarshal(acceptance, &guide); parseErr == nil {
			result.HumanAcceptance = &guide
		} else if result.Outcome != "blocked" {
			var typeErr *json.UnmarshalTypeError
			if errors.As(parseErr, &typeErr) {
				return result, fmt.Errorf("field humanAcceptance.%s must be %s", typeErr.Field, typeErr.Type)
			}
			return result, errors.New("humanAcceptance must be a valid object")
		}
		// A blocked result has no completed change to validate manually. Preserve
		// its needsHelp instead of masking the obstacle with a malformed guide.
	}
	// Older prompts did not state the type, so models commonly used false for
	// "no help needed". Normalize only that unambiguous value; true is not an
	// explanation and blocked outcomes still require a non-empty string below.
	help := bytes.TrimSpace(wire.NeedsHelp)
	if len(help) > 0 && !bytes.Equal(help, []byte("false")) {
		if err = json.Unmarshal(help, &result.NeedsHelp); err != nil {
			return result, errors.New("needsHelp must be a string; use an empty string when no help is needed")
		}
	}
	valid := false
	for _, outcome := range strings.Split(outcomes(role), ", ") {
		if result.Outcome == outcome && outcome != "" {
			valid = true
		}
	}
	if !valid || strings.TrimSpace(result.Summary) == "" {
		return result, fmt.Errorf("invalid %s result", role)
	}
	if result.Outcome == "blocked" && strings.TrimSpace(result.NeedsHelp) == "" {
		return result, errors.New("blocked result requires needsHelp")
	}
	return result, nil
}

func redact(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
}
