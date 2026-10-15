package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
)

func acceptanceConfig(t domain.Task) (domain.IntegrationConfig, error) {
	cfg := t.Project.Integration
	if t.Project.AcceptanceStrategy != "repository" {
		return cfg, nil
	}
	cfg = domain.IntegrationConfig{Mode: "commands", Image: t.Project.Development.Image, Commands: t.AcceptanceCommands, StartupTimeoutSeconds: 600}
	if t.Project.Integration.Image != "" {
		cfg.Image = t.Project.Integration.Image
	}
	if len(cfg.Commands) == 0 || len(cfg.Commands) > 20 {
		return cfg, errors.New("未识别到有依据的仓库验收命令，请人工确认")
	}
	for _, ch := range cfg.Commands {
		registered := false
		for _, r := range t.Project.Repositories {
			if ch.Path == r.Path {
				registered = true
			}
		}
		if !registered || strings.TrimSpace(ch.Evidence) == "" || len(ch.Argv) == 0 || ch.Argv[0] == "" || ch.Name == "" {
			return cfg, errors.New("验收计划缺少仓库目录、命令或来源依据")
		}
	}
	return cfg, nil
}

func (a *Activities) strategyVerification(ctx context.Context, c forge.Client, n execution.Node, t *domain.Task) (ready, waiting bool, err error) {
	t.AcceptanceIncomplete = false
	t.Verification = nil
	digest := domain.Digest(t.Workspace.Revisions)
	incomplete := func(reason string) (bool, bool, error) {
		t.AcceptanceIncomplete = true
		t.Message = reason + "；MR 保留，等待人工验收，不允许自动合并"
		t.Verification = append(t.Verification, domain.Verification{Name: "验收待人工确认", Required: true, Passed: false, ExitCode: -1, Report: reason, RevisionDigest: digest})
		return true, false, nil
	}
	if t.Project.AcceptanceStrategy == "platform_ci" && t.Project.HostingProvider() == domain.ProviderGitHub {
		client, err := githubClient(c)
		if err != nil {
			return false, false, err
		}
		changed := false
		ready = true
		for _, r := range t.Workspace.Revisions {
			if !r.Changed {
				continue
			}
			changed = true
			checks, checkErr := client.RequiredChecks(ctx, r.ProjectID, r.MRIID, r.HeadSHA)
			if checkErr != nil {
				return false, false, checkErr
			}
			if checks.RequiredCount == 0 {
				return incomplete("目标分支没有配置必需检查")
			}
			t.Verification = append(t.Verification, domain.Verification{Name: r.RepositoryID + " GitHub 必需检查", Required: true, Passed: checks.Ready, ExitCode: map[bool]int{true: 0, false: 1}[checks.Ready], Report: r.MRURL + " — " + checks.Reason, RevisionDigest: digest})
			ready = ready && checks.Ready
			waiting = waiting || checks.Waiting
		}
		if !changed {
			return incomplete("没有变更提交可触发必需检查")
		}
		if waiting {
			t.Message = "等待 GitHub 当前 PR 提交的必需检查"
		}
		return ready, waiting, nil
	}
	if t.Project.AcceptanceStrategy == "gitlab_ci" || t.Project.AcceptanceStrategy == "platform_ci" && t.Project.HostingProvider() == domain.ProviderGitLab {
		changed := false
		ready = true
		for _, r := range t.Workspace.Revisions {
			if !r.Changed {
				continue
			}
			changed = true
			mr, same, e := (mergeEvidence{client: c, task: t}).reviewedMR(ctx, r)
			if e != nil {
				return false, false, e
			}
			if !same {
				return false, false, errors.New("MR 提交已变化，不能使用旧提交 CI")
			}
			if mr.HeadPipeline == nil || mr.HeadPipeline.SHA != r.HeadSHA {
				// GitLab may attach the pipeline shortly after MR creation.
				attempts := 0
				if t.Checkpoint != nil {
					attempts = t.Checkpoint.CIWaits
				}
				if attempts < 4 {
					t.Message = "等待 GitLab 为当前 MR 提交创建流水线"
					return false, true, nil
				}
				return incomplete("当前 MR 提交没有对应的 GitLab CI 流水线")
			}
			status := mr.HeadPipeline.Status
			if status == "pending" || status == "running" || status == "created" || status == "preparing" || status == "waiting_for_resource" {
				waiting = true
			}
			pass, e := c.RequiredCIPassed(ctx, r.ProjectID, r.MRIID, r.HeadSHA, true)
			if e != nil {
				return false, false, e
			}
			t.Verification = append(t.Verification, domain.Verification{Name: r.RepositoryID + " GitLab CI", Required: true, Passed: pass, ExitCode: map[bool]int{true: 0, false: 1}[pass], Report: mr.URL + " — " + status, RevisionDigest: digest})
			ready = ready && pass
			if status == "manual" || status == "skipped" || status == "canceled" {
				return incomplete("GitLab CI 尚未形成成功的验收结果：" + status)
			}
		}
		if !changed {
			return incomplete("没有变更提交可触发 CI；请审查无改动结论")
		}
		if waiting {
			t.Message = "等待当前 MR 提交的 GitLab CI 完成"
		}
		return ready, waiting, nil
	}
	cfg, e := acceptanceConfig(*t)
	if e != nil {
		return incomplete(e.Error())
	}
	if e = n.StartIntegration(ctx, t.Workspace, cfg, a.integrationEnv(*t)); e != nil {
		_ = n.StopIntegration(ctx, t.Workspace, cfg, a.integrationEnv(*t))
		return incomplete("验收环境启动失败：" + e.Error())
	}
	checks, e := n.VerifyIntegration(ctx, t.Workspace, cfg, a.integrationEnv(*t))
	stopErr := n.StopIntegration(ctx, t.Workspace, cfg, a.integrationEnv(*t))
	if stopErr != nil {
		return false, false, stopErr
	}
	if e != nil {
		return incomplete("验收环境或命令不可用：" + e.Error())
	}
	t.Verification = checks
	ready = true
	required := 0
	environmentFailures := []string{}
	for i := range t.Verification {
		v := &t.Verification[i]
		v.RevisionDigest = digest
		if v.Required {
			required++
			ready = ready && v.Passed
		}
		if v.EnvironmentFailure || v.ExitCode == 125 || v.ExitCode == 126 || v.ExitCode == 127 || v.ExitCode == -1 {
			environmentFailures = append(environmentFailures, v.Name)
		}
	}
	if len(environmentFailures) > 0 {
		return incomplete("验收环境无法运行检查：" + strings.Join(environmentFailures, "、") + "；请查看命令报告")
	}
	if required == 0 {
		return incomplete("没有执行必需验收检查")
	}
	return ready, false, nil
}

const manualStart = "<!-- swarmjet:human-acceptance -->"
const manualEnd = "<!-- /swarmjet:human-acceptance -->"

func manualDescription(t domain.Task, r domain.Revision, existing string) (string, error) {
	plan := t.HumanAcceptance
	if plan == nil || len(plan.Prerequisites) == 0 || len(plan.Steps) == 0 || len(plan.Steps) > 30 {
		return "", errors.New("MR 缺少真人验收清单：请提供前置条件、操作步骤和每步预期结果")
	}
	for _, s := range plan.Steps {
		if strings.TrimSpace(s.Action) == "" || strings.TrimSpace(s.Expected) == "" {
			return "", errors.New("真人验收每一步必须包含操作方法和预期结果")
		}
	}
	stamp := "<!-- reviewed-head:" + r.HeadSHA + " -->"
	// Preserve human checkmarks on retries at the same revision. New revisions
	// reset this section so earlier sign-off is not silently carried forward.
	if strings.HasPrefix(existing, manualStart) && strings.Contains(existing, stamp) && strings.Contains(existing, manualEnd) {
		return existing, nil
	}
	body := existing
	if start := strings.Index(body, manualStart); start >= 0 {
		if end := strings.Index(body[start:], manualEnd); end >= 0 {
			body = body[:start] + body[start+end+len(manualEnd):]
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n## 🧑‍💻 真人测试验收（请优先阅读）\n\n> **待真人验收：自动检查或 Agent 审查通过，不代表本清单已经通过。**\n> 验收版本：`%s`。提交变化后请重新验收。\n\n### 前置条件与环境\n", manualStart, stamp, r.HeadSHA)
	for _, s := range plan.Prerequisites {
		fmt.Fprintf(&b, "- %s\n", cleanManual(s))
	}
	b.WriteString("\n### 操作步骤与预期结果\n")
	for _, s := range plan.Steps {
		fmt.Fprintf(&b, "- [ ] **操作：** %s\n  - **预期：** %s\n", cleanManual(s.Action), cleanManual(s.Expected))
	}
	if len(plan.Notes) > 0 {
		b.WriteString("\n### 风险、未覆盖项与清理\n")
		for _, s := range plan.Notes {
			fmt.Fprintf(&b, "- %s\n", cleanManual(s))
		}
	}
	b.WriteString("\n### 真人验收记录\n- 验收人：\n- 环境 / 时间：\n- 结果与截图 / 录屏：\n- 未通过项：\n\n" + manualEnd + "\n\n---\n\n" + strings.TrimSpace(body))
	if b.Len() > 60000 {
		return "", errors.New("真人验收清单过长")
	}
	return b.String(), nil
}
func cleanManual(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "<!--", "&lt;!--"), "\n", " ")
}

func issueReviewGuide(t domain.Task) (string, error) {
	plan := t.HumanAcceptance
	if plan == nil || len(plan.Prerequisites) == 0 || len(plan.Steps) == 0 || len(plan.Steps) > 30 {
		return "", errors.New("缺少人工复核指引：请提供前置条件、操作步骤和预期结果")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## 人工复核指引 · SwarmJet 第 %d 轮\n\n任务：`%s`。本轮由系统执行验收与独立审查；合并状态以关联 MR 为准。以下步骤供合并后复核，不代表人工已验收。\n\n", t.Round, t.ID)
	fmt.Fprintf(&b, "审查提交集合：`%s`\n\n### 本轮 MR\n", domain.Digest(t.Workspace.Revisions))
	for _, r := range t.Workspace.Revisions {
		if r.Changed {
			fmt.Fprintf(&b, "- %s：%s · `%s` → `%s`\n", cleanManual(r.RepositoryID), r.MRURL, r.HeadSHA, cleanManual(r.TargetBranch))
		}
	}
	b.WriteString("\n### 前置条件与环境\n")
	for _, item := range plan.Prerequisites {
		fmt.Fprintf(&b, "- %s\n", cleanManual(item))
	}
	b.WriteString("\n### 操作步骤与预期结果\n")
	for i, step := range plan.Steps {
		if strings.TrimSpace(step.Action) == "" || strings.TrimSpace(step.Expected) == "" {
			return "", errors.New("复核步骤需要操作和预期结果")
		}
		fmt.Fprintf(&b, "%d. %s\n   - 预期：%s\n", i+1, cleanManual(step.Action), cleanManual(step.Expected))
	}
	if len(plan.Notes) > 0 {
		b.WriteString("\n### 风险、未覆盖项与清理\n")
		for _, item := range plan.Notes {
			fmt.Fprintf(&b, "- %s\n", cleanManual(item))
		}
	}
	b.WriteString("\n如需新一轮开发，请在此 Issue 发布以 `/swarmjet run` 开头的评论。")
	if b.Len() > 60000 {
		return "", errors.New("人工复核指引过长")
	}
	return b.String(), nil
}

func publishIssueReviewGuide(ctx context.Context, c forge.Client, t domain.Task) error {
	changed := false
	for _, r := range t.Workspace.Revisions {
		changed = changed || r.Changed
	}
	if !changed {
		return nil
	}
	body, err := issueReviewGuide(t)
	if err != nil {
		return err
	}
	link, err := c.UpsertIssueReview(ctx, t.Issue, t.ID, domain.Digest(t.Workspace.Revisions), body)
	if err != nil {
		return err
	}
	const start = "<!-- swarmjet:issue-review -->"
	const end = "<!-- /swarmjet:issue-review -->"
	for _, r := range t.Workspace.Revisions {
		if !r.Changed || r.MRIID == 0 {
			continue
		}
		mr, same, err := (mergeEvidence{client: c, task: &t}).reviewedMR(ctx, r)
		if err != nil {
			return err
		}
		if !same {
			return errors.New("MR changed before review guide publication")
		}
		description := mr.Description
		if pos := strings.Index(description, start); pos >= 0 {
			if tail := strings.Index(description[pos:], end); tail >= 0 {
				description = description[:pos] + description[pos+tail+len(end):]
			}
		}
		next := strings.TrimSpace(description) + "\n\n" + start + "\n人工复核指引与后续讨论：[来源 Issue](" + link + ")\n" + end
		if next != mr.Description {
			if err := c.UpdateMRDescription(ctx, r.ProjectID, r.MRIID, next); err != nil {
				return err
			}
		}
	}
	return nil
}

func taskIntegration(t domain.Task) domain.IntegrationConfig {
	if t.Project.AcceptanceStrategy == "none" || t.Project.AcceptanceStrategy == "repository" || t.Project.AcceptanceStrategy == "gitlab_ci" || t.Project.AcceptanceStrategy == "platform_ci" {
		return domain.IntegrationConfig{Mode: "commands"}
	}
	return t.Project.Integration
}

func refreshMRVerification(ctx context.Context, c forge.Client, t domain.Task) error {
	const start = "<!-- swarmjet:verification -->"
	const end = "<!-- /swarmjet:verification -->"
	var b strings.Builder
	if t.Round > 0 {
		b.WriteString(start + "\n## 自动验收结果\n\n")
	} else {
		b.WriteString(start + "\n## 自动验收结果（不代替真人验收）\n\n")
	}
	if t.AcceptanceIncomplete {
		b.WriteString("> **验收不完整，禁止自动合并。**\n\n")
	}
	for _, v := range t.Verification {
		state := "未通过"
		if v.Passed {
			state = "通过"
		}
		fmt.Fprintf(&b, "- %s：%s\n", cleanManual(v.Name), state)
	}
	if t.AcceptanceIncomplete {
		b.WriteString("\n" + cleanManual(t.Message) + "\n")
	}
	b.WriteString("\n详细执行记录见 SwarmJet 任务报告。\n" + end)
	for _, r := range t.Workspace.Revisions {
		if !r.Changed || r.MRIID == 0 {
			continue
		}
		mr, same, err := (mergeEvidence{client: c, task: &t}).reviewedMR(ctx, r)
		if err != nil {
			return err
		}
		if !same {
			return errors.New("MR changed during verification")
		}
		body := mr.Description
		if pos := strings.Index(body, start); pos >= 0 {
			if tail := strings.Index(body[pos:], end); tail >= 0 {
				body = body[:pos] + body[pos+tail+len(end):]
			}
		}
		next := strings.TrimSpace(body) + "\n\n" + b.String()
		if next != mr.Description {
			if err = c.UpdateMRDescription(ctx, r.ProjectID, r.MRIID, next); err != nil {
				return err
			}
		}
	}
	return nil
}
