package initflow

import (
	"fmt"
	"strings"

	"swarmjet/internal/domain"
)

const initPromptIntro = `你是 SwarmJet 的初始化 Agent。你只读取静态文件并生成 Meta 仓库文档，不要修改、推送或合并任何业务仓库。禁止执行业务代码、命令脚本、依赖安装、构建、测试或启动服务；不得调用 shell、bash 或委派其他 Agent 执行这些操作。使用文件读取、搜索和编辑工具完成任务。

已分析的仓库固定在以下提交（只读挂载）：
`

const initPromptBody = `
必须生成的文件（写入可写的 generated 目录，不要写入分析目录）：
- PROJECT.md：项目说明、参与仓库及其关系。
- ARCHITECTURE.md：组件、边界、数据流与跨仓库契约。
- DEVELOPMENT.md：每个仓库的构建、测试、运行命令，必须来自你实际读到的文件。
- 不需要生成 verification.json，也不需要验证任何命令。可在 DEVELOPMENT.md 记录静态识别到的命令并明确标注“未执行”。
- integration.json（可选，不是初始化完成的条件）：联调配置：{"composeFile":"<相对路径>","checks":[{"name":"...","service":"<compose 服务名>","command":["..."],"required":true}]}。必须在 generated 目录同时写入你引用的 compose 文件，且 checks 的 service 必须存在于该文件。只有在仓库内容有依据时才提供。

规则：
- 只依据实际读取的文件陈述；不要编造命令、服务或文件。
- 不要生成永远通过的空测试或空配置。
- 信息不足时在结果 questions 中提出具体问题，不要猜测。
- 输出中不得包含任何令牌或凭据。
`

func initialPrompt(project domain.Project, init domain.Initialization) string {
	var b strings.Builder
	b.WriteString(initPromptIntro)
	for _, repo := range project.Repositories {
		if repo.Path == "." || repo.Path == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s：路径 %s，地址 %s，提交 %s%s", repo.ID, repo.Path, repo.URL, init.RepositorySHAs[repo.ID], newline)
	}
	if len(init.Questions) > 0 {
		b.WriteString(newline + "此前提出的问题与回答（必须据此修正你的结论）：" + newline)
		for _, q := range init.Questions {
			fmt.Fprintf(&b, "- 问：%s%s  答：%s%s", q.Question, newline, q.Answer, newline)
		}
	}
	b.WriteString(initPromptBody)
	return b.String()
}
