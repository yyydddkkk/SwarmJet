# 第七轮任务 C：面板迁移对照

实现分支：`feat/frontend-panels`，基于审查通过的任务 A（`ce3bd86`）。仅修改 C 面板、测试及经主代理明确授权的 `stars/ui.ts` 定向关闭方法。没有后端改动，没有新增运行时依赖。

## 数据、视觉与交互

- 组件只通过 `src/data` 的 `useDataAPI()` 发操作、读取共享 `props.stores`；不另建 store 或 SSE。
- 左右薄面板和底部平台设置沿用设计稿 lens、almanac、appendix 的尺寸、留白、边线与主题变量。没有导入旧页面 CSS、组件或蜂巢图标；所有样式独立使用 `pc-` 命名。
- `panels/format.ts`、`diff.ts` 保留旧纯逻辑，已复制到新目录；新面板没有任何 `hive/` 目录依赖。集成删除旧页面无需为 C 保留旧目录。
- 面板 X 使用 `closeProject/closeObject/closePlatform` 定向关闭；新增 `closeOverlay` 供外围使用。原 `close()` 的 Esc 层序保持不变，C 没有注册全局 Esc。嵌套确认框仅在自身处理 Esc，维护焦点 trap 和归还。
- 完整会话遍历 inbox 分页、去重并按 `Date.parse(createdAt)` 排序。共享 SSE 的 preview 仅用于触发正文重新读取，不当作完整消息。
- 合并只调用共享 `store.mergePR(number, observedHeadSha)`；显示的 head 与请求一致，busy 加同步锁防重，409 使用共享中文提示与刷新。

## 旧页面功能 → 新位置

以下“已对齐”指旧页面真正存在的功能已迁移；原占位与批准缺口单独标明，不把占位当作实现。

| 旧页面 / 功能 | 新位置 | 对齐情况与证据 |
|---|---|---|
| HivePage 主 Agent 状态、停用提示与开启入口 | 右侧主 Agent 详情 → 此刻在做什么 / 项目设置 | 已对齐，`ObjectPanel.tsx:40`；状态来自共享快照 |
| HivePage 完整对话、当前管理员收件箱、主 Agent 发给项目的首次 bootstrap | 右侧主 Agent 详情 → 对话 | 已对齐；包括全部 `to=queen`、`from=queen` 和当前管理员收件箱；全文分页读取、来源 trust、时间排序与去重 |
| HivePage 发消息、首次引导、选择方法论 | 右侧主 Agent 详情底部输入 / 内置目录 | 已对齐；主 Agent 关闭时仍显示开启入口；没有直接 spawn |
| 当前管理员提问与回复 | 右侧详情 → 回复这条提问 | 已对齐；回复给实际发问者，保留提问的 chainId，并标记 reply |
| HivePage Inbox 未认领事件全文与来源 | 列表 / 画布选事件 → 右侧待处理事件 | 已对齐 C 的详情；显示真实事件正文、来源、时间、GitHub 链接；派活给主 Agent。列表与事件入口由 B/D 提供 |
| HivePage 原手动派出表单 | 底部对主 Agent 说 / 事件详情请主 Agent 处理 | 按本轮契约改为主 Agent 路由，明确不保留直接 spawn |
| HivePage 项目列表、Swarm、待处理入口 | 画布 / 列表 | B/D 所有，不由 C 重建第二套列表 |
| ForagerPage 当前状态、任务、角色、分支、PR、parent、revision、chain | 右侧 Agent 详情 → 此刻在做什么 | 已对齐，`ObjectPanel.tsx:40`；PR 可在验证与合并中查看 GitHub 链接 |
| ForagerPage 完整对话与插话 | 右侧 Agent 详情 → 对话及输入线 | 已对齐；包括发给 Agent、Agent 对主 Agent / 项目 / 当前管理员的往来，插话保留链 |
| ForagerPage 取消工作 | 右侧 Agent 详情底部取消 Agent | 已对齐；终态不显示取消，错误可重试 |
| ForagerPage handoff 目标、进展、剩余、风险、下一步、产物 | 右侧 Agent 详情 → 交接摘要 | 已对齐，按需展开；Agent 详情通过数据层读取 |
| ForagerPage PR readiness、条件、检查结果、检查输出与链接 | 右侧 Agent 详情 → 验证与合并；或 PR 详情 | 已对齐，`ObjectPanel.tsx:471` |
| ForagerPage 结论历史、提交、作者标记、作废状态、时间、评论、findings | 同上 → 验证结论历史 | 已对齐；保留每条发现的位置、严重程度与证据 |
| ForagerPage 花费、tokens、调用次数、角色、各模型拆分 | 右侧 Agent 详情 → 花费 | 已对齐，`UsagePanel.tsx:159`；不借用其他 Agent 用量 |
| ForagerPage Diff、终端、时间线 | 右侧 Agent 详情同名折叠项 | 原页面本来占位；继续明确“平台暂未提供”，不制造 Diff、终端或历史数据 |
| 最近工具调用 | 右侧 Agent 详情 → 此刻在做什么 | 后端没有提供工具调用，明确暂未提供，下方为真实消息，不制造工具日志 |
| MethodologyPage 角色、模型、生命周期、提示词、权限、禁用工具 | 左侧项目面板 → 方法论 → 角色与权限 | 已对齐，`MethodologyPage.tsx:392`；附角色分工小图 `:1144` |
| MethodologyPage 常驻规则、事件过滤与角色路由、合并策略、上限 | 左侧方法论各分节 | 已对齐；已知事件用大白话说明，保留实际过滤条件；预算从真实 manifest 读取 |
| MethodologyPage 任意版本查看、源 URL/ref/SHA、digest、作者与备注 | 左侧方法论顶部及版本历史 | 已对齐；项目版本和目录来源版本独立说明 |
| MethodologyPage 文件列表、文件大小、完整文件内容 | 左侧方法论 → 文件 | 已对齐，`MethodologyPage.tsx:653` |
| MethodologyPage 两版本 manifest 与所有文件 diff | 左侧方法论 → 版本差异 | 已对齐，`MethodologyPage.tsx:924`；沿用防大文件冻结的纯 diff 逻辑 |
| MethodologyPage 版本激活 / 回滚、确认、错误与修改记录 | 左侧方法论 → 版本历史 / 修改记录 | 已对齐，`:727`；切换确认新增可选备注，使用 activate 接口，无虚构 rollback 端点 |
| MethodologyPage 内置模板目录、安装确认、失败重试 | 首次主 Agent 引导 / 左侧方法论 → 从内置目录安装方法论 | 已对齐，`:277`；显示目录版本、已安装来源版本和更新提示，安装会创建并启用项目版本 |
| HiveSettings 并发名额、主 Agent 开关、主 Agent 模型 | 左侧项目面板 → 设置 | 已对齐，`HiveSettings.tsx:16`；真实 PATCH + CSRF；模型仅列已有凭证的提供方，保留不可用当前项，保留只读方法论上限 |
| UsagePanel 今天总额、Agent 排名、tokens、未知价格提示、刷新 | 左侧项目面板 → 花费 | 已对齐，`UsagePanel.tsx:29`；排名仍可打开对应 Agent 详情；未知价格说明金额不完整 |
| 7 天走势、本周花费 | 左侧花费 / 平台账簿 | 用户批准的接口缺口；显示暂时无法获取，只请求 today / all，不伪造历史 |
| 方法论预算 | 左侧花费与设置、方法论上限 | 已对齐；零值按旧逻辑视作未设置，不把未知预算包装成真实额度 |
| 记忆 | 左侧项目面板 → 记忆 | 需求本来占位，明确暂未提供 |
| SetupPage GitHub 已配置状态、来源、App ID、安装数、管理与安装链接 | 平台设置 → 接入 | 已对齐，`SetupPage.tsx:220` |
| SetupPage 一键创建 App、可选组织、manifest、隐藏 form POST、回调通知、安装链接、刷新 | 平台设置 → 接入 | 完整流程已对齐；`setupQuery` 的 connected/error 通知保留；创建向 GitHub POST，不把 manifest 放 URL |
| SetupPage 隧道地址、启用指引、真实同步状态与最近错误 | 平台设置 → 接入 → Webhook 隧道 | 已对齐，`SetupPage.tsx:366`；操作文案为重新读取 |
| 最近投递、手动重新同步 | 平台设置 → 接入 | 用户批准的接口缺口；明确暂时无法获取，不把读取称作同步 |
| SetupPage 模型状态与管理入口 | 平台设置 → 接入中的管理模型凭证；平台设置 → 模型 | 已对齐；不再跳转 legacy |
| SetupPage MCP 连接摘要列表、新建名称 / scope、确认吊销、错误重试 | 平台设置 → 连接 | 已对齐；列表仅摘要，吊销需要确认 |
| SetupPage Token 一次性明文、MCP 地址、Claude Code / Codex / JSON 配置与复制 | 平台设置 → 连接 → 新建连接的局部对话框 | 已对齐，`SetupPage.tsx:434`；明文仅局部组件内存，关闭即卸载，重开不显示旧明文 |
| ModelsPage 凭证摘要、网关状态、默认模型、目录、上下文与输出上限 | 平台设置 → 模型 | 已对齐，`ModelsPage.tsx:33`；仅展示服务器脱敏值 |
| ModelsPage 凭证输入、更换提供方、保存、删除确认、错误重试 | 平台设置 → 模型 | 已对齐；成功立即清空输入，切页 / 关闭卸载清空，不写 storage、URL、日志 |
| 各项目总账 | 平台设置 → 账簿 | 已实现，`ObjectPanel.tsx:594`；真实 today / all 分项目请求，未知价格明确标注金额不完整 |
| 原外壳退出登录 | 平台设置 → 账号 | 已对齐，`PanelsSlot.tsx:176`；只调用外壳 onLogout 一次，失败显示并可重试 |

## 验证

- 已执行 `cd web && npm ci`，随后 `npm run build`；成功。
- 最终全量命令 `PLAYWRIGHT_PORT=5276 npx playwright test` 通过 103 项（1.1 分钟），包含新增 C 面板 16 项。此前 98 项全量也已通过。
- 新增 `tests/panels.spec.ts` 16 项，包括每个分区开关、两侧定向关闭、插话、PATCH+CSRF、激活与回滚、headSha 与双击防重、409、一次性 Token、凭证内存清理、分页与实时正文、RFC3339排序、焦点、GitHub form POST、目录版本、提问回复、退出次数、事件正文与派活。
- Playwright 端口通过 `PLAYWRIGHT_PORT=5276` 隔离；默认保留 5274。
- 截图验证三种主题、项目面板、平台模型页、手机 PR 详情。截图在 Playwright 的 `test-results/panels-panels-remain-legible-in-each-theme-and-on-mobile/`，不含真实凭证。
- 未访问真实服务或使用真实凭证。设计稿中的随机工具日志不移植；后台没有提供的数据如实标明。

## 集成边界

- 全局快捷键、Esc 以及列表入口由 D 提供；C 的独立 harness 直接调用同一共享 UI 接口，集成时保留 D 的唯一全局监听。
- 本分支保留 A 的旧页面及原测试，C 没有删除其他任务的文件。集成可在核验本对照及 B/D 的旧入口后删除 legacy。
- 7 天 / 本周明细和投递 / 重同步为已批准缺口；昨晚合并数缺口归 D，不在 C 伪造。
