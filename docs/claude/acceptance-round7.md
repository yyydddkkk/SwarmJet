# 第七轮部署与真实验收

验收时间：2026-10-20（America/Los_Angeles，UTC 2026-10-21）。对象：本机 `sj-r3`、`http://localhost:8180`，测试项目 `680a4a31a140831b421fe16666bc3dfe`，仓库 `yyydddkkk/test-for-myself`。

## 范围与授权

用户批准后合并集成 PR [#75](https://github.com/yyydddkkk/SwarmJet/pull/75)，合并提交 `c4532b2215b288ae0275178c3424826530c9ef9b`。A～E 的 [#70](https://github.com/yyydddkkk/SwarmJet/pull/70)～[#74](https://github.com/yyydddkkk/SwarmJet/pull/74) 自动显示为已合并，无需另行关闭。

用户已批准保持前端范围并如实说明四项接口缺口：无日期明细，不能提供七天、本周、昨晚花费；无 webhook 最近投递与手动同步接口；PR 无 `mergedAt`，不能统计昨晚合并数；无最近工具调用轨迹。界面展示真实今天/累计用量、近期已合并 PR、当前同步状态与真实消息，缺项明确标明不可获取。时间轴仍为明确标注的示意回放。

本次使用真实 Chromium 浏览器和部署服务，未注入 mock、未拦截或伪造 API。登录从部署配置在进程内读取，Cookie 仅在内存中；未新增 MCP 连接、未读取或填写模型凭证。所有截图都来自真实部署，不复用开发阶段 mock 截图。

## 部署和收尾

| 操作 | 实际结果 | 结论 |
| --- | --- | --- |
| 规格副本逐字节核对后更新主检出 | 与 `origin/dev` 文件一致，移除未跟踪副本后 `git pull --ff-only` 成功，主检出仍为 `dev` | 通过 |
| 清理开发环境 | 删除本轮六个 worktree；停止 `sj-int-pg`；保留其他 worktree、`design/topology/` 和 `.claude/launch.json` | 通过 |
| 配置备份 | `deploy/.env.bak-r7`，0600，备份时字节一致；升级后密钥类变量与备份一致 | 通过 |
| 升级 | 在主检出 `deploy/` 执行 `python3 install.py upgrade --yes --image-source repo`，退出 0，平台及 Agent 镜像构建成功 | 通过 |
| 服务 | api、postgres、auth-broker、auth-gateway 健康；隧道运行，Compose 升级等待健康成功 | 通过 |
| 接入 | `/api/setup/status`：GitHub 已配置、1 个安装；`webhookSynced=true`、无最近错误；Cell/Docker 可用、已配置模型可用 | 通过 |

未回滚数据库，未删除数据卷或数据目录，未修改仓库设置。

## 真实界面与数据

| 项目 | 做了什么、看到了什么 | 结论 |
| --- | --- | --- |
| 登录和新界面 | 进程内 POST `/api/login`，带 Origin 与 `X-Requested-With: swarmjet`，打开新简报及星图，浏览器无脚本异常 | 通过 |
| 简报 | 昨晚完成 16 个；按 `completed` 的 `updated_at`、当地昨晚 18:00～今天 06:00 查询数据库也是 16。今天费用 $0.00，对应数据库今日 0 笔、$0 | 通过（按界面注明口径） |
| 简报限制 | 昨晚合并数/花费明确不可获取；显示真实近期已合并 PR #12、#8、#3。历史任务、旧失败和 PR #10 等均来自真实项目 | 通过（已批准边界） |
| 项目面板 | 显示真实方法论 v4/来源目录 1，安装后显示 v7/来源目录 2；规则、文件、历史可读 | 通过 |
| 列表 | 展示真实主 Agent、工作 Agent、PR、Issue 和待决定项，能进入对象详情 | 通过 |
| 平台设置 | 接入页显示真实 App、1 个安装、隧道地址及已同步状态；缺项明确说明 | 通过 |
| 主题与风格 | 明亮、纸本、夜空、明亮加简洁四组真实截图 | 通过 |

`updated_at` 是行的最后更新时间，不是独立完成时间；本次核对严格沿用界面说明。今日费用使用服务端 `date_trunc('day', now())` 的统计窗口；不把本次零费用解释成无历史模型调用。

初次简报上半部截图无法单独读到费用，初次查询结果已摘录到 [数据库证据](acceptance-round7/database-github-evidence.json) 的 `usageBeforeWorkflow`。完成真实任务后再次登录补拍 [简报下半部](acceptance-round7/17-final-brief-bottom.png)：仍显示昨晚 16 个，今天花费 $0.10；此时数据库为 69 笔、$0.103553，四舍五入一致，近期合并列表已更新含 PR #14。接入完整状态另见 [安全字段摘录](acceptance-round7/setup-status-evidence.json)。

截图：[简报](acceptance-round7/01-brief.png)、[安装前项目面板](acceptance-round7/02-project-before.png)、[真实列表](acceptance-round7/05-live-list.png)、[真实接入](acceptance-round7/06-live-platform.png)。

主题：[明亮](acceptance-round7/theme-bright-classic.png)、[纸本](acceptance-round7/theme-paper-classic.png)、[夜空](acceptance-round7/theme-night-classic.png)、[简洁](acceptance-round7/theme-bright-simple.png)。

## 安装新版 pstack

通过“项目面板 → 方法论 → 从内置目录安装方法论 → 安装 pstack → 确认安装”，没有直接修改数据库。

安装前：项目 v4 生效，来源 `builtin:pstack`、目录版本 1，`auto_rolled_back_at` 有旧标记。安装后：平台分配项目 v7，来源目录版本 2，`active=true`，新版本无旧回滚标记；主 Agent `enabled=true`，之后正常派工。

结论：**通过**。[目录安装后截图](acceptance-round7/03-pstack-installed.png)、[新生效版本截图](acceptance-round7/15-reconnected.png)。目录版本与项目版本分别编号；v5/v6 是既有历史，所以新版本为 v7。

## 新 Issue 到合并的真实链路

创建前已向用户说明内容；[Issue #13](https://github.com/yyydddkkk/test-for-myself/issues/13) 只要求新增 `acceptance-r7.txt`，内容为 `Round 7 star map acceptance.` 加末尾换行，运行现有测试，不改其他文件、不自行合并。

| 环节 | 实际证据 | 结论 |
| --- | --- | --- |
| 问题标记 | 01:43:09Z 收到真实 GitHub 消息 `gh-4387c3663fc1d408a45c09a2e9d43bfa`，新增 Issue #13 十字；API signal 与 URL 对应 | 通过 |
| 消息脉冲 | 画布脉冲计数随真实消息增加；取证截图可见向主 Agent/worker 方向的消息轨迹 | 通过 |
| worker 出发 | 01:43:16Z 创建 `forager-5de1d3cdbe7a9450d36d619604830d03`，关联 Issue #13、使用新方法论版本；显示工作中节点 | 通过 |
| 评论触发 | 按已告知步骤发送 `/swarmjet run`，评论信号由同一 worker 领取，最终只创建一个 worker | 通过 |
| PR 出现 | [PR #14](https://github.com/yyydddkkk/test-for-myself/pull/14)，head `bbc5ef91357920d32a7af8704d8f616ed1108968`，只新增一个文件一行 | 通过 |
| 独立检查 | 平台 01:43:44Z 自动创建 `forager-c46d3117beea62ba670c3725`，对应 PR #14；画布显示 verifier 节点；01:45:13Z 同 head 非作者 `pass` | 通过 |
| 等待决定 | ⊙ 待决定数量由 4 变 5，列出 PR #14；从这里打开详情，读取同一完整 head，按钮可用 | 通过 |
| 合并及清理 | 界面只发出一次 POST，携带核验过的完整 head；返回 200、`merged=true`、`method=squash`。GitHub 确认合并提交 `89d351248c6d61b1fc96e5e7ffc2d6a0547d1177`；owner 在 01:46:30Z 进入 completed，owner 与 verifier Cell 均不再存在；画布 merges 从 0 到 1、trails 从 4 到 5 | 通过 |

独立复核：通过 GitHub 读取固定 head 的 diff、文件列表和归档；断言标记文件字节内容正确；在归档目录执行 `python3 -m unittest -v test_wordcount.py`，**8 项通过**。GitHub 检查列表为空；不将空列表表述为有 CI 运行。

用户单独批准合并测试 PR #14 后，才在真实 ⊙ 进入的 PR 面板点击“确认合并”。旧 PR #10 等未触碰。

截图：[创建前](acceptance-round7/04-before-issue.png)、[问题与派工](acceptance-round7/flow-004.png)、[检查中详情](acceptance-round7/07-pr-reviewing.png)、[PR 与 verifier](acceptance-round7/08-verifier-map.png)、[⊙ 待决定](acceptance-round7/09-observer-ready.png)、[可合并详情](acceptance-round7/10-pr-ready.png)、[真实合并动画](acceptance-round7/flow-018.png)、[完成后星图](acceptance-round7/16-final-map.png)。

## API 断开与恢复

工作完成后执行 `docker stop sj-r3-api-1`，真实页面出现“连接已断开，正在重连”，并覆盖在打开的项目面板上方。截图后执行 `docker start sj-r3-api-1`。浏览器记录到 1 秒、2 秒、4 秒递增的 SSE 重试间隔，01:47:50.927Z 收到真实 `ready` 事件，立即重新 GET 项目、Agent、主 Agent、PR、队列、信号、方法论与事件；随后重新读取各消息收件箱和已观察 PR #14 状态。

01:48:16.707Z 确认断线提示消失，项目面板仍显示 v7/来源目录 2；API 健康检查恢复 200。整个过程没有页面重载、没有补凭证，浏览器脚本异常为 0。结论：**通过**。

真实快照读取约 26 秒后才完成，因此提示不会在 SSE 刚连通时提前消失。这是本次观察值，不是普遍性能承诺。证据：[断线截图](acceptance-round7/14-disconnected.png)、[恢复截图](acceptance-round7/15-reconnected.png)、[浏览器事件和 GET 路径](acceptance-round7/browser-evidence.json)。

## 新发现和建议（本 PR 不修代码）

### 属于平台

本次没有新发现阻断基本链路的平台错误。真实恢复时，SSE 连通后完整快照仍需约 26 秒；建议后续评估 PR 上游查询与历史收件箱读取耗时，尚未做归因或压力测试。已批准的四类接口缺口继续保留为后续平台能力需求。

### 属于方法论

内置目录的说明仍为英文（真实方法论元数据），安装对话框也沿用该说明；建议后续提供中文用户说明。测试 PR 合并后 Issue #13 仍为 open，本任务没有要求自动关闭 Issue；如希望自动收尾，建议方法论交付说明要求 PR 写明关闭 Issue 的关联语句。本轮新 pstack 未重复派 verifier；是否覆盖所有重复 head/失败补派组合，**未验证**，不以这一个小任务外推。

### 属于前端

真实项目有大量历史 Agent、事件和长任务文字，星图文字存在遮挡，列表和简报中的历史长任务也很占空间。PR 尚无验证时，后端原因 `no attestation for the current head` 直接显示为英文，建议后续将已知原因转成中文。建议后续做文字避让、摘要截断及历史状态筛选；本次未修改设计或代码。

构建时 npm 报出 2 项 high。使用官方 registry 的 `npm audit --registry=https://registry.npmjs.org --json` 复核：`source-map-js@1.2.1`、`vite@7.3.6`，均是构建工具链依赖；这些版本在本轮前的 dev 也已存在。已记录具体公告与范围至 [审计结果](acceptance-round7/dependency-audit.json)，建议另开依赖升级任务；本轮未运行自动修复。

## 证据说明

本 PR 只包含中文验收记录、真实截图和经过挑选的非敏感证据。没有凭证、Cookie、请求认证头、完整数据库导出或模型 key。文档/JSON已做真实配置敏感值与常见密钥模式扫描，截图经逐张视觉复核；图片无嵌入元数据。`flow-*.png` 来自真实画布对象/脉冲/合并/完成变化的自动截图，变化数据汇总于 [浏览器证据](acceptance-round7/browser-evidence.json)；最终状态见 [数据库和 GitHub 证据](acceptance-round7/database-github-evidence.json)。只保留关键变化截图，JSON 中的 index 与对应 flow 文件名编号一致。开发阶段的完整自动化测试和 300 Agent 性能数据见 [集成记录](round7-integration.md)，不把它们计为本次真实部署表现。

## 验收结论与未验证项

本次小 Issue 的真实处理、独立验证、用户批准合并、owner 完成与 Cell 清理全链路通过；pstack 新版本安装、真实数据入口、主题风格及断线恢复通过。无代码修复混入本验收 PR。

未验证：真实手机合并（开发阶段 mock 测试已覆盖，本次真实合并在桌面完成）、300 Agent 真实运行规模、pstack 所有兼容回归及补派场景、缺失后端接口对应的功能。上述项不声称通过。英文提示与密集文字问题、构建依赖公告按分类建议另行处理，等待用户决定。
