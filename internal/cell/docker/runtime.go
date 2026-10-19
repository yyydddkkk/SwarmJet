// Package docker implements the Cell runtime and Publisher on top of Docker.
//
// A Cell is one long-lived container per Agent plus two persistent locations:
//
//   - the workspace, a host bind directory at <DATA_DIR>/cells/<agent-id>/workspace
//     mounted at /workspace. The host clones it once; after that, branch
//     selection and publishing run git inside the container;
//   - the CLI data volume, a named volume mounted at /cell, holding HOME and
//     the XDG directories (OpenCode's session database lives here).
//
// The Publisher keeps a platform-owned bare mirror at
// <DATA_DIR>/cells/<agent-id>/mirror.git. It is never mounted into the Cell:
// the Cell produces a bundle, and only the host mirror receives and pushes it.
//
// Containers are named swarmjet-cell-<agent-id> and carry the swarmjet.cell,
// swarmjet.agent and swarmjet.hive labels for lookup and cleanup. The CLI data
// volume is named swarmjet-cell-<agent-id>-data; the data volume outlives the
// container so a parked or hibernated Agent keeps its CLI session.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/agent"
	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/acp"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const (
	cellWorkdir   = "/workspace"
	cellDataMount = "/cell"
)

// Runtime implements cell.Runtime with Docker on the platform host.
type Runtime struct {
	DataDir string
	// Config carries the same image, network and resource limits as the legacy
	// internal/agent runtime (domain.AgentConfig).
	Config       domain.AgentConfig
	DockerBinary string
	Exec         Executor
	Dialer       Dialer
	Cell         CellRunner
	Workspace    Workspace
	// Git and Tokens are used by the default GitWorkspace.
	Git    *Git
	Tokens TokenSource
	Logger *slog.Logger

	mu       sync.Mutex
	sessions map[string]*Session
	locks    map[string]*sync.Mutex
}

var _ cell.Runtime = (*Runtime)(nil)

// NewRuntime creates a runtime rooted at dataDir. The image defaults to
// AGENT_IMAGE, matching the deployment configuration.
func NewRuntime(dataDir string) *Runtime {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	image := os.Getenv("AGENT_IMAGE")
	if image == "" {
		image = "swarmjet-agent:local"
	}
	return &Runtime{
		DataDir:      abs,
		Config:       domain.AgentConfig{Image: image},
		DockerBinary: "docker",
		Git:          &Git{},
		sessions:     map[string]*Session{},
	}
}

func (r *Runtime) executor() Executor {
	if r.Exec != nil {
		return r.Exec
	}
	return execExecutor{Binary: r.DockerBinary}
}

func (r *Runtime) dialer() Dialer {
	if r.Dialer != nil {
		return r.Dialer
	}
	return &DockerDialer{Binary: r.DockerBinary}
}

func (r *Runtime) cellRunner() CellRunner {
	if r.Cell != nil {
		return r.Cell
	}
	return &DockerCellRunner{Binary: r.DockerBinary}
}

func (r *Runtime) git() *Git {
	if r.Git != nil {
		return r.Git
	}
	return &Git{}
}

func (r *Runtime) workspace() Workspace {
	if r.Workspace != nil {
		return r.Workspace
	}
	git := r.Git
	if git == nil {
		git = &Git{}
	}
	return &GitWorkspace{Git: git, Tokens: r.Tokens}
}

func (r *Runtime) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// agentLock returns the per-Agent mutex so operations on different Cells can
// run concurrently while one Agent's Start/Stop/Remove/Destroy stay ordered.
func (r *Runtime) agentLock(agentID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks == nil {
		r.locks = map[string]*sync.Mutex{}
	}
	lock := r.locks[agentID]
	if lock == nil {
		lock = &sync.Mutex{}
		r.locks[agentID] = lock
	}
	return lock
}

func (r *Runtime) session(agentID string) *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[agentID]
}

func (r *Runtime) setSession(agentID string, session *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*Session{}
	}
	r.sessions[agentID] = session
}

func (r *Runtime) deleteSession(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, agentID)
}

func containerName(agentID string) string { return "swarmjet-cell-" + agentID }
func volumeName(agentID string) string    { return "swarmjet-cell-" + agentID + "-data" }

// Start creates or restarts the Agent's container, prepares the workspace,
// starts the CLI and creates or loads its session.
func (r *Runtime) Start(ctx context.Context, spec cell.Spec) (cell.Session, error) {
	if err := validateSpec(spec, r.Config.Image); err != nil {
		return nil, err
	}
	secrets := specSecrets(spec.Env, spec.MCPToken)

	lock := r.agentLock(spec.AgentID)
	lock.Lock()
	defer lock.Unlock()

	if old := r.session(spec.AgentID); old != nil {
		_ = old.Close()
		r.deleteSession(spec.AgentID)
	}

	workspace := WorkspaceDir(r.DataDir, spec.AgentID)
	fresh, err := r.workspace().EnsureCloned(ctx, spec, workspace)
	if err != nil {
		return nil, fmt.Errorf("cell: prepare workspace: %s", redact(err.Error(), secrets...))
	}

	name := containerName(spec.AgentID)
	running, exists, err := r.containerState(ctx, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := r.createContainer(ctx, spec, workspace); err != nil {
			return nil, err
		}
	}
	if !running {
		res, err := r.docker(ctx, nil, "start", name)
		if err != nil {
			return nil, err
		}
		if res.ExitCode != 0 {
			return nil, fmt.Errorf("cell: start container: %s", redact(strings.TrimSpace(string(res.Stderr)), secrets...))
		}
	}
	if err := r.prepareCell(ctx, spec, fresh); err != nil {
		return nil, fmt.Errorf("cell: prepare branch in container: %s", redact(err.Error(), secrets...))
	}

	env, err := r.cellEnv(spec)
	if err != nil {
		return nil, err
	}
	session := &Session{now: time.Now}
	client, err := r.dialer().Dial(ctx, name, cellWorkdir, env, cliArgv(spec), session.permission)
	if err != nil {
		return nil, fmt.Errorf("cell: start CLI: %s", redact(err.Error(), secrets...))
	}
	session.client = client

	handshake, err := client.Initialize(ctx)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("cell: initialize CLI: %s", redact(err.Error(), secrets...))
	}
	servers := mcpServers(spec)
	switch {
	case spec.SessionRef == "":
		id, err := newSession(ctx, client, servers)
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("cell: create session: %s", redact(err.Error(), secrets...))
		}
		session.id = id
	case !handshake.AgentCapabilities.LoadSession:
		r.logger().Warn("cell: CLI does not support session/load; starting a new session", "agent", spec.AgentID)
		id, err := newSession(ctx, client, servers)
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("cell: create session: %s", redact(err.Error(), secrets...))
		}
		session.id = id
	default:
		// Replay is delivered to the session sink, which has no consumer before
		// the first Prompt, so it is intentionally discarded.
		if _, err := client.LoadSession(ctx, spec.SessionRef, cellWorkdir, servers, session.onUpdate); err == nil {
			session.id = spec.SessionRef
		} else {
			r.logger().Warn("cell: session/load failed; starting a new session",
				"agent", spec.AgentID, "error", redact(err.Error(), secrets...))
			id, newErr := newSession(ctx, client, servers)
			if newErr != nil {
				_ = client.Close()
				return nil, fmt.Errorf("cell: recover from failed session/load: %s", redact(newErr.Error(), secrets...))
			}
			session.id = id
		}
	}
	if session.id == "" {
		_ = client.Close()
		return nil, errors.New("cell: CLI returned an empty session ID")
	}
	r.setSession(spec.AgentID, session)
	return session, nil
}

func newSession(ctx context.Context, client ACPClient, servers []acp.MCPServer) (string, error) {
	res, err := client.NewSession(ctx, cellWorkdir, servers)
	if err != nil {
		return "", err
	}
	if res.SessionID == "" {
		return "", errors.New("empty session id")
	}
	return res.SessionID, nil
}

// Stop closes the CLI and stops the container. Volumes and the workspace stay.
func (r *Runtime) Stop(ctx context.Context, agentID string) error {
	if err := validAgentID(agentID); err != nil {
		return err
	}
	lock := r.agentLock(agentID)
	lock.Lock()
	defer lock.Unlock()
	return r.stop(ctx, agentID)
}

func (r *Runtime) stop(ctx context.Context, agentID string) error {
	if session := r.session(agentID); session != nil {
		_ = session.Close()
		r.deleteSession(agentID)
	}
	res, err := r.docker(ctx, nil, "stop", "--time", "10", containerName(agentID))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && !notFound(res.Stderr) {
		return fmt.Errorf("cell: stop container: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// Remove deletes the container and keeps the volumes (hibernated).
func (r *Runtime) Remove(ctx context.Context, agentID string) error {
	if err := validAgentID(agentID); err != nil {
		return err
	}
	lock := r.agentLock(agentID)
	lock.Lock()
	defer lock.Unlock()
	if err := r.stop(ctx, agentID); err != nil {
		return err
	}
	return r.removeContainer(ctx, agentID)
}

// Destroy deletes the container, the CLI volume and the workspace directory.
func (r *Runtime) Destroy(ctx context.Context, agentID string) error {
	if err := validAgentID(agentID); err != nil {
		return err
	}
	lock := r.agentLock(agentID)
	lock.Lock()
	defer lock.Unlock()
	if err := r.stop(ctx, agentID); err != nil {
		return err
	}
	if err := r.removeContainer(ctx, agentID); err != nil {
		return err
	}
	res, err := r.docker(ctx, nil, "volume", "rm", "-f", volumeName(agentID))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && !notFound(res.Stderr) {
		return fmt.Errorf("cell: remove volume: %s", strings.TrimSpace(string(res.Stderr)))
	}
	if err := r.removeWorkspace(agentID); err != nil {
		return err
	}
	return nil
}

func (r *Runtime) removeContainer(ctx context.Context, agentID string) error {
	res, err := r.docker(ctx, nil, "rm", "-f", containerName(agentID))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && !notFound(res.Stderr) {
		return fmt.Errorf("cell: remove container: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func (r *Runtime) removeWorkspace(agentID string) error {
	root := filepath.Join(r.DataDir, "cells")
	dir := filepath.Join(root, agentID)
	if err := validateWorkspacePath(root, dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("cell: remove workspace: %w", err)
	}
	return nil
}

func (r *Runtime) containerState(ctx context.Context, name string) (running, exists bool, err error) {
	res, err := r.docker(ctx, nil, "inspect", "--type", "container", "--format", "{{.State.Running}}", name)
	if err != nil {
		return false, false, err
	}
	if res.ExitCode != 0 {
		return false, false, nil
	}
	return strings.TrimSpace(string(res.Stdout)) == "true", true, nil
}

func (r *Runtime) createContainer(ctx context.Context, spec cell.Spec, workspace string) error {
	secrets := specSecrets(spec.Env, spec.MCPToken)
	volume := volumeName(spec.AgentID)
	if err := r.ensureVolume(ctx, spec, volume); err != nil {
		return err
	}
	name := containerName(spec.AgentID)
	args := []string{
		"create",
		"--name", name,
		"--label", "swarmjet.cell=true",
		"--label", "swarmjet.agent=" + spec.AgentID,
		"--label", "swarmjet.hive=" + spec.HiveID,
		"--workdir", cellWorkdir,
		"--mount", "type=bind,src=" + workspace + ",dst=" + cellWorkdir,
		"--mount", "type=volume,src=" + volume + ",dst=" + cellDataMount,
	}
	args = append(args, agent.ContainerPolicyArgs(r.Config)...)
	args = append(args, "--user", agent.ContainerUser())
	args = append(args, "--entrypoint", "/bin/sh", r.Config.Image, "-c", "exec sleep infinity")
	res, err := r.docker(ctx, nil, args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cell: create container: %s", redact(strings.TrimSpace(string(res.Stderr)), secrets...))
	}
	return nil
}

// ensureVolume creates the CLI data volume and hands it to the container user.
// Docker creates an empty volume owned by root; the CLI runs as the platform
// uid so it can write files that the platform can later read.
func (r *Runtime) ensureVolume(ctx context.Context, spec cell.Spec, volume string) error {
	secrets := specSecrets(spec.Env, spec.MCPToken)
	res, err := r.docker(ctx, nil, "volume", "inspect", volume)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		res, err = r.docker(ctx, nil, "volume", "create", volume)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("cell: create volume: %s", redact(strings.TrimSpace(string(res.Stderr)), secrets...))
		}
	}
	// Chown even when the volume already exists: an earlier create may have
	// failed between volume creation and ownership setup.
	res, err = r.docker(ctx, nil,
		"run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh",
		"--mount", "type=volume,src="+volume+",dst="+cellDataMount,
		r.Config.Image, "-c", "chown -R "+agent.ContainerUser()+" "+cellDataMount,
	)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cell: initialize volume: %s", redact(strings.TrimSpace(string(res.Stderr)), secrets...))
	}
	return nil
}

func (r *Runtime) cellEnv(spec cell.Spec) ([]string, error) {
	config := map[string]any{
		"model": spec.Model,
		"permission": map[string]string{
			"bash":     "ask",
			"edit":     "ask",
			"webfetch": "ask",
		},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("cell: encode CLI config: %w", err)
	}
	env := []string{
		"HOME=" + cellDataMount + "/home",
		"XDG_DATA_HOME=" + cellDataMount + "/data",
		"XDG_CONFIG_HOME=" + cellDataMount + "/config",
		"XDG_STATE_HOME=" + cellDataMount + "/state",
		"OPENCODE_DISABLE_AUTOUPDATE=true",
		"OPENCODE_CONFIG_CONTENT=" + string(raw),
	}
	keys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+spec.Env[key])
	}
	return env, nil
}

func mcpServers(spec cell.Spec) []acp.MCPServer {
	if spec.MCPURL == "" {
		return nil
	}
	headers := []acp.HTTPHeader{}
	if spec.MCPToken != "" {
		headers = append(headers, acp.HTTPHeader{Name: "Authorization", Value: "Bearer " + spec.MCPToken})
	}
	return []acp.MCPServer{{
		Name:    "swarmjet",
		Type:    "http",
		URL:     spec.MCPURL,
		Headers: headers,
	}}
}

func cliArgv(spec cell.Spec) []string {
	if spec.CLI == "" || spec.CLI == "opencode" {
		return []string{"opencode", "acp"}
	}
	return []string{spec.CLI, "acp"}
}

func (r *Runtime) docker(ctx context.Context, secrets []string, args ...string) (Result, error) {
	res, err := r.executor().Run(ctx, args, nil)
	if err != nil {
		verb := ""
		if len(args) > 0 {
			verb = args[0]
		}
		return res, fmt.Errorf("cell: docker %s: %s", verb, redact(err.Error(), secrets...))
	}
	return res, nil
}

func notFound(stderr []byte) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "no such")
}

var reservedEnv = map[string]bool{
	"HOME":                        true,
	"XDG_DATA_HOME":               true,
	"XDG_CONFIG_HOME":             true,
	"XDG_STATE_HOME":              true,
	"OPENCODE_DISABLE_AUTOUPDATE": true,
	"OPENCODE_CONFIG_CONTENT":     true,
}

func validateSpec(spec cell.Spec, image string) error {
	if err := validAgentID(spec.AgentID); err != nil {
		return err
	}
	if spec.HiveID == "" || strings.ContainsAny(spec.HiveID, "\x00\r\n") || len(spec.HiveID) > 128 {
		return errors.New("cell: invalid hive ID")
	}
	if err := validRepository(spec.Repository); err != nil {
		return err
	}
	if err := validBranch(spec.Branch); err != nil {
		return err
	}
	if spec.BaseBranch != "" {
		if err := validBranch(spec.BaseBranch); err != nil {
			return err
		}
	}
	if spec.CLI != "" && spec.CLI != "opencode" {
		return fmt.Errorf("cell: unsupported CLI %q", spec.CLI)
	}
	if spec.Model == "" || len(spec.Model) > 200 || strings.ContainsAny(spec.Model, "\x00\r\n") {
		return errors.New("cell: a pinned model is required")
	}
	if image == "" {
		return errors.New("cell: an agent image is required")
	}
	if spec.MCPURL != "" {
		u, err := url.Parse(spec.MCPURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return errors.New("cell: MCP URL must be an http(s) URL without credentials")
		}
	}
	if strings.ContainsAny(spec.MCPToken, "\x00\r\n") {
		return errors.New("cell: invalid MCP token")
	}
	for key := range spec.Env {
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("cell: invalid environment variable name %q", key)
		}
		if reservedEnv[key] {
			return fmt.Errorf("cell: environment variable %q is reserved by the runtime", key)
		}
	}
	return nil
}
