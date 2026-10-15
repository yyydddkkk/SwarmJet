// Package integration runs validated Docker Compose stacks scoped to one task.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/networkconfig"
	"time"
)

type Executor func(context.Context, string, []string, []string) ([]byte, int, error)
type Manager struct {
	DataDir   string
	Execute   Executor
	Heartbeat func(context.Context, string)
	Env       map[string]string
}

func New(dataDir string) *Manager {
	a, _ := filepath.Abs(dataDir)
	return &Manager{DataDir: a, Execute: execute}
}
func execute(ctx context.Context, dir string, args, env []string) ([]byte, int, error) {
	c := exec.CommandContext(ctx, "docker", args...)
	c.Dir = dir
	c.Env = env
	b, e := c.CombinedOutput()
	code := 0
	if e != nil {
		code = -1
		var x *exec.ExitError
		if errors.As(e, &x) {
			code = x.ExitCode()
		}
	}
	return b, code, e
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
var safeService = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func projectName(id string) (string, error) {
	if !safeID.MatchString(id) || id != strings.ToLower(id) {
		return "", fmt.Errorf("unsafe task ID %q", id)
	}
	return "sj-" + strings.ToLower(id), nil
}
func within(root, p string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}
func resolve(root, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	abs, e := filepath.Abs(p)
	if e != nil {
		return "", e
	}
	for q := abs; ; q = filepath.Dir(q) {
		resolved, err := filepath.EvalSymlinks(q)
		if err == nil {
			tail, _ := filepath.Rel(q, abs)
			return filepath.Join(resolved, tail), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if q == filepath.Dir(q) {
			return "", err
		}
	}
}
func (m *Manager) environment(ws domain.Workspace) []string {
	env := []string{}
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range m.Env {
		env = append(env, k+"="+v)
	}
	return append(env, "SWARMJET_TASK_ID="+ws.TaskID, "SWARMJET_WORKSPACE="+ws.Root)
}
func (m *Manager) setup(ws domain.Workspace, cfg domain.IntegrationConfig) ([]string, string, error) {
	name, e := projectName(ws.TaskID)
	if e != nil {
		return nil, "", e
	}
	root, e := filepath.EvalSymlinks(ws.Root)
	if e != nil {
		return nil, "", e
	}
	root, e = filepath.Abs(root)
	if e != nil {
		return nil, "", e
	}
	if cfg.ComposeFile == "" {
		return nil, "", errors.New("integration compose file is required")
	}
	file, e := resolve(root, cfg.ComposeFile)
	if e != nil {
		return nil, "", e
	}
	if !within(root, file) {
		return nil, "", errors.New("compose file escapes task workspace")
	}
	if _, e = os.Stat(file); e != nil {
		return nil, "", e
	}
	return []string{"compose", "--project-name", name, "--project-directory", root, "-f", file}, root, nil
}
func (m *Manager) run(ctx context.Context, ws domain.Workspace, dir string, args []string) ([]byte, int, error) {
	if m.Heartbeat != nil {
		m.Heartbeat(ctx, "docker compose running")
	}
	done := make(chan struct{})
	if m.Heartbeat != nil {
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					m.Heartbeat(ctx, "docker compose running")
				}
			}
		}()
	}
	defer close(done)
	run := m.Execute
	if run == nil {
		run = execute
	}
	return run(ctx, dir, args, m.environment(ws))
}
func (m *Manager) report(ws domain.Workspace, name string, b []byte) (string, error) {
	b = []byte(networkconfig.Redact(string(b), m.Env))
	if _, e := projectName(ws.TaskID); e != nil {
		return "", e
	}
	dir := filepath.Join(m.DataDir, "runs", ws.TaskID, "integration")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	p := filepath.Join(dir, name)
	f, e := os.CreateTemp(dir, ".report-")
	if e != nil {
		return p, e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return p, e
	}
	return p, os.Rename(f.Name(), p)
}

type composeConfig struct {
	Services map[string]service      `json:"services"`
	Networks map[string]resource     `json:"networks"`
	Volumes  map[string]resource     `json:"volumes"`
	Configs  map[string]fileResource `json:"configs"`
	Secrets  map[string]fileResource `json:"secrets"`
}
type resource struct {
	Name       string         `json:"name"`
	External   any            `json:"external"`
	Driver     string         `json:"driver"`
	DriverOpts map[string]any `json:"driver_opts"`
}
type fileResource struct {
	File     string `json:"file"`
	External any    `json:"external"`
}
type service struct {
	ContainerName string   `json:"container_name"`
	NetworkMode   string   `json:"network_mode"`
	PID           string   `json:"pid"`
	IPC           string   `json:"ipc"`
	Privileged    bool     `json:"privileged"`
	Devices       []any    `json:"devices"`
	VolumesFrom   []string `json:"volumes_from"`
	Ports         []struct {
		Published any `json:"published"`
	} `json:"ports"`
	Volumes []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"volumes"`
	Build any `json:"build"`
}

func external(x any) bool {
	if x == nil {
		return false
	}
	if b, ok := x.(bool); ok {
		return b
	}
	return true
}
func (m *Manager) validate(ws domain.Workspace, root string, b []byte) (composeConfig, error) {
	var c composeConfig
	if e := json.Unmarshal(b, &c); e != nil {
		return c, fmt.Errorf("invalid compose config JSON: %w", e)
	}
	if len(c.Services) == 0 {
		return c, errors.New("compose has no services")
	}
	name, _ := projectName(ws.TaskID)
	runs := filepath.Join(m.DataDir, "runs", ws.TaskID, "integration")
	runs, _ = resolve(m.DataDir, runs)
	allowed := func(src string) bool {
		p, e := resolve(root, src)
		return e == nil && (within(root, p) || within(runs, p))
	}
	for kind, rs := range map[string]map[string]resource{"network": c.Networks, "volume": c.Volumes} {
		for key, r := range rs {
			if external(r.External) || r.Name != "" && r.Name != name+"_"+key {
				return c, fmt.Errorf("%s %s must be task scoped and non-external", kind, key)
			}
			if len(r.DriverOpts) > 0 {
				return c, fmt.Errorf("%s %s custom driver options forbidden", kind, key)
			}
			if r.Driver != "" && r.Driver != "local" && r.Driver != "bridge" {
				return c, fmt.Errorf("unsupported shared resource driver %s", r.Driver)
			}
		}
	}
	for _, resources := range []map[string]fileResource{c.Configs, c.Secrets} {
		for key, r := range resources {
			if external(r.External) || r.File != "" && !allowed(r.File) {
				return c, fmt.Errorf("config/secret %s escapes task", key)
			}
		}
	}
	for key, s := range c.Services {
		if !safeService.MatchString(key) {
			return c, fmt.Errorf("unsafe service %q", key)
		}
		if s.ContainerName != "" || s.NetworkMode != "" && s.NetworkMode != "none" || s.PID != "" || s.IPC != "" || s.Privileged || len(s.Devices) > 0 || len(s.VolumesFrom) > 0 {
			return c, fmt.Errorf("service %s uses shared or privileged host configuration", key)
		}
		for _, p := range s.Ports {
			v := fmt.Sprint(p.Published)
			if p.Published != nil && v != "" && v != "0" {
				return c, fmt.Errorf("service %s has fixed host port %s", key, v)
			}
		}
		for _, v := range s.Volumes {
			if strings.Contains(v.Source, "docker.sock") || strings.Contains(v.Target, "docker.sock") {
				return c, errors.New("docker socket mount forbidden")
			}
			switch v.Type {
			case "bind":
				if !allowed(v.Source) {
					return c, fmt.Errorf("service %s bind mount escapes task: %s", key, v.Source)
				}
			case "volume":
				if v.Source != "" {
					if _, ok := c.Volumes[v.Source]; !ok {
						return c, fmt.Errorf("undeclared volume %s", v.Source)
					}
				}
			case "tmpfs":
			default:
				return c, fmt.Errorf("unsupported mount type %s", v.Type)
			}
		}
		if s.Build != nil {
			var contextPath string
			switch v := s.Build.(type) {
			case string:
				contextPath = v
			case map[string]any:
				contextPath, _ = v["context"].(string)
				if lenMap, ok := v["additional_contexts"]; ok && lenMap != nil {
					return c, errors.New("additional build contexts are unsupported")
				}
			}
			if contextPath != "" && !allowed(contextPath) {
				return c, fmt.Errorf("build context for %s escapes task", key)
			}
		}
	}
	return c, nil
}
func (m *Manager) checked(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig) ([]string, string, composeConfig, error) {
	args, root, e := m.setup(ws, cfg)
	if e != nil {
		return nil, "", composeConfig{}, e
	}
	b, code, e := m.run(ctx, ws, root, append(append([]string{}, args...), "config", "--format", "json"))
	if e != nil || code != 0 {
		_, _ = m.report(ws, "config-error.log", b)
		return nil, "", composeConfig{}, fmt.Errorf("compose config failed (exit %d): %w", code, e)
	}
	c, e := m.validate(ws, root, b)
	return args, root, c, e
}
func (m *Manager) Start(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig) error {
	if cfg.Mode == "commands" {
		return nil
	}

	timeout := cfg.StartupTimeoutSeconds
	if timeout <= 0 {
		timeout = 180
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout+30)*time.Second)
	defer cancel()
	args, root, _, e := m.checked(ctx, ws, cfg)
	if e != nil {
		return e
	}
	if e = os.Remove(m.stopMarker(ws.TaskID)); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	args = append(args, "up", "-d", "--wait", "--wait-timeout", strconv.Itoa(timeout))
	b, code, e := m.run(ctx, ws, root, args)
	_, saveErr := m.report(ws, "startup.log", append([]byte(fmt.Sprintf("exit_code=%d\n", code)), b...))
	if e != nil || code != 0 {
		return fmt.Errorf("compose startup failed (exit %d), stack retained for diagnosis: %v", code, e)
	}
	return saveErr
}
func (m *Manager) Verify(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig) ([]domain.Verification, error) {
	if cfg.Mode == "commands" {
		return m.verifyCommands(ctx, ws, cfg)
	}

	args, root, c, e := m.checked(ctx, ws, cfg)
	if e != nil {
		return nil, e
	}
	var results []domain.Verification
	required := false
	for i, ch := range cfg.Checks {
		required = required || ch.Required
		v := domain.Verification{Name: ch.Name, Required: ch.Required, ExitCode: -1}
		var output []byte
		_, ok := c.Services[ch.Service]
		if ch.Name == "" || !ok || len(ch.Command) == 0 {
			output = []byte("invalid check: name, existing service, and non-empty argv required\n")
		} else {
			cmd := append(append([]string{}, args...), "exec", "-T", ch.Service)
			cmd = append(cmd, ch.Command...)
			b, code, err := m.run(ctx, ws, root, cmd)
			output = b
			v.ExitCode = code
			v.Passed = err == nil && code == 0
			if err != nil {
				output = append(output, []byte("\n"+err.Error())...)
			}
		}
		output = append([]byte(fmt.Sprintf("exit_code=%d\n", v.ExitCode)), output...)
		v.Report, e = m.report(ws, fmt.Sprintf("check-%03d.log", i), output)
		if e != nil {
			return results, e
		}
		results = append(results, v)
	}
	if !required {
		return results, errors.New("at least one required integration check must be configured")
	}
	b, _ := json.MarshalIndent(results, "", "  ")
	if _, e = m.report(ws, "verification.json", b); e != nil {
		return results, e
	}
	return results, nil
}
func (m *Manager) stopMarker(taskID string) string {
	return filepath.Join(m.DataDir, "integration-state", taskID+".stopped")
}
func (m *Manager) Stop(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig) error {
	if ws.Root == "" {
		return nil
	}
	if cfg.Mode == "commands" || cfg.ComposeFile == "" {
		return m.stopCommands(ctx, ws)
	}

	if ws.Root == "" {
		return nil
	}
	if _, e := projectName(ws.TaskID); e != nil {
		return e
	}
	if _, e := os.Stat(m.stopMarker(ws.TaskID)); e == nil {
		return nil
	}
	args, root, _, e := m.checked(ctx, ws, cfg)
	if e != nil {
		return e
	}
	b, code, e := m.run(ctx, ws, root, append(args, "down", "--volumes"))
	_, saveErr := m.report(ws, "shutdown.log", append([]byte(fmt.Sprintf("exit_code=%d\n", code)), b...))
	if e != nil || code != 0 {
		return fmt.Errorf("compose shutdown failed (exit %d): %v", code, e)
	}
	if e = os.MkdirAll(filepath.Dir(m.stopMarker(ws.TaskID)), 0700); e != nil {
		return e
	}
	if e = os.WriteFile(m.stopMarker(ws.TaskID), []byte("stopped\n"), 0600); e != nil {
		return e
	}
	return saveErr
}
