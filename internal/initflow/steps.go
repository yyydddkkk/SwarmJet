package initflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/managedmeta"
)

const maxGeneratedFile = 256 << 10

var newline = string(byte(10))

// errVerificationUnavailable blocks verification when no isolated runtime is
// configured. Verification must never run as the worker process.
var errVerificationUnavailable = errors.New("未配置隔离验证环境（INIT_VERIFY_IMAGE），已阻断验证")

func (m *Manager) ask(ctx context.Context, init domain.Initialization, project domain.Project, question string) (domain.Initialization, error) {
	init.Questions = addQuestion(init.Questions, question)
	init.Status = domain.InitNeedsInput
	init.Message = question
	return m.save(ctx, init, project)
}

// connect verifies the connection, pins every repository revision and creates
// the managed Meta-Repo with its initial commit.
func (m *Manager) connect(ctx context.Context, init domain.Initialization, project domain.Project) (domain.Initialization, error) {
	client, err := m.forgeClient(project)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = err.Error()
		init.Message = "无法读取代码托管连接：" + err.Error()
		return m.save(ctx, init, project)
	}
	if m.Managed == nil {
		init.Status = domain.InitBlocked
		init.Error = "受管 Meta-Repo 组件未配置"
		return m.save(ctx, init, project)
	}
	shas := map[string]string{}
	for _, repo := range project.Repositories {
		if repo.ProjectID <= 0 || repo.URL == "" || repo.Path == "" {
			return m.ask(ctx, init, project, "仓库 "+repo.ID+" 缺少真实项目 ID、地址或路径，请在项目中补全后继续。")
		}
		if repo.TargetBranch == "" {
			return m.ask(ctx, init, project, "请为仓库 "+repo.ID+" 指定目标分支后继续。")
		}
		branch, err := client.GetBranch(ctx, repo.ProjectID, repo.TargetBranch)
		if err != nil || branch.Commit.ID == "" {
			return m.ask(ctx, init, project, "仓库 "+repo.ID+" 的目标分支 "+repo.TargetBranch+" 不可读；请确认分支存在或设置创建起点后重试。")
		}
		shas[repo.ID] = branch.Commit.ID
	}
	files, err := managedmeta.Files(project.Name, project.Repositories)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = "仓库清单无效：" + err.Error()
		init.Message = init.Error
		return m.save(ctx, init, project)
	}
	sha, err := m.Managed.Ensure(ctx, project.ID, project.MetaRef, files)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = "无法创建受管 Meta-Repo：" + err.Error()
		init.Message = init.Error
		return m.save(ctx, init, project)
	}
	init.RepositorySHAs = shas
	init.MetaSHA = sha
	init.RepositoriesConnected = true
	init.AnalysisComplete = false
	init.ReadyForTasks = false
	init.Progress = 20
	init.Status = domain.InitRunning
	init.Stage = "analyze"
	init.Error = ""
	init.Message = "全部仓库已连接，准备分析"
	return m.save(ctx, init, project)
}

// analyze materializes a read-only analysis copy, runs the init agent with a
// writable generated directory and publishes the validated documents.
func (m *Manager) analyze(ctx context.Context, init domain.Initialization, project domain.Project) (domain.Initialization, error) {
	if m.Agents == nil && m.RemoteAnalysis == nil {
		init.Status = domain.InitBlocked
		init.Error = "Agent 执行后端未配置"
		return m.save(ctx, init, project)
	}
	if init.Attempts < 1 {
		init.Attempts = 1
	}
	if init.ExecutionID == "" {
		init.ExecutionID = fmt.Sprintf("init-%s-analyze-%d", project.ID, init.Attempts)
	}
	var response execution.AnalysisResult
	var err error
	if m.RemoteAnalysis != nil {
		response, err = m.RemoteAnalysis(ctx, &init, project)
	} else {
		response, err = AnalyzeAtNode(ctx, m.DataDir, m.Agents, execution.AnalysisParams{Initialization: init, Project: project}, m)
	}
	ex := response.Execution
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = "无法启动分析 Agent：" + err.Error()
		init.Message = init.Error
		return m.save(ctx, init, project)
	}
	init.SessionID = ex.SessionID
	if ex.Status == "starting" || ex.Status == "running" {
		init.Status = domain.InitRunning
		init.Stage = "analyze"
		init.Progress = 35
		init.Message = "初始化 Agent 正在分析仓库"
		return m.save(ctx, init, project)
	}
	if ex.Status != "succeeded" || ex.Result == nil {
		init.Status = domain.InitBlocked
		init.Error = firstNonEmpty(ex.FailureReason, "分析执行未成功："+ex.Status)
		init.Message = "分析未成功，请检查后重试"
		return m.save(ctx, init, project)
	}
	result := ex.Result
	switch result.Outcome {
	case "needs_input":
		questions := result.Questions
		if len(questions) == 0 {
			questions = []string{firstNonEmpty(result.NeedsHelp, result.Summary, "分析需要补充信息。")}
		}
		for _, q := range questions {
			init.Questions = addQuestion(init.Questions, q)
		}
		init.Status = domain.InitNeedsInput
		init.Message = "分析需要补充信息后继续"
		return m.save(ctx, init, project)
	case "blocked":
		init.Status = domain.InitBlocked
		init.Error = firstNonEmpty(result.NeedsHelp, result.Summary)
		init.Message = "分析遇到障碍，请处理后重试"
		return m.save(ctx, init, project)
	}
	reports, files := response.Reports, response.Files
	err = validateGeneratedFiles(files)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = "读取分析结果失败：" + err.Error()
		return m.save(ctx, init, project)
	}
	if len(files) == 0 {
		init.Status = domain.InitBlocked
		init.Error = "分析未生成任何说明文件"
		init.Message = "分析结果为空，请重试"
		return m.save(ctx, init, project)
	}
	sha, err := m.Managed.Commit(ctx, project.ID, project.MetaRef, "初始化分析结果", files)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = "保存生成内容失败：" + err.Error()
		return m.save(ctx, init, project)
	}
	init.Reports = reports
	init.MetaSHA = sha
	init.AnalysisComplete = true
	return m.completeStatic(ctx, init, project)
}

// prepareAnalysis clones every registered repository at its pinned commit into
// the read-only analysis root. Re-runs reuse a copy only when the commit matches.
func (m *Manager) prepareAnalysis(ctx context.Context, init domain.Initialization, project domain.Project) (string, error) {
	root := filepath.Join(m.initDir(project.ID), "analysis")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	for _, repo := range project.Repositories {
		if repo.Path == "." || repo.Path == "" {
			continue
		}
		sha := init.RepositorySHAs[repo.ID]
		if sha == "" {
			return "", fmt.Errorf("仓库 %s 没有固定的分析提交", repo.ID)
		}
		dest := filepath.Join(root, filepath.FromSlash(repo.Path))
		if head, err := m.analysisHead(dest); err == nil && head == sha {
			continue
		}
		if err := os.RemoveAll(dest); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return "", err
		}
		cloner := m.Cloner
		if cloner == nil {
			cloner = m.defaultClone
		}
		if err := cloner(ctx, project, repo, dest, sha); err != nil {
			return "", fmt.Errorf("拉取仓库 %s 失败：%w", repo.ID, err)
		}
	}
	return root, nil
}

func (m *Manager) analysisHead(dest string) (string, error) {
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		return "", err
	}
	return m.runGit(context.Background(), dest, m.gitEnv(domain.Project{}), "rev-parse", "HEAD")
}

func (m *Manager) gitEnv(project domain.Project) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(m.DataDir, "git-home"), "LANG=C", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:http:ssh:file"}
	if project.ID != "" {
		if token, err := m.projectCredential(project); err == nil && token != "" {
			askpass := os.Getenv("SWARMJET_GIT_ASKPASS")
			if askpass == "" {
				askpass = "/usr/local/bin/swarmjet-askpass"
			}
			key := "GITLAB_TOKEN"
			if project.HostingProvider() == domain.ProviderGitHub {
				key = "GITHUB_TOKEN"
			}
			env = append(env, key+"="+token, "GIT_ASKPASS="+askpass)
		}
	}
	for k, v := range m.AnalysisEnv {
		if hostingCredential(k) {
			continue
		}
		env = append(env, k+"="+v)
	}
	return env
}

func (m *Manager) runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	flags := []string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "core.fsmonitor=false", "-c", "protocol.ext.allow=never"}
	command := exec.CommandContext(ctx, "git", append(flags, args...)...)
	command.Dir = dir
	command.Env = env
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && v != "" && (strings.Contains(k, "TOKEN") || strings.Contains(strings.ToUpper(k), "PROXY")) {
			text = strings.ReplaceAll(text, v, "[REDACTED]")
		}
	}

	if err != nil {
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (m *Manager) defaultClone(ctx context.Context, project domain.Project, repo domain.Repository, dest, sha string) error {
	if !strings.HasPrefix(repo.URL, "http") && !strings.HasPrefix(repo.URL, "ssh") && !filepath.IsAbs(repo.URL) {
		return errors.New("不支持的仓库地址")
	}
	env := m.gitEnv(project)
	if _, err := m.runGit(ctx, "", env, "clone", "--no-checkout", "--no-hardlinks", "--", repo.URL, dest); err != nil {
		return err
	}
	if _, err := m.runGit(ctx, dest, env, "fetch", "--no-recurse-submodules", "--", repo.URL, sha); err != nil {
		return err
	}
	_, err := m.runGit(ctx, dest, env, "checkout", "--detach", sha)
	return err
}

func reservedGenerated(rel string) bool {
	switch rel {
	case ".meta.yaml", ".meta.yml", ".meta":
		return true
	}
	return strings.HasPrefix(rel, ".git/") || rel == ".git"
}

func collectGenerated(generated string) ([]domain.InitReport, map[string]string, error) {
	files := map[string]string{}
	reports := []domain.InitReport{}
	err := filepath.WalkDir(generated, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("生成目录包含符号链接：%s", p)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(generated, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if reservedGenerated(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxGeneratedFile {
			return fmt.Errorf("生成文件 %s 超过 %d 字节", rel, maxGeneratedFile)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(body)
		reports = append(reports, domain.InitReport{Path: rel, Description: summarize(body)})
		return nil
	})
	sort.Slice(reports, func(i, j int) bool { return reports[i].Path < reports[j].Path })
	return reports, files, err
}

func summarize(body []byte) string {
	for _, line := range strings.Split(string(body), newline) {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			if len(line) > 200 {
				return line[:200] + "…"
			}
			return line
		}
	}
	return ""
}

type verificationSpec struct {
	ComposeFile string         `json:"composeFile"`
	Checks      []domain.Check `json:"checks"`
	Commands    []struct {
		Name string   `json:"name"`
		Path string   `json:"path"`
		Argv []string `json:"argv"`
	} `json:"commands"`
}

// readVerificationSpec reads the two analyzed artifacts: verification.json for
// build/test commands and integration.json for the compose-based integration
// configuration. composeFile/checks inside verification.json remain supported
// for compatibility.
func readVerificationSpec(generated string) (verificationSpec, bool, error) {
	var spec verificationSpec
	present := false
	body, err := os.ReadFile(filepath.Join(generated, "verification.json"))
	switch {
	case err == nil:
		if json.Unmarshal(body, &spec) != nil {
			return spec, true, errors.New("verification.json 不是有效的 JSON")
		}
		present = true
	case !errors.Is(err, os.ErrNotExist):
		return spec, false, err
	}
	integrationBody, err := os.ReadFile(filepath.Join(generated, "integration.json"))
	switch {
	case err == nil:
		var integration struct {
			ComposeFile string         `json:"composeFile"`
			Checks      []domain.Check `json:"checks"`
		}
		if json.Unmarshal(integrationBody, &integration) != nil {
			return spec, present, errors.New("integration.json 不是有效的 JSON")
		}
		if strings.TrimSpace(integration.ComposeFile) != "" {
			spec.ComposeFile = integration.ComposeFile
		}
		if len(integration.Checks) > 0 {
			spec.Checks = integration.Checks
		}
	case !errors.Is(err, os.ErrNotExist):
		return spec, present, err
	}
	return spec, present, nil
}

// verify handles persisted jobs from the old initialization flow without
// executing their generated commands.
func (m *Manager) verify(ctx context.Context, init domain.Initialization, project domain.Project) (domain.Initialization, error) {
	return m.completeStatic(ctx, init, project)
}

func (m *Manager) completeStatic(ctx context.Context, init domain.Initialization, project domain.Project) (domain.Initialization, error) {
	init.Status = domain.InitReady
	init.Stage = "complete"
	init.Progress = 100
	init.AnalysisComplete = true
	// Initialization readiness means that the Meta repository exists. Actual
	// task execution still requires the project's separate execution contract.
	init.ReadyForTasks = true
	init.Error = ""
	init.Verification = nil
	init.Message = "Meta 仓库初始化完成：项目说明已保存。初始化仅做静态分析，不再执行业务代码、安装依赖或运行测试。"
	return m.save(ctx, init, project)
}

// runCommand uses the injected runner in tests and an isolated container in
// production. There is deliberately no host fallback.
func (m *Manager) runCommand(ctx context.Context, projectID string, index int, dir string, argv []string) (int, string, error) {
	if m.Runner != nil {
		return m.Runner(ctx, dir, argv)
	}
	if m.VerifyImage == "" {
		return -1, "", errVerificationUnavailable
	}
	return m.dockerRun(ctx, projectID, index, dir, argv)
}

// dockerRun executes one command in an isolated container with a deterministic
// name. The name is recorded before the run so cancellation, timeout and a
// worker restart can stop and remove the container even if the docker client is
// killed: killing the CLI alone would leave the container running.
func (m *Manager) dockerRun(ctx context.Context, projectID string, index int, dir string, argv []string) (int, string, error) {
	name := verifyContainerName(projectID, index)
	// Remove a container left behind by an earlier attempt with the same name.
	stale, cancelStale := context.WithTimeout(context.Background(), 30*time.Second)
	_, _ = m.runDocker(stale, "rm", "-f", name)
	cancelStale()
	if err := m.recordVerifyContainer(projectID, name); err != nil {
		// Without a durable record the container could never be cleaned up, so
		// refuse to start it rather than leak it.
		return -1, "", fmt.Errorf("无法登记验证容器，已阻止启动：%w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Drop the record only after removal is confirmed. A temporary Docker
		// failure keeps it so a later cleanup or restart can retry.
		if m.removeVerifyContainer(cleanup, name) {
			m.forgetVerifyContainer(projectID, name)
		}
	}()
	args := []string{
		"run", "--name", name,
		"--network", "none",
		"--read-only",
		"--tmpfs", "/tmp",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--mount", "type=bind,src=" + dir + ",dst=" + dir,
		"--workdir", dir,
		"--env", "HOME=/tmp",
		// Verification runs its own executable, not the image's agent entrypoint.
		"--entrypoint", argv[0],
		m.VerifyImage,
	}
	args = append(args, argv[1:]...)
	output, runErr := m.runDocker(ctx, args...)
	if runErr != nil {
		if ctx.Err() != nil {
			return -1, string(output), ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return exit.ExitCode(), string(output), nil
		}
		return -1, string(output) + newline + runErr.Error(), fmt.Errorf("容器化验证失败：%w", runErr)
	}
	return 0, string(output), nil
}

func (m *Manager) runDocker(ctx context.Context, args ...string) ([]byte, error) {
	if m.Docker != nil {
		return m.Docker(ctx, args)
	}
	command := exec.CommandContext(ctx, "docker", args...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(m.DataDir, "git-home"), "DOCKER_HOST=" + os.Getenv("DOCKER_HOST")}
	var out cappedBuffer
	command.Stdout, command.Stderr = &out, &out
	err := command.Run()
	return out.Bytes(), err
}

type verifyRegistry struct {
	Containers []string `json:"containers"`
}

func (m *Manager) verifyRegistryPath(projectID string) string {
	return filepath.Join(m.initDir(projectID), "verify-containers.json")
}

func verifyContainerName(projectID string, index int) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			return r
		default:
			return '-'
		}
	}, projectID)
	return "sj-init-verify-" + safe + "-" + strconv.Itoa(index)
}

func (m *Manager) recordVerifyContainer(projectID, name string) error {
	path := m.verifyRegistryPath(projectID)
	names := readVerifyRegistry(path)
	for _, existing := range names {
		if existing == name {
			return nil
		}
	}
	return writeVerifyRegistry(path, append(names, name))
}

func (m *Manager) forgetVerifyContainer(projectID, name string) {
	path := m.verifyRegistryPath(projectID)
	names := readVerifyRegistry(path)
	kept := names[:0]
	for _, existing := range names {
		if existing != name {
			kept = append(kept, existing)
		}
	}
	if len(kept) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = writeVerifyRegistry(path, kept)
}

// cleanupVerifyContainers stops and removes every recorded verification
// container. It uses a fresh context so it still runs after cancellation, and
// retains any record whose removal is not confirmed so it can be retried.
func (m *Manager) cleanupVerifyContainers(projectID string) {
	path := m.verifyRegistryPath(projectID)
	names := readVerifyRegistry(path)
	remaining := names[:0]
	for _, name := range names {
		if !strings.HasPrefix(name, "sj-init-verify-") {
			continue
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		gone := m.removeVerifyContainer(cleanup, name)
		cancel()
		if !gone {
			remaining = append(remaining, name)
		}
	}
	if len(remaining) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = writeVerifyRegistry(path, remaining)
}

// removeVerifyContainer reports whether the named container is gone. Only a
// successful removal or an explicit "no such container" counts; a transient
// Docker failure must keep the record so cleanup can be retried.
func (m *Manager) removeVerifyContainer(ctx context.Context, name string) bool {
	output, err := m.runDocker(ctx, "rm", "-f", name)
	if err == nil {
		return true
	}
	return containerMissing(output, err)
}

func containerMissing(output []byte, err error) bool {
	text := strings.ToLower(string(output))
	if err != nil {
		text += " " + strings.ToLower(err.Error())
	}
	return strings.Contains(text, "no such container") || strings.Contains(text, "no such object")
}

func readVerifyRegistry(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var registry verifyRegistry
	if json.Unmarshal(body, &registry) != nil {
		return nil
	}
	return registry.Containers
}

func writeVerifyRegistry(path string, names []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	body, err := json.Marshal(verifyRegistry{Containers: names})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".verify-registry-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(body)
	}
	_ = temp.Close()
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	const limit = 1 << 20
	n := len(p)
	if b.Len() < limit {
		remaining := limit - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func copyTree(ctx context.Context, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("分析目录不存在：%s", src)
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		entry, err := d.Info()
		if err != nil {
			return err
		}
		if !entry.Mode().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if entry.Mode()&0111 != 0 {
			mode = 0700
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return os.WriteFile(target, body, mode)
	})
}

func (m *Manager) applyIntegration(ctx context.Context, project domain.Project, spec verificationSpec) {
	if strings.TrimSpace(spec.ComposeFile) == "" || len(spec.Checks) == 0 {
		return
	}
	current, err := m.Store.Project(ctx, project.ID)
	if err != nil {
		return
	}
	candidate := current
	candidate.Integration = domain.IntegrationConfig{ComposeFile: spec.ComposeFile, Checks: spec.Checks, StartupTimeoutSeconds: 180}
	if err := domain.ValidateProjectForSave(candidate); err != nil {
		m.log("proposed integration config rejected", err)
		return
	}
	if _, err := m.Store.SaveProject(ctx, candidate); err != nil {
		m.log("save proposed integration config", err)
	}
}

// integrationIsUsable requires the referenced compose file to exist at the
// pinned Meta commit with a valid services section for every required check.
func (m *Manager) integrationIsUsable(project domain.Project) bool {
	if m.Managed == nil {
		return false
	}
	integration := project.Integration
	if strings.TrimSpace(integration.ComposeFile) == "" || len(integration.Checks) == 0 {
		return false
	}
	required := false
	for _, check := range integration.Checks {
		if strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Service) == "" || len(check.Command) == 0 {
			return false
		}
		required = required || check.Required
	}
	if !required {
		return false
	}
	body, err := m.Managed.ReadFile(context.Background(), project.ID, project.MetaRef, integration.ComposeFile)
	if err != nil {
		return false
	}
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if yaml.Unmarshal(body, &doc) != nil || len(doc.Services) == 0 {
		return false
	}
	for _, check := range integration.Checks {
		if _, ok := doc.Services[check.Service]; !ok {
			return false
		}
	}
	return true
}

func safeRelative(p string) bool {
	if p == "" || filepath.IsAbs(p) || filepath.Clean(p) != p || strings.HasPrefix(p, "..") || strings.IndexByte(p, 92) >= 0 {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".git") {
			return false
		}
	}
	return true
}

func truncate(text string, n int) string {
	text = strings.TrimSpace(text)
	if len(text) <= n {
		return text
	}
	return text[:n] + newline + "…（输出已截断）"
}
