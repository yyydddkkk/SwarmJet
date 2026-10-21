# 第七轮：星图前端对接平台，以及 pstack 调整

日期：2026-10-20。第六轮已经提供前端需要的全部后端能力（见 [round6-prompts.md](round6-prompts.md) 的公共契约和 [acceptance-round6.md](acceptance-round6.md)）。这一轮把 [设计稿](../../design/stars/index.html) 做成真正的产品前端，替换现有页面，并处理第六轮验收留下的两个问题。

## 已定决策

- **设计依据**：视觉、交互、文案以 [design/stars/index.html](../../design/stars/index.html) 和 [frontend-stars-design.md](frontend-stars-design.md) 为准。设计稿和文档有出入时，以文档的命名表和原则为准，再参考设计稿的实现细节。实现时发现设计稿本身有问题，按原则处理并在汇报里说明，不要私自改设计方向。
- **命名**：界面文字一律用大白话（文档第 2 节）。代码、API、数据库里的 hive、queen、forager 不改。
- **技术栈**：沿用 web/ 现有的 React 19 + Vite + TypeScript。
  - 星图用 Canvas 2D 自己画，不引入 UI 组件库。
  - 不引入图形引擎，除非性能测试证明 Canvas 2D 不够；需要引入时在 PR 里给出测量数据和理由。
  - 新增运行时依赖要在 PR 里逐个说明用途。
- **路由**：新界面成为默认入口。旧页面在本轮内临时挂在 `#/legacy/...` 下，等功能逐项对齐后在集成阶段删除，本轮结束时不保留旧页面。
- **字体随前端打包**：自托管部署可能没有外网，不再用 Google Fonts。
  - IM Fell English、Source Serif 4、IBM Plex Sans、JetBrains Mono 都是 OFL，放进 `web/public/fonts/`，并附带许可证。
  - Noto Serif SC 和 Noto Sans SC 按界面实际用字加常用汉字做子集，在 PR 里写明子集生成命令和体积。
- **不改后端**：后端接口以第六轮契约和现有 `internal/api` 为准。发现缺口时写进汇报，由用户决定，不在本轮顺手改后端。任务 E 改的是方法论包，不算后端改动。
- **合并顺序**：A → E → C → D → B。A 是其他前端任务的基础，最先合入；B 改动最大，最后合入。

## 前端内部契约（任务 A 实现，B、C、D 只通过它取数据）

位置：`web/src/data/`。

**类型**：从现有 `internal/api` 的实际 JSON 推导 TypeScript 类型，不要凭空发明字段。至少包括：

- Hive、Agent（含 costUsd、issueNumber）、Message、PullRequest；
- QueueItem、Signal、MethodologyEvent、MethodologyVersion；
- Usage、SetupStatus、ModelsState、Token。

**数据流**：

1. 打开项目时拉快照：hive、foragers、queen、prs（state=all）、queue、signals、methodology（当前版本和事件）、最近的消息。
2. 订阅 `GET /api/events?hive=...`。每次收到 `ready`（包括断线重连后）都重新拉快照，再继续应用增量。
3. 增量规则：

   | 事件 | 处理 |
   |---|---|
   | message | 追加，并触发一次光脉冲 |
   | agent | 按 id 更新或插入 |
   | pr | 按 number 更新或插入 |
   | methodology | 追加到事件列表，并重新拉当前版本 |
   | queue | waiting 时插入或更新；started 或 expired 时移除 |
   | hive | 更新项目 |

4. 断线后按指数退避重连，最长 30 秒。连接状态对外暴露，界面要能显示"连接已断开，正在重连"。

**派生数据**（集中写在数据层，界面只读结果）：

- **星图节点**：
  - 恒星：Queen；
  - 彗星：role 不是 verifier 的 Agent；
  - 卫星：verifier，按 prNumber 挂到对应的 PR 上；
  - 带环行星：PR；
  - 边界外的十字：signals；
  - 边界上的空心小圈：queue。
- **光脉冲的起点和终点**：由消息的 from/to 映射到节点：
  - `queen` → 恒星；
  - `forager:<id>` → 对应 Agent；
  - `hive` 和 `system` → 项目边缘；
  - `local:*` → 观测者 ⊙。
- **等你决定的事**（⊙ 发光的依据）：
  - ready 且未合并、且方法论的合并策略不是自动合并的 PR；
  - 发给当前网页管理员、且还没有回复的 question。
- **Agent 旁边的一句话状态**：取这个 Agent 最近一条外发消息的 preview，截成很短的一句；没有消息时用状态词（工作中、空闲、失败、已完成）。空闲超过 6 小时且仍占名额时，写成"空闲 N 小时，仍占着名额"。

**操作**：

| 操作 | 做法 |
|---|---|
| 对主 Agent 说话 | 发消息给 queen |
| 插话 | 发消息给 `forager:<id>` |
| 派活 | 把 Issue 拖到恒星上，等于给 queen 发一条"请处理 Issue #n"的消息，由 Queen 决定怎么派。不直接调用 spawn，保持 Queen 路由 |
| 合并 PR | 必须带当时看到的 headSha；收到 409 时提示"PR 有新提交，请重新查看"并刷新 |
| 其他 | 取消 Agent、修改项目配置、方法论的安装/激活/回滚、模型凭证、MCP 连接，复用现有 API |

**测试工具**：提供可注入的 mock 数据源，包括快照数据和可以手动推送事件的假 SSE，供 B、C、D 和 Playwright 使用。

## 任务

### 任务 A：数据层与新界面骨架

````text
# 任务：星图前端的数据层与新界面骨架

## 背景
SwarmJet 的前端要换成一张实时星图。先读：
- docs/claude/frontend-stars-design.md（全部）
- design/stars/index.html（可运行的设计稿，`python3 -m http.server 4731 --directory design/stars`）
- docs/claude/round7-prompts.md 的"已定决策"和"前端内部契约"
- docs/claude/round6-prompts.md 的"公共契约"
- internal/api 下 events.go、prs.go、web_lists.go、hives.go、methodology.go 的实际响应结构
- web/src/hive/api.ts、use-resource.ts（现有调用方式）

## 要做的事
1. 按"前端内部契约"实现 web/src/data/：类型、快照拉取、SSE 订阅与重连、增量应用、派生数据、操作函数、连接状态、mock 数据源和假 SSE。
2. 新界面骨架：
   - 新的默认路由；全屏画布容器；
   - 左上角标识和路径，左下角标题框，右下角工具栏，底部对话线；
   - 这些元素先接上真实数据，但内容可以先占位，B、C、D 会填充。
   - 旧页面整体挪到 `#/legacy/...`，保证仍能用。
3. 主题基础设施：
   - 按设计稿的 `THEMES` 结构实现主题表（明亮、纸本、夜空、跟随系统）和"经典/简洁"风格；
   - 通过 CSS 变量和一个可订阅的主题对象提供给画布；
   - 选择存进 localStorage，读写都要 try/catch。
4. 字体：按"已定决策"打包字体并附许可证，删除对 Google Fonts 的依赖。

## 约束
- 不改后端（internal/、deploy/、cmd/）。不对整个文件跑格式化工具以外的大范围重排。
- 代码、测试里不得出现真实凭证。

## 验收标准
- `cd web && npm ci && npm run build` 通过；`npx playwright test` 通过。旧测试改为访问 `#/legacy/...` 后仍能通过。
- 新增测试：
  - 快照加 SSE 增量正确合并；
  - ready 后重新拉快照；
  - 断线重连退避；
  - 409 合并提示；
  - 主题切换写入 CSS 变量并被记住。
- PR 描述里列出数据层对外的类型和函数清单，供 B、C、D 使用。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

### 任务 B：星图主体

````text
# 任务：把设计稿的星图做成真实数据驱动的画布

## 背景
先读 docs/claude/frontend-stars-design.md、design/stars/index.html，以及 docs/claude/round7-prompts.md 的"前端内部契约"。任务 A 提供的数据层（web/src/data/）是唯一的数据来源；从 A 的分支开始工作。

## 要做的事
1. 把设计稿的画布移植成 React 组件，渲染逻辑和 React 状态分离：画布每帧只读数据层的派生结果。要实现：
   - 三层缩放：全部项目、项目、单个对象；
   - 平移、双击回到全部项目；
   - 进入项目时其他项目退成远景；
   - 每种对象的剪影：恒星、彗星（尾巴背向恒星，有彗发）、空闲（空心头加虚线短尾）、失败、带环行星、等你决定的强调色虚线环、卫星、十字、菱形、排队小圈；
   - 淡斜体注记和一句话状态；
   - 主题表里的渲染参数：纸纹、点线、刻度、托盘、线宽、尺寸、星点、经纬线；
   - 简洁风格。
2. 光脉冲只来自真实的 message 事件，不随机生成。合并成功时 PR 螺旋并入恒星，owner 完成后留下星轨。
3. 交互：
   - 点对象打开详情（详情面板由任务 C 提供，这里只负责触发和镜头）；
   - 把未认领的 Issue 拖到恒星上派活；
   - 观测者 ⊙ 在有等你决定的事时发光；
   - 悬停高亮。
4. 时间轴：按设计稿做 mock 版。拖动时冻结画面并画出示意星轨，"曝光"播放回放。界面上要有一行小字说明这是示意，不代表真实历史。真实历史留给后续的可观测性工作。
5. 性能：
   - 远景降低细节（只画点）；
   - 画布只在有动画或数据变化时重绘，空闲时不持续占用 CPU；
   - 尊重"减少动态"；
   - 用 mock 数据源构造 300 个 Agent 和 50 个 PR 的场景测帧耗时，在 PR 描述里给出数据。

## 约束
- 不改后端；只通过数据层取数据和发操作。不得出现真实凭证。

## 验收标准
- build 和 Playwright 通过。
- 新增测试（用 mock 数据源和假 SSE）：
  - 推送 agent 事件后节点出现或变化；
  - 推送 message 事件后产生脉冲（可以通过暴露给测试的计数器断言）；
  - 拖拽 Issue 到恒星后发出给 queen 的消息；
  - ⊙ 在有 ready PR 时出现发光状态；
  - 三种主题和简洁风格下画布都能渲染（截图对比或像素抽样）。
- 设计稿里每个交互在新画布里都有对应实现，在 PR 描述里逐项打勾。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

### 任务 C：详情、项目面板、平台设置

````text
# 任务：星图旁的三类面板

## 背景
先读 docs/claude/frontend-stars-design.md 第 2、4 节和第 8 节，design/stars/index.html 里的 lens、almanac、appendix 部分，以及 docs/claude/round7-prompts.md 的"前端内部契约"。从任务 A 的分支开始，数据和操作只通过 web/src/data/。现有 web/src/hive/ 下的 ForagerPage、HivePage、MethodologyPage、HiveSettings、UsagePanel、SetupPage、ModelsPage、ConversationStream 已经实现了很多逻辑和表单，优先复用它们的 API 调用和校验逻辑，换成新的视觉。

## 要做的事
1. **对象详情**（右侧）：
   - Agent：对话和插话、交接摘要、Diff、终端、验证与合并、花费；按需折叠，默认先给"此刻在做什么"和最近的工具调用。
   - 主 Agent：完整对话。这就是原来的 Hive 对话页，包括首次引导和方法论选择。
   - PR：验证结论、合并条件、带 headSha 的合并按钮。
2. **项目面板**（左侧）：
   - 方法论：轨道小图、大白话的事件规则、文件列表、版本历史与回滚、从内置目录安装；
   - 设置：同时工作的 Agent 名额、主 Agent 启用/停用、主 Agent 模型；
   - 花费：7 天走势、今天和本周、方法论预算；
   - 记忆：占位。
3. **平台设置**（底部升起）：
   - 接入：GitHub App、隧道、最近投递、重新同步；
   - 模型：凭证状态，以及更换凭证的表单；
   - 连接：MCP 连接列表、新建（Token 只显示一次）、吊销；
   - 账簿：各项目花费；
   - 账号：退出登录。
   原 SetupPage 里一键创建 GitHub App 的完整流程也要搬过来。
4. 同屏规则：最多同时左右两层；平台设置单独出现；Esc 逐层退出；焦点管理和读屏标签正确。

## 约束
- 不改后端。凭证表单里输入的值不得写进 localStorage、URL 或日志。不得出现真实凭证。

## 验收标准
- build 和 Playwright 通过。
- 新增测试覆盖：
  - 每个面板的打开和关闭；
  - 插话发出正确的消息；
  - 方法论激活与回滚调用正确的接口；
  - 并发名额修改走 PATCH 并带 CSRF；
  - 合并带 headSha；
  - Token 只显示一次；
  - 凭证输入不出现在任何持久化位置。
- 在 PR 描述里给出"旧页面功能 → 新位置"的对照清单，逐项说明是否已对齐。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

### 任务 D：外围体验

````text
# 任务：简报、列表、图例、显示设置、命令行、登录与首次使用

## 背景
先读 docs/claude/frontend-stars-design.md 第 4、6、8、9 节，design/stars/index.html 里的 brief、catalogue、legend、display、say/route 部分，以及 docs/claude/round7-prompts.md 的"前端内部契约"。从任务 A 的分支开始，数据只通过 web/src/data/。

## 要做的事
1. **简报**：默认打开，可以设为下次直接进入星图。内容全部来自真实数据：
   - 需要你：等你决定的事；
   - 正在进行：工作中的 Agent，最多 3 条，其余链接到列表；
   - 需要留意：失败的 Agent、凭证失效、空闲很久仍占名额的 Agent；
   - 昨晚完成：过去一夜合并的 PR 数、完成的 Agent 数和花费。
   点任意一条飞到星图上对应的地方。
2. **列表**：全屏文字视图，作为键盘和读屏用户的完整入口。
   - 按"有事等你优先、停用的排最后"分组，可以搜索；
   - 点行飞到对象；
   - 星图上能做的操作，在列表里都能做到：插话、合并、取消、打开详情。
3. **图例**：首次进入星图时自动展开几秒，之后从工具栏打开。
4. **显示设置**：
   - 风格：经典或简洁；
   - 主题：从主题表生成选项，外加跟随系统；
   - 经纬线：显示或隐藏。
5. **对话线兼命令行**：
   - 认得项目名、`PR #n`、`#n`，以及模型、接入、连接、账簿、方法论、列表、时间等词；
   - 输入时提示回车会做什么；
   - 长句子照常发给主 Agent。
   快捷键：`/` `A` `,` `T` `C` `B` `L` `S` `G` `D` `Esc`。
6. **登录页**：夜空或纸面背景，中间一条输入线；错误提示说清楚原因。
7. **首次使用**：三颗暗星连成小星座，分别是接入 GitHub、填写模型凭证、创建第一个项目，完成一步点亮一颗。数据来自 `GET /api/setup/status`。创建项目时在全部项目视图点空白处，选择仓库后自动打开主 Agent 的详情。
8. **加载、空状态、错误状态**：API 断开、登录过期、SSE 断线、凭证失效时，说清楚发生了什么、怎么处理，不道歉。
9. **手机宽度**：保证能看到等你决定的事，并能完成合并或回答。

## 约束
- 不改后端。localStorage 只存界面偏好，读写都要 try/catch。不得出现真实凭证。

## 验收标准
- build 和 Playwright 通过。
- 新增测试覆盖：
  - 简报四组内容来自 mock 数据；
  - 列表搜索和跳转；
  - 命令行区分跳转和发消息；
  - 所有快捷键；
  - 首次使用三步的点亮逻辑；
  - 登录失败提示；
  - SSE 断线提示；
  - 手机宽度下完成一次合并。
- 用 axe 或等价工具对登录页、列表、简报做无障碍检查，零严重问题。

## 交付
提交信息用英文约定式前缀（feat:）。向 dev 开 PR，不要合并。
````

### 任务 E：pstack 调整

````text
# 任务：根据第六轮验收调整 pstack 方法论

## 背景
第六轮真实验收（docs/claude/acceptance-round6.md 第 9 节）发现两处工作方式问题：
- verifier 把"违反 Issue 明确的兼容要求"当成了非阻塞警告；
- Queen 在同一个 PR head 上重复派 verifier，而 pr_head 规则已经自动派了。
这两点属于方法论，按 ADR 第 2 节，不改平台代码。先读这一节、methodologies/pstack/ 下的全部文件，以及 internal/methodology/packages_test.go。

## 要做的事
1. roles/verifier.md 和 skills/verify-revision/SKILL.md：
   - 写明 Issue 要求"保持原有行为"或给出兼容要求时，必须在基线提交上运行同样的检查做对照；
   - 行为发生了 Issue 没有允许的变化时，结论是 fail，不是 pass_notes。
   - 给一个正反对照的小例子（可以参考第六轮无效 UTF-8 退出码的例子，但不要照抄测试仓库的代码）。
2. queen.md（pstack 的 Queen 风格补充）：
   - PR 每次有新 head，平台会按 pr_head 规则自动派 verifier；
   - Queen 不要再为同一个 PR 和 head 派 verifier；
   - 只有自动派出的 verifier 失败或被取消时才补派，补派前先确认同一个 PR 和 head 没有工作中或排队中的 verifier。
   - Queen 做取舍时，Issue 写明的兼容范围优先于实现上的方便。
3. 提高内置目录里 pstack 的版本号，让已安装旧版本的项目能看出有新版本。
4. 不改 methodology.yaml 里的事件规则和合并策略。

## 约束
- 只改 methodologies/pstack/ 下的文件和必要的版本号；不改 internal/ 的逻辑代码（版本号如果在代码里登记，可以只改那一处常量）。
- 不得出现真实凭证。

## 验收标准
- go test ./internal/methodology/ 通过；带 TEST_DATABASE_URL 的 go test -race -p 1 ./... 通过。
- PR 描述里贴出改动前后的关键段落对照。

## 交付
提交信息用英文约定式前缀（docs: 或 fix:）。向 dev 开 PR，不要合并。
````

## 集成与验收

1. **开发顺序**：先开发 A 和 E。A 的 PR 审查通过后，B、C、D 从 A 的分支开始并行开发。
2. **集成**：按 A → E → C → D → B 合并到集成分支，每次合并后跑 `cd web && npm run build`。冲突以本文契约为准。
3. **删除旧页面**：C 的对照清单全部对齐后，在集成分支上删除 `#/legacy/...` 和旧页面代码。旧的 Playwright 测试要么迁移到新界面，要么随页面一起删除，并在集成 PR 里列明。
4. **全量测试**：
   - 带 TEST_DATABASE_URL 的 `go test -race -p 1 ./...`；
   - `python3 -m unittest discover -s deploy -p 'test_*.py'`；
   - compose config 校验；
   - `cd web && npm ci && npm run build && npx playwright test`。
5. **开集成 PR**，等用户批准后合并。
6. **升级部署后做真实验收**：本机部署 sj-r3，平台镜像包含前端。用真实浏览器（Playwright，不用 mock）验证：
   1. 登录，看到简报，内容和数据库一致；进入星图，看到测试项目里真实的 Agent、PR 和排队。
   2. 在测试仓库创建一个小 Issue（先告诉用户内容）。在星图上依次看到：边界外出现十字、脉冲飞向恒星、新彗星出发、PR 行星出现、verifier 卫星、⊙ 发光。
   3. 征得用户同意后，从 ⊙ 合并。确认 PR 并入恒星、owner 完成并留下星轨、Cell 被清理。
   4. 三种主题和简洁风格都截图；列表、项目面板、平台设置都能打开并显示真实数据。不新建 MCP 连接，不填写凭证。
   5. 通过主 Agent 对话或项目面板，从内置目录安装新版 pstack。确认生成新的生效版本（版本号由平台分配），绕开 v4 上的旧回滚标记；确认 Queen 仍开启。
   6. 断开再恢复 api 服务，确认界面提示断线并在恢复后重新拉快照。
   7. 结果写进 docs/claude/acceptance-round7.md，截图放在 docs/claude/acceptance-round7/。截图里不能出现任何凭证。
