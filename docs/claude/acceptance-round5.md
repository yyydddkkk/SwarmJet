# SwarmJet 第五轮真实验收

验收日期：2026-10-19（America/Los_Angeles）；下文时间统一使用 UTC（2026-10-20）。记录分支：`docs/round5-acceptance`。验收契约见 [round5-prompts.md](round5-prompts.md) 的“公共契约”；第四轮问题背景见 [acceptance-round4.md](acceptance-round4.md)。结论汇总见第 10 节；未验证项不计为通过。

证据以部署日志、Postgres 查询、真实浏览器 DOM、OMP session 工具调用记录和 GitHub 对象为准。Queen 或 Forager 的回复仅记录其行为，不单独作为任务完成、测试通过或平台机制生效的结论。本文只保留查询摘要与关联 ID，不包含真实凭证、会话 Cookie、Token 或原始含密日志。

## 1. 部署与验收基线

| 项目 | 已核对结果 |
|---|---|
| 部署代码 | `f23de0d`：第五轮集成 #55（`e30b789`），镜像 COPY 修复 #56（`9aff729`），Queen 流式正文及默认页修复 #57（`f23de0d`） |
| 数据库 | schema 13 |
| 服务 | api、postgres、auth-broker、auth-gateway 均 healthy；tunnel 运行，webhook 地址已同步 |
| 配置与密钥 | `.env`、`.env.bak-r5` 权限均为 `0600`，密钥未变；不在本文展开配置值 |
| Hive | 唯一现有 Hive：`680a4a31a140831b421fe16666bc3dfe`，仓库 `yyydddkkk/test-for-myself` |
| 升级前状态 | Queen 关闭；已有 pstack 派生 v2 和 16 个历史 Forager；未清库，未创建新 Hive |
| Queen | Agent `8eece359bcfa5a97bd7730cebde27f09`；session `01a157a9-1d48-7055-bb31-26ab6ef00564` |
| 模型 | 默认 `deepseek/deepseek-v4-flash` |

本轮在保留历史数据的 Hive 上验收。已有 Hive 恢复 Queen 后的引导与网页可见性已有观察；**全新 Hive、未安装方法论时的首次引导尚未验证**，不能由此次观察替代。尤其不能据此判定新 Hive 默认不安装方法论、选择前不派 worker 等全部契约已通过。

## 2. Queen 引导回复与真实网页回归

**结论：本次引导介绍消息的完整正文保存及默认页可见性通过；全新 Hive 首次引导未验证。**

05:25:06，按用户授权发送引导介绍回归消息：

| 证据 | ID / 结果 |
|---|---|
| 输入消息 | `001a157c53c81-000001-62b4ca1a6d59c9b5502c76c650fbb93e` |
| Chain | `chain_f7169359` |
| 回复时间 | 05:25:16 |
| 回复消息 | `001a157c5641b-000002-4de22f5582628289530f149b99d51de6` |
| 正文核对 | 2477 字符中文；OMP session、数据库正文与实际 DOM 逐字一致 |
| 网页核对 | 真实 Playwright 浏览器，无 mock；Hive 默认页可见引导消息 |
| 副作用核对 | 此次回归前后方法论列表、Forager ID 列表未变 |
| 用量 | 此次回归 1 次模型请求，平台估算 cost `0.000899` 美元 |

原先回复落成单独 `.` / `。`、以及发到 Hive 的引导在默认页不可见的问题，已由 #57 修复并按上述链路真实回归。此结论仅覆盖该次消息的正文及展示；没有将 Queen 自述的介绍内容当作安装或派活成功的证据。

查询摘要：按消息 ID 核对输入、回复与正文；按 Queen Agent/session 核对实际输出；浏览器读取默认页实际文本并逐字比较；比较回归前后方法论版本与 Forager ID 集合；按该次请求查询用量账本。

## 3. 网页选择 pstack 与 Queen 实际安装

**结论：用户网页选择 → Queen 调用安装工具 → pstack 新版本生效通过。**

用户自行在网页发送“用 pstack”。输入消息 `001a157d1ec7f-000003-12637387416f46cb7c298803a5f5a2ef`，05:38:57.791 已 handled。Queen session 第 47 行记录实际 `write` 到 `xd://mcp__swarmjet_install_methodology_template`，参数为本 Hive 的 `hiveId` 和 `name=pstack`。

Postgres 版本记录与工具调用交叉核对如下：

| 字段 | 结果 |
|---|---|
| version / active | v4 / true |
| createdBy | `queen` |
| source | `{url: "builtin:pstack", ref: "1"}` |
| digest | `6654840d490d427ec1e8118b2871fd0ddf4e20b6fa932af98127edd9f0f37728` |
| createdAt | 05:39:05.745 |

05:39:13 有发给 `local:web-admin` 的完整回复，之后 Queen 为 parked。安装结论来自实际工具调用和数据库 active 记录，而非这条回复。本次是已有 pstack 派生版本的 Hive 安装内置模板，未验证空方法论 Hive 的首次安装路径。

## 4. 无标签 GitHub Issue 自主处理

按用户原授权，先告知任务内容后创建无标签 [Issue #7：feat: add raw byte counting with --bytes](https://github.com/yyydddkkk/test-for-myself/issues/7)。任务要求按原始二进制字节数计数，覆盖中文、无效 UTF-8、空文件、缺失文件退出码 2，保持原 words / `--lines` 兼容，补 CLI unittest 与 README，**暂不合并**。

Issue 创建于 05:41:23，GitHub 状态 `OPEN`、`labels=[]`。创建时 main 基线为 `4fd05d2b16845c67475629ce2ea0d162551d20aa`，仓库文件为 `README.md`、`wordcount.py`、`test_wordcount.py`、`.gitignore`，无 CI。所有以下 spawn 均由 Queen 或 Router 自动执行，验收主代理没有手工 spawn 此 worker 或 verifier。

**结论：无标签 Issue → Queen 自主派 worker → PR → 精确 head 独立 verifier → PASS → Queen 收到可合并通知的链路通过。** 主代理另行检出 PR 并运行真实测试，未仅依据 Agent 自述。合并后 owner 自动完成见第 6 节。

### 路由、派活与发布证据

| 时间 UTC | 日志 / DB / GitHub 核对结果 |
|---|---|
| 05:41:25.793 | api `event routed`：delivery `e30d69d4-cc48-11f1-91f9-5d7a04104008`，event `issues`，action `inbox`，target `queen`；消息 `gh-3f91d1a5ce50b7eb7be5314331b72a1b` 为 `trust=external`，已 handled |
| 05:41:26.760 | Queen turn started |
| 05:41:36.637 | 自主创建 worker `forager-d044ca50da3370ebab99cc70d222c40e`，role=`worker`，ParentID=`8eece359bcfa5a97bd7730cebde27f09`；任务消息 `msg-329c41d1f90fc08aba97a260dd5720ab`，from=`queen`，已 handled |
| 05:41:40.372 → 05:42:14.323 | worker 本轮开始 / 结束，发布 [PR #8](https://github.com/yyydddkkk/test-for-myself/pull/8)，之后 parked |
| 05:42:15.338 | 系统按 `pr_head` 创建 verifier `forager-22db7956b058aca1419640f5`，ParentID 为上述 worker，PR=8，Revision=`15529389b15e7b07ad756895316be224eefffc1a`；观察时 active |

PR #8 的真实 head 为 `15529389b15e7b07ad756895316be224eefffc1a`，含三个提交：`c2f1e88`（实现）、`5b60dd6`（测试）、`1552938`（README）。GitHub 文件变更统计为 `README.md` +9、`test_wordcount.py` +40、`wordcount.py` +10/-2。这些记录证明 PR 已发布，不单独证明实现满足要求或测试已通过。

GitHub 当前 `mergeable=MERGEABLE`、`statusCheckRollup=[]`，PR 尚未合并。仓库无 CI，不能把空检查列表写成“真实 CI 通过”；gate 在无 required checks 时按没有失败检查的语义放行，也不能替代实现与测试复核。

### 独立验证、gate 与完成证据

| 证据 | 结果 |
|---|---|
| DB attestation | `1729a575f1cf5e75f08ae55383599161`，05:43:12.628775 创建；PR=8，SHA=`15529389b15e7b07ad756895316be224eefffc1a` |
| 验证人与作者 | attester=`forager-22db7956b058aca1419640f5`，author=`forager-d044ca50da3370ebab99cc70d222c40e`，`is_author=false` |
| 结论 | `result=pass`，`findings=[]`；[GitHub attest 评论](https://github.com/yyydddkkk/test-for-myself/pull/8#issuecomment-6284288135) |
| verifier 完成控制消息 | `001a157d5dff3-00000a-c858b018b6d5188515940fccc5ababa8`，05:43:16.723 创建，From/To 均为 verifier 自身，kind=`control`，正文以 `complete` 换行 `PASS` 开始，已 handled |
| verifier 最终状态与 Cell | DB `completed`，updatedAt=05:43:19.364；其 Cell 容器已不在 `docker ps -a` 中 |
| 平台可合并通知 | `merge-3d81105a1edcdfde80004d44592faf86`，05:43:16.272，system → queen，kind=`report`，指出 PR #8（`1552938`）合并条件满足、可以合并 |
| 真实 gate API | `GET /api/hives/680a4a31a140831b421fe16666bc3dfe/prs/8`：`ready=true`、`reasons=[]`、`merged=false`、`checks=[]`，attest 对应上述 PASS |
| GitHub 复核 | head 仍为上述精确 SHA，`OPEN`、`MERGEABLE` |
| 主代理独立复核 | `gh clone` 到 `/tmp/swarmjet-r5-pr8-review`，检出并审阅 3 文件 +59/-2 的 diff；运行 `python3 -m unittest -v test_wordcount.py`，8/8 通过，耗时 0.091 秒 |

这里的 PASS 是非作者 verifier 的真实 attest，测试通过另有主代理独立执行结果；不依赖 Queen 所述“已做本地复核”。可合并通知发给 Queen 的结论由消息表证明。

附带观察：worker 数据库 `issue_number=0`，尽管任务来自 Issue #7；当前仅记录该字段，尚未判断影响或将其列为主验收阻断。

用户批准后 PR #8 已合并，owner 结束见第 6 节。本文不把 verifier 完成与 worker owner 完成混为同一结论。

## 5. Forager `send` 省略目标默认到 Queen

**结论：实际省略 `to` 的 question 投递给 Queen、Queen 回复 worker 通过。**

主代理经 API 向已 parked 的 worker 发送通信验收任务，要求调用 `send` 时省略 `to`，以 kind=`question` 询问 Issue #7 的缺失文件退出码。任务消息 `001a157d7816c-00000f-7922603f4d22736aee72d85a3d3a5e3f`，05:45:03.596 创建。此任务是通信探针，不是手工派生新 worker。

worker session `01a157d4-66d9-729b-9739-1c0c6dd26808` 的第 53 行记录实际 `write` 到 `xd://mcp__swarmjet_send`，参数只有 `hiveId`、`kind=question`、`body`，**没有 `to` 字段**，不是把 `queen` 显式作为目标后推断默认行为。

| DB 消息 | 时间 UTC | 实际去向与状态 |
|---|---|---|
| `001a157d7936e-000010-6cf9ec9114d80488a0ba8ca9f683878c` | 05:45:08.206 | worker → queen，question，已 handled |
| `001a157d7a935-000011-c99f9dac8f49344788222f1f93540762` | 05:45:13.781 | Queen → worker，说明缺失文件退出码 2、stderr 报错、stdout 为空 |

05:45:38.778 worker 再次 parked，PR head 未变。该链路以实际工具参数和数据库收发消息交叉证明，不以 Queen 自述证明收件目标。

## 6. PR 合并后 owner 自动结束

**结论：通过。** 用户批准后，06:01:07 用用户作用域 MCP 调用 `merge_pr({hiveId, pr:8})`。调用前 `pr_status` 显示 ready=true、reasons=[]，head 仍是 `15529389b15e7b07ad756895316be224eefffc1a`。工具返回 merged=true、method=squash，pin 住同一 head。

| 时间 UTC | 证据 |
|---|---|
| 06:01:14 | GitHub：PR #8 `MERGED`，合并提交 `86490fa5ac2a599e9d606a5d45072e0f6a6f608a` |
| 06:01:16.221 | 控制消息 `gh-be1749d33fca2b6a5a0401100cc8e813-complete`：system → owner，kind=`control`，trust=`system`，正文 `complete after_event PR #8 closed (merged=true)`，已 handled |
| 06:01:16.222 | 原 closed 事件 `gh-be1749d33fca2b6a5a0401100cc8e813` 作为本轮内容交给 owner（trust=`external`），已 handled |
| 06:01:17 | api 日志 `hive event routed`（delivery `a8a623f0-cc4b-11f1-8468-bc111ff066b5`，target 为 owner）；owner `turn started`，messages=2 |
| 06:01:24.835 | owner 最后汇报 `001a157e67a63-000018-…` → queen，写明 `PR #8 closed (merged=true)` |
| 06:01:24.840 | DB：owner `forager-d044ca50da3370ebab99cc70d222c40e` 为 `completed` |
| 之后 | owner 的 Cell 不在 `docker ps` 中；远端分支 `swarm/680a…/forager-d044…` 的 head 仍是 `1552938…`，没有再次发布 |
| 06:01:36 | Queen 向 hive 汇报合并完成，并在 main 上复核 |

次要观察：
- owner 最后一轮没有 `forager: turn finished` 日志，只有 started。结束路径少了一条可观测日志，不影响结果。
- 任务分支在 GitHub 上没有删除。这取决于仓库的"合并后自动删除分支"设置，本轮没有改仓库设置。

## 7. 并发上限与排队

**结论：排队、补派、去重通过；Hive 并发上限被方法论覆盖，不通过（平台问题，已记录未修）。**

开始前占用名额：第四轮遗留的 parked worker `forager-ef8e754363c1dbd8fc2f37f0faf71f0a`（PR 3）。Queen 不占名额。

### 7.1 Hive 上限 1 不生效

06:02 用 `PATCH /api/hives/{id}`，body 为 `{"config":{"maxConcurrentForagers":1}}`。响应 200，版本 2→3，`maxHops`、`triggers` 等其他字段保持原值（部分更新正确）。随后用 MCP `spawn` 派占位 worker A（`forager-7c3691938e865c757cc5bad8ace039d1`），任务要求只回复 ok、不动仓库。

已有 1 个占用，上限为 1，A 却**直接创建成功，没有排队**。原因：`internal/store/hive.go` 的 `createForagerTx`（MCP 的 memstore 同样）在方法论 `limits.maxConcurrentAgents` 非零时直接替换 Hive 的上限，pstack 写的是 4。

这和 ADR 第 2 节的边界相反：并发上限是用户设置的平台底线，方法论只能更严，不能放宽。用户已决定先记录，修复方向是**取两者较小值**（MCP memstore 同步修改）。

### 7.2 达到有效上限（4）后排队、去重、补派

| 时间 UTC | 证据 |
|---|---|
| 06:02:5x | 再派占位 B `forager-d05233e3…`、C `forager-7f75443c…`，均直接创建，有效占用达到 4 |
| 06:02:51 | 第 4 个请求 D（requestId `r5-conc-d`）返回 `queued=true`、`queuePosition=1`、agentId `forager-091c0cf361df98fc9595ed770ec1a7a6`；`spawn_queue` 出现 1 行 waiting |
| 06:03 | 用同一 requestId 重试，返回相同 agentId 和位置 1；`spawn_queue` 仍只有 1 行 |
| 06:03:12 | `cancel_forager` 取消 A，A 变为 `cancelled` |
| 06:03:28 | 监管者扫描补派 D（creating → 06:03:30 active）；`spawn_queue` 行变为 `started`；D 收到任务消息 `msg-73c09058…`（delivered） |

之后取消 B、C、D，把并发上限恢复为 4（Hive 版本 4）。几秒后它们的 Cell 被清理，测试仓库没有新的 PR 或分支。24 小时过期没有实测（单元测试覆盖）。

## 8. Queen 关闭时的回退

**结论：发给 hive 的消息回退通过；未认领 GitHub 事件的回退未实测。**

| 时间 UTC | 证据 |
|---|---|
| 06:04:03 | `PATCH {"queenEnabled":false}`，Hive 版本 5。Queen Agent 仍是 parked，Cell 保留（关闭不销毁 Queen Cell，只是不再路由给它） |
| 06:04:31 | MCP `send` to=`hive`：消息 `msg-47661319…` 的 to_addr=`hive`，state=`delivered`，留在 Hive 收件箱 |
| 06:04:41 | `PATCH {"queenEnabled":true}`，Hive 版本 6。立即生成引导消息 `queen-bootstrap:680a…:6`（system → queen，"新 Hive，请开始首次引导"） |
| 06:04:41 | 对照：同样 send to=`hive`，返回 to=`queen`，消息 `msg-794d4472…` 投递给 Queen |
| 06:04:44 | Queen 回复 hive："收到；路由正常，Hive 状态未变（内置 pstack v4 生效…）"。方法论版本没有变化，也没有派活 |

未认领 GitHub 事件的回退需要在测试仓库发一条评论来产生事件。这次 GitHub 写操作被本机的权限检查拦下，所以没有实测；Router 这条路径有单元测试覆盖。

次要观察：Queen 关闭再打开都会重新发"新 Hive，请开始首次引导"，即使 Hive 已有生效的方法论。这次 Queen 判断正确、没有重装，但文案容易误导。建议已有方法论时改发"Queen 已重新开启"之类的说明。

## 9. 方法论自动回滚

**结论：触发机制（3 次失败、system 激活、通知 Queen）生效；但失败记到了错误的版本，导致"回滚"到了故意破坏的版本。不通过（平台问题，已记录未修）。**

方法：
- 用用户 Token 发布 v5：内容与仓库里的 pstack 相同，只把 `roles/worker.md` 换成"只执行 `sleep 150`"。发布即生效。
- 临时把 Hive 的 `turnTimeoutMinutes` 改为 1。
- 派 3 个 worker（requestId `r5-rollback-1..3`），让它们因轮次超时进入 failed。

| 时间 UTC | 证据（methodology_events / agents / messages） |
|---|---|
| 06:05:53 | event 6：publish v5，actor 为验收用户 Token。system → queen 通知 |
| 06:06:00 | 派出 3 个 worker（`forager-21612e5b…`、`forager-ff384593…`、`forager-8c7381ef…`），它们在 v5 下启动 |
| 06:06:10 | event 7：**Queen 主动激活 v4**。理由：v5 的 worker 提示词必然失败，发布备注也写明 v4 才是正式版本。Queen 向 hive 汇报了原因 |
| 06:07:04 | 3 个 worker 全部 `failed`（轮次超时）。这时生效的是 v4，失败被记在 v4 上：v4 consecutive_failures=3 |
| 06:07:04 | event 8：actor=`system`，"Automatically rolled back methodology version 4 to version 5"。v4 被标记 `auto_rolled_back_at`，**故意破坏的 v5 被重新激活**；system → queen 通知 |
| 06:07:30 | event 9：Queen 发现平台回滚的目标是破坏版本，再次激活 v4，并向 hive 写明经过 |

问题：
- 失败按"Agent 结束时生效的版本"计数（`recordMethodologyOutcome` 用 `WHERE active`），而不是"Agent 启动时使用的版本"。激活新版本后，旧版本下启动的 Agent 失败，会算到新版本头上；回滚目标也可能正是出问题的版本。
- v4 因此带上了 `auto_rolled_back_at`。按"每个版本只自动回滚一次"的规则，v4 以后不会再被自动回滚。

建议：
- Agent 记录启动时的方法论版本，失败只计入该版本。
- 只有该版本仍是生效版本时才回滚，回滚目标排除该版本。
- 补一个"激活新版本后旧版本下的 Agent 失败"的测试。

收尾：06:07 之后把 `turnTimeoutMinutes` 恢复为 120（Hive 版本 8）。最终 v4 生效，digest `6654840d…f37728`；v5 未生效。这一步之后只剩 Queen 和遗留的 `forager-ef8e…`，没有其他存活的 Agent 或 Cell。第 6～9 节合计产生 31 次模型请求，估算 0.0271 美元。

## 10. 结论汇总

| 验收项 | 结论 | 见 |
|---|---|---|
| Queen 引导回复的完整正文与默认页可见 | 通过 | 第 2 节 |
| 网页对话选择 pstack，Queen 安装 | 通过 | 第 3 节 |
| 无标签 Issue → Queen 派 worker → PR → 独立 verifier → 可合并通知 | 通过（测试仓库无 CI，门禁按"无失败检查"放行） | 第 4 节 |
| Forager `send` 省略 `to` 时默认发给 Queen | 通过 | 第 5 节 |
| PR 合并后 owner 结束、Cell 清理、不再发布 | 通过 | 第 6 节 |
| 并发排队、去重、补派 | 通过 | 第 7.2 节 |
| Hive 并发上限优先于方法论 | **不通过** | 第 7.1 节 |
| Queen 关闭时发给 hive 的消息回退 | 通过 | 第 8 节 |
| Queen 关闭时未认领 GitHub 事件回退 | 未验证 | 第 8 节 |
| 方法论自动回滚 | 机制生效；**计数归属不通过** | 第 9 节 |
| 全新 Hive 在选方法论前不安装、不派活 | 未验证 | 第 1 节 |
| verifier FAIL 路径 | 未验证（第四轮遗留） | — |

### 发现的问题

平台问题（属于"swarm 能不能稳定、安全地工作"）：

1. **重要：方法论并发上限覆盖 Hive 上限。** 修复方向：取较小值，store 与 memstore 一起改。
2. **重要：自动回滚把失败记到当前生效版本。** 修复方向见第 9 节。
3. 次要：重开 Queen 时一律发"新 Hive"引导文案。
4. 次要：owner 收尾那一轮没有 turn finished 日志。
5. 次要：Hive config 里还留着 `cli: "opencode"`（第三轮遗留字段，omp 下不再使用）。
6. 次要：Queen 派出的 worker 的 `issue_number=0`，任务来源 Issue 没有关联上（第 4 节）。
7. 遗留：第四轮 parked worker `forager-ef8e…`（PR 3，在 F 修复前已合并）仍占一个并发名额，需要手工取消或做一次迁移清理。

方法论问题：本轮没有发现。pstack 的 worker、verifier 和 Queen 风格在第 4～6 节的链路中表现符合预期。

本记录没有修改 ADR 或规格，没有修复上述问题，等用户决定放进下一轮还是立即修。
