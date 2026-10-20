# 方法论包

每个子目录是一套可以安装到 Hive 的方法论，`catalog.yaml` 记录内置模板的名称、介绍和版本。模板随平台二进制一起发布。

**新 Hive 不预装方法论。** Queen 在首次对话中介绍可选模板，由用户决定后安装。未安装时，手动派出的 worker 使用平台的极简兜底；这不产生版本历史，也不算已安装方法论。

- `methodology.yaml` 是方法论的 manifest，定义角色、权限、常驻规则、事件规则、合并条件和资源上限，格式见 `docs/claude/round4-prompts.md` 的"公共契约"第 2 节。
- 除 `README.md` 外，其余文件都会原样下发到每个 Cell 的 omp 配置目录：
  - `skills/<名字>/SKILL.md`，frontmatter 必须写 `name` 和 `description`；
  - `rules/*.md`；
  - 角色提示词文件。
- 仓库里的每个方法论包都由 `internal/methodology/packages_test.go` 用发布时同样的规则校验。
- `queen.prompt` 指向的方法论文件追加到平台固定的 Queen 核心提示词之后，只调整团队风格；`queen.merge` 决定 Queen 是否可以调用合并入口，合并仍需满足 manifest 的条件。

## minimal

只有一个 `worker`，可发布分支和 PR、发送消息。没有独立 verifier，不要求 attest，不自动合并，适合先用简单工作方式开始。

## pstack

`pstack/` 改写自 [cursor/plugins 的 pstack](https://github.com/cursor/plugins/tree/main/pstack)（MIT 许可，见 `pstack/NOTICE.md`）。

**角色**：
- `worker`：负责一件活，写代码，由平台在每轮结束后自动发布分支和 PR。
- `verifier`：只读角色。PR 每有一个新的 head 提交，就由 `pr_head` 事件派出一个；它在 Cell 里把改动真正跑一遍，然后用 `attest` 对这个提交给出结论，并把发现发回给 worker。

**合并条件**：当前 head 必须有一个非作者给出的 PASS 或 PASS_NOTES，并且 CI 通过；默认不自动合并。

**skill**：
- `bug-fix`、`feature`、`refactoring`、`investigation`：按任务类型选用的四个 playbook；
- `verify-revision`：verifier 的验证流程；
- `ci-and-review-triage`：PR 作者处理 CI 失败、验证发现和审查评论。

## 安装与发布

在网页的方法论页选择模板并确认安装，或请 Queen 安装。MCP 的 `list_methodology_templates` 列出目录，`install_methodology_template` 接受 `hiveId`、`name` 和可选的 `note`；安装权限属于用户 Token 和 Queen。

安装会发布并立即激活一个新版本，来源记录为 `builtin:<name>`，ref 为目录中的模板版本。HTTP 对应 `GET /api/methodology-templates` 和 `POST /api/hives/{id}/methodology/install`（请求体 `{name, note?}`），均需要管理员会话，POST 还需要 CSRF 校验。

本地 Agent 可以直接调用 MCP 工具 `publish_methodology` 发布，也可以用仓库里的脚本：

```bash
SWARMJET_TOKEN=<default 或 admin Token> python3 scripts/publish_methodology.py methodologies/pstack <hive-id> --note "pstack"
```

发布后立即生效：
- 常驻规则从下一轮开始生效；
- 角色提示词和 skill 文件在 Agent 下一次启动 Cell 时生效。

要回滚，用 `activate_methodology`，或在网页的方法论页切换到之前的版本。
