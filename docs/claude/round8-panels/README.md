# 第八轮 D：面板设计对照与验收

基准是 `context/design/stars/index.html`（本轮唯一设计稿），视口 1440×900。每张并排图左侧为设计稿、右侧为使用 `round8Project()` 注入的实现；两侧均关闭简报，等待入场过渡结束，项目主面板从顶部开始。明亮、夜空各一组。mock 包含 48 个 Agent、16 个开着的 PR、13 个 Issue、900 条消息、约 3000 字任务；所有凭证输入保持空白。设计稿的示例项目与 mock 的实时规模不同，因此星图的对象密度不作像素差异判定。

| 页面 | 明亮 | 夜空 | 对照差异与处理 |
|---|---|---|---|
| 项目主面板 | [并排](compare-project-overview-bright.png) | [并排](compare-project-overview-night.png) | 按设计稿顺序呈现标题、lead、方法论轨道/规则/文件/版本、设置、花费、记忆。mock 生效的是第 3 版，设计示例为 pstack 第 4 版；规则/文件照真实数据多出几项。侧板恢复设计稿的 `--veil`，项目 Logo 与 cartouche 的遮挡由 E 的外围层在集成后承担；本单支截图仍可见淡影。 |
| 方法论高级页 | [并排](compare-project-methodology-bright.png) | [并排](compare-project-methodology-night.png) | 设计稿没有文件编辑、权限、版本差异与安装目录；从细线分区、轨道图、斜体次级入口推导，在项目主面板“编辑方法论与版本历史”打开，保留现有能力。主面板的“查看第 N 版”直接打开该版文件与规则，只查看不激活。已知内置目录/角色英文说明转中文；未知英文只显示中文提示，原文可展开。 |
| 项目完整设置 | [并排](compare-project-settings-bright.png) | [并排](compare-project-settings-night.png) | 设计稿只示三条设置；原有并发/模型/主 Agent 表单从“更多项目设置”进入，沿用细线表格和斜体主要操作。主面板中的 ±、启停可直接保存。 |
| 项目花费详情 | [并排](compare-project-usage-bright.png) | [并排](compare-project-usage-night.png) | 设计稿有本周数字与趋势线；接口只提供今天/累计，主面板显示今天及方法论预算，详细排行进入次级页，不伪造本周趋势。 |
| 记忆 | [并排](compare-project-memory-bright.png) | [并排](compare-project-memory-night.png) | 后端无记忆数据，沿用设计稿的空状态。 |
| 平台接入 | [并排](compare-platform-setup-bright.png) | [并排](compare-platform-setup-night.png) | 设计稿为简洁接入行；现有 GitHub App manifest、安装回调和接入状态保留为附录式分区。接口无“最近一次投递/手动同步”，不显示虚构值或操作。 |
| 平台模型 | [并排](compare-platform-models-bright.png) | [并排](compare-platform-models-night.png) | 设计稿只示凭证状态；原有模型凭证表单与供应商列表按附录细线表格推导，截图输入框为空。 |
| 平台连接 | [并排](compare-platform-connections-bright.png) | [并排](compare-platform-connections-night.png) | 设计稿只示连接行；现有 MCP token 新建、一次性明文显示、吊销保留在附录式表格与对话框，截图没有真实 token。 |
| 平台账簿 | [并排](compare-platform-ledger-bright.png) | [并排](compare-platform-ledger-night.png) | 设计稿示本周按项目花费；实际接口只支持今天/累计，逐项目列出真实可用值。 |
| 平台账号 | [并排](compare-platform-account-bright.png) | [并排](compare-platform-account-night.png) | 设计稿示最近登录时间；会话接口无此字段，显示当前管理员与退出操作。 |
| Agent 详情 | [并排](compare-object-agent-bright.png) | [并排](compare-object-agent-night.png) | 沿用设计稿右侧 lens 尺寸、标题/斜体/细线/进出过渡。标题取关联 Issue 摘要并单行省略，完整摘要保留在 title 与可访问名称；任务默认 4 行；编号/sha 显示短值并复制全值。设计稿的工具日志与 Diff 数据后端未提供，实页展示真实对话、交接、验证和花费。 |
| PR 详情 | [并排](compare-object-pr-bright.png) | [并排](compare-object-pr-night.png) | 设计稿没有 PR 专页；按 lens 推导，标题用单行省略的 PR 标题，完整标题留在 title 与可访问名称；GitHub CI 与独立验证分区。长结论 4 行折叠；金色下划线主按钮保留 headSha/ready/merged 同步校验与双击保护。 |
| Issue 详情 | [并排](compare-object-signal-bright.png) | [并排](compare-object-signal-night.png) | 设计稿没有 Issue 专页；按 lens 推导，显示外部内容来源、正文折叠、GitHub 链接与请主 Agent 处理操作。只有 `kind=issue` 可派活；check、pull_request 只读。 |
| 主 Agent 详情 | [并排](compare-object-queen-bright.png) | [并排](compare-object-queen-night.png) | 设计稿没有主 Agent 专页；按 lens 推导，显示状态、真实对话、对话输入与方法论入口。 |

对象详情、项目面板、平台设置均保留真实退场节点约 0.7–0.75 秒；退场期间设 `inert`、`aria-hidden`，之后卸载。对象详情打开时工具栏整体隐藏，避免只露出一半。平台设置附录保持不透明 `--paper`、参考的双细线框、230px 左导航、640px 内容列及底部留白；子页主标题为参考的 30px。375px 下详情占满可用宽度，长文本可展开。方法论资源未就绪时仅在该节显示加载或失败，设置/花费/记忆仍可先查看，不用占位数据冒充已加载版本。

**用户批准的对比度例外**：设计稿把部分可读辅助文字设为 `ink3`，在明亮底色约 2.92:1。首次实际运行 axe 报 `color-contrast`：项目未选分区按钮、对象 kicker、方法论文件路径（6 个节点）；在相同字体/字号/间距下仅改用已有 `ink2`，约 7.05:1。首次 `heading-order`（接入页 h1→h3、PR 页 h2→h4）和 `label-content-name-mismatch`（短 sha 复制按钮）也已修正。最终 [全页面 axe 结果](axe-all-pages.json) 为 14 页×2 主题全部 0 violations；未禁用规则。[焦点/退场/375px 结果](axe-violations.json) 三类面板也全部为 0。

验证：`cd web && npm ci`、`npm run build` 均通过。最终 `PLAYWRIGHT_PORT=5277 npx playwright test --workers=2` 为 **108/109**：唯一失败是 B 已有的慢 PR 测试要求 Agent 首次可见 <1000ms，本机实测 1139ms；`--workers=1` 全套同为 **108/109**，该项 1057ms。该项单独重试实测 966ms 通过，未改数据层或放宽阈值。D 新增的慢/失败方法论 2/2、旧版本 1/1、视觉与 axe 3/3 均通过；[最终 axe 结果](axe-all-pages.json) 的 28 个页面/主题组合均为 0 violations。测试配置在没有预先运行参考服务时会启动设计稿服务；预览入口仅位于 `web/tests/round8-panels-preview.tsx`，不进入生产入口或向真实 API 发请求。

最终组合截图已在集成分支重拍并重新拼接；最新审查修复、156项完整测试及性能结果见[集成记录](../round8-integration.md)。本索引早期独立分支数据保留为开发过程记录。
