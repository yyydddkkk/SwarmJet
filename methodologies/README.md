# 方法论包

每个子目录是一套可以直接发布到 Hive 的方法论。

- `methodology.yaml` 是方法论的 manifest，定义角色、权限、常驻规则、事件规则、合并条件和资源上限，格式见 `docs/claude/round4-prompts.md` 的"公共契约"第 2 节。
- 除 `README.md` 外，其余文件都会原样下发到每个 Cell 的 omp 配置目录：
  - `skills/<名字>/SKILL.md`，frontmatter 必须写 `name` 和 `description`；
  - `rules/*.md`；
  - 角色提示词文件。
- 仓库里的每个方法论包都由 `internal/methodology/packages_test.go` 用发布时同样的规则校验。

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

## 发布

本地 Agent 可以直接调用 MCP 工具 `publish_methodology` 发布，也可以用仓库里的脚本：

```bash
SWARMJET_TOKEN=<default 或 admin Token> python3 scripts/publish_methodology.py methodologies/pstack <hive-id> --note "pstack"
```

发布后立即生效：
- 常驻规则从下一轮开始生效；
- 角色提示词和 skill 文件在 Agent 下一次启动 Cell 时生效。

要回滚，用 `activate_methodology`，或在网页的方法论页切换到之前的版本。
