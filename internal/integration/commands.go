package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/networkconfig"
	"time"
)

func (m *Manager) stopCommands(ctx context.Context, ws domain.Workspace) error {
	name, err := projectName(ws.TaskID)
	if err != nil {
		return err
	}
	// Commands for one task execute serially in a named container.
	output, _, err := m.run(ctx, ws, ws.Root, []string{"rm", "-f", name + "-acceptance"})
	if err != nil && !strings.Contains(strings.ToLower(string(output)), "no such container") {
		return err
	}
	return nil
}
func (m *Manager) verifyCommands(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig) ([]domain.Verification, error) {
	name, err := projectName(ws.TaskID)
	if err != nil {
		return nil, err
	}
	if cfg.Image == "" || len(cfg.Commands) == 0 || len(cfg.Commands) > 20 {
		return nil, errors.New("验收镜像或命令未配置")
	}
	root, err := filepath.EvalSymlinks(ws.Root)
	if err != nil {
		return nil, err
	}
	results := []domain.Verification{}
	for i, ch := range cfg.Commands {
		if len(ch.Argv) == 0 || ch.Argv[0] == "" || ch.Name == "" || filepath.IsAbs(ch.Path) {
			return nil, errors.New("invalid acceptance command")
		}
		dir, err := resolve(root, filepath.Join(root, ch.Path))
		if err != nil || !within(root, dir) {
			return nil, errors.New("验收目录越界")
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return nil, errors.New("验收目录不存在")
		}
		timeout := cfg.StartupTimeoutSeconds
		if timeout <= 0 {
			timeout = 600
		}
		if timeout > 3600 {
			timeout = 3600
		}
		runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		cleanup := func() {
			c, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			_ = m.stopCommands(c, ws)
		}
		// Use only platform environment, never task GitLab or model credentials.
		if err = m.stopCommands(runCtx, ws); err != nil {
			cancel()
			return nil, err
		}
		// pnpm may install the repository-pinned CLI under HOME=/tmp and execute
		// it immediately. Docker's default tmpfs is noexec, so permit execution
		// only in this bounded, container-private temporary directory.
		// pnpm's generated .bin wrappers embed absolute NODE_PATH entries.
		// Mount at the Node's original path, matching development Runners.
		args := []string{"run", "--name", name + "-acceptance", "--read-only", "--tmpfs", "/tmp:exec,mode=1777,size=256m", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "--mount", "type=bind,src=" + root + ",dst=" + root, "--workdir", dir}
		for _, k := range networkconfig.Keys {
			if _, ok := m.Env[k]; ok {
				args = append(args, "--env", k)
			}
		}
		args = append(args, "--env", "HOME=/tmp", "--entrypoint", ch.Argv[0], cfg.Image)
		args = append(args, ch.Argv[1:]...)
		output, code, runErr := m.run(runCtx, ws, root, args)
		cancel()
		cleanup()
		if runErr != nil {
			output = append(output, []byte("\n"+runErr.Error())...)
		}
		if len(output) > 1<<20 {
			output = output[:1<<20]
		}
		argvJSON, _ := json.Marshal(ch.Argv)
		output = append([]byte(fmt.Sprintf("command=%s\ndirectory=%s\nevidence=%s\nexit_code=%d\n\n", argvJSON, ch.Path, ch.Evidence, code)), output...)
		report, err := m.report(ws, fmt.Sprintf("command-%03d.log", i), output)
		if err != nil {
			return results, err
		}
		results = append(results, domain.Verification{Name: ch.Name, Required: true, Passed: runErr == nil && code == 0, EnvironmentFailure: commandEnvironmentFailure(code, output), ExitCode: code, Report: report})
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
	}
	return results, nil
}

// Only known command-start and toolchain-loading failures are classified as
// environment failures. Ordinary assertions, type errors and lint violations
// still return to the developer for code rework.
func commandEnvironmentFailure(code int, output []byte) bool {
	if code == -1 || code == 125 || code == 126 || code == 127 {
		return true
	}
	message := strings.ToLower(string(output))
	return strings.Contains(message, "cannot find module") && (strings.Contains(message, "failed to load parser") || strings.Contains(message, "failed to load plugin"))
}
