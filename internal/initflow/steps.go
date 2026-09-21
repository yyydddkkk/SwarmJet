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

	"swarmjet/internal/domain"
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
	client, err := m.client(project)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = err.Error()
		init.Message = "无法读取 GitLab 连接：" + err.Error()
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
	if m.Agents == nil {
		init.Status = domain.InitBlocked
		init.Error = "Agent 执行后端未配置"
		return m.save(ctx, init, project)
	}
	root, err := m.prepareAnalysis(ctx, init, project)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = "准备分析副本失败：" + err.Error()
		init.Message = init.Error
		return m.save(ctx, init, project)
	}
	generated := filepath.Join(m.initDir(project.ID), "generated")
	if err = os.MkdirAll(generated, 0700); err != nil {
		init.Status = domain.InitBlocked
		init.Error = err.Error()
		return m.save(ctx, init, project)
	}
	if init.Attempts < 1 {
		init.Attempts = 1
	}
	if init.ExecutionID == "" {
		init.ExecutionID = fmt.Sprintf("init-%s-analyze-%d", project.ID, init.Attempts)
	}
	req := domain.AgentRequest{ExecutionID: init.ExecutionID, TaskID: project.ID, Role: "init", Workspace: root, GeneratedDir: generated, Prompt: initialPrompt(project, init), SessionID: init.SessionID, Config: project.Development}
	ex, err := m.Agents.Start(ctx, req)
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
	reports, files, err := collectGenerated(generated)
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
	init.Progress = 65
	init.Stage = "verify"
	init.Status = domain.InitRunning
	init.Error = ""
	init.Message = "分析完成，开始验证构建与测试"
	return m.save(ctx, init, project)
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
			env = append(env, "GITLAB_TOKEN="+token, "GIT_ASKPASS=/usr/local/bin/swarmjet-askpass")
		}
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

// verify runs the analyzed build/test commands in a separate writable copy of
// the pinned revisions. A missing command list is reported as unfinished rather
// than silently passing.
// verify runs the analyzed build/test commands in a separate writable copy of
// the pinned revisions. Missing commands and a missing isolated runtime block
// the verification instead of silently passing; a failed run stays retryable.
func (m *Manager) verify(ctx context.Context, init domain.Initialization, project domain.Project) (domain.Initialization, error) {
	generated := filepath.Join(m.initDir(project.ID), "generated")
	spec, present, err := readVerificationSpec(generated)
	if err != nil {
		init.Status = domain.InitBlocked
		init.Error = err.Error()
		return m.save(ctx, init, project)
	}
	// Publish any analyzed integration configuration even when verification is
	// blocked so an operator can review and complete it. Readiness is taken from
	// the persisted project so a manually configured integration also counts.
	m.applyIntegration(ctx, project, spec)
	integrationReady := false
	if current, err := m.Store.Project(ctx, project.ID); err == nil {
		integrationReady = strings.TrimSpace(current.Integration.ComposeFile) != "" && len(current.Integration.Checks) > 0
	}
	if !present || len(spec.Commands) == 0 {
		init.AnalysisComplete = true
		init.ReadyForTasks = false
		init.Progress = 100
		init.Status = domain.InitBlocked
		init.Error = "分析未提供可执行的构建/测试命令"
		init.Message = "项目说明已保存，但没有识别到可执行的构建/测试命令；请补充验收检查或调整后重试。"
		init.Verification = []domain.InitVerification{{Name: "verification", Passed: false, DryRun: true, Report: init.Error}}
		return m.save(ctx, init, project)
	}
	if m.Runner == nil && m.VerifyImage == "" {
		init.AnalysisComplete = true
		init.ReadyForTasks = false
		init.Progress = 100
		init.Status = domain.InitBlocked
		init.Error = errVerificationUnavailable.Error()
		init.Message = "已阻断验证：未配置隔离验证环境（INIT_VERIFY_IMAGE）。项目说明已保存，请在部署中配置隔离镜像后重试。"
		init.Verification = []domain.InitVerification{{Name: "verification", Passed: false, DryRun: true, Report: init.Error}}
		return m.save(ctx, init, project)
	}
	verifyRoot := filepath.Join(m.initDir(project.ID), "verify")
	if err = os.RemoveAll(verifyRoot); err != nil {
		init.Status = domain.InitBlocked
		init.Error = err.Error()
		return m.save(ctx, init, project)
	}
	for _, repo := range project.Repositories {
		if repo.Path == "." || repo.Path == "" {
			continue
		}
		src := filepath.Join(m.initDir(project.ID), "analysis", filepath.FromSlash(repo.Path))
		if err = copyTree(ctx, src, filepath.Join(verifyRoot, filepath.FromSlash(repo.Path))); err != nil {
			init.Status = domain.InitBlocked
			init.Error = "准备验证副本失败：" + err.Error()
			return m.save(ctx, init, project)
		}
	}
	results := []domain.InitVerification{}
	allPassed := true
	for _, command := range spec.Commands {
		name := strings.TrimSpace(command.Name)
		if name == "" {
			name = strings.Join(command.Argv, " ")
		}
		if len(command.Argv) == 0 || strings.TrimSpace(command.Argv[0]) == "" {
			results = append(results, domain.InitVerification{Name: name, Command: command.Argv, Passed: false, ExitCode: -1, Report: "验证命令为空"})
			allPassed = false
			continue
		}
		dir := verifyRoot
		if p := strings.TrimSpace(command.Path); p != "" {
			if !safeRelative(p) {
				results = append(results, domain.InitVerification{Name: name, Command: command.Argv, Passed: false, ExitCode: -1, Report: "验证路径不安全：" + p})
				allPassed = false
				continue
			}
			dir = filepath.Join(verifyRoot, filepath.FromSlash(p))
		}
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			results = append(results, domain.InitVerification{Name: name, Command: command.Argv, Passed: false, ExitCode: -1, Report: "验证目录不存在：" + command.Path})
			allPassed = false
			continue
		}
		timeout := m.VerifyTimeout
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		code, output, runErr := m.runCommand(runCtx, dir, command.Argv)
		cancel()
		passed := runErr == nil && code == 0
		allPassed = allPassed && passed
		report := output
		if runErr != nil {
			report = runErr.Error()
		}
		results = append(results, domain.InitVerification{Name: name, Command: command.Argv, Passed: passed, ExitCode: code, Report: truncate(report, 4000)})
	}
	init.Verification = results
	init.AnalysisComplete = true
	init.Progress = 100
	init.Error = ""
	switch {
	case !allPassed:
		init.Status = domain.InitBlocked
		init.ReadyForTasks = false
		init.Error = "验证命令未全部通过"
		init.Message = "项目说明已保存，但验证命令未全部通过；请查看结果并修正后重试。"
	case !integrationReady:
		init.Status = domain.InitBlocked
		init.ReadyForTasks = false
		init.Error = "缺少有依据的联调配置"
		init.Message = "验证已通过，但缺少联调 compose 与必需检查；请补充后重试或手动配置。"
	default:
		init.Status = domain.InitReady
		init.ReadyForTasks = true
		init.Message = "初始化完成：说明与验证均已通过，请确认后启用项目。"
	}
	return m.save(ctx, init, project)
}

// runCommand uses the injected runner in tests and an isolated container in
// production. There is deliberately no host fallback.
func (m *Manager) runCommand(ctx context.Context, dir string, argv []string) (int, string, error) {
	if m.Runner != nil {
		return m.Runner(ctx, dir, argv)
	}
	if m.VerifyImage == "" {
		return -1, "", errVerificationUnavailable
	}
	return m.dockerRun(ctx, dir, argv)
}

// dockerRun executes one command in a throwaway container that has no network,
// no added capabilities and only the writable verification copy mounted.
func (m *Manager) dockerRun(ctx context.Context, dir string, argv []string) (int, string, error) {
	args := []string{
		"run", "--rm",
		"--network", "none",
		"--read-only",
		"--tmpfs", "/tmp",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--mount", "type=bind,src=" + dir + ",dst=" + dir,
		"--workdir", dir,
		"--env", "HOME=/tmp",
		m.VerifyImage,
	}
	args = append(args, argv...)
	command := exec.CommandContext(ctx, "docker", args...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(m.DataDir, "git-home"), "DOCKER_HOST=" + os.Getenv("DOCKER_HOST")}
	var out cappedBuffer
	command.Stdout, command.Stderr = &out, &out
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return -1, out.String(), ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), out.String(), nil
		}
		return -1, out.String() + newline + err.Error(), fmt.Errorf("容器化验证失败：%w", err)
	}
	return 0, out.String(), nil
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
