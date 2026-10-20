# SwarmJet 第六轮真实验收

验收日期：2026-10-20。下文时间统一为 UTC。规格为 [round6-prompts.md](round6-prompts.md)，平台边界依据 [ADR 第 2 节](adr-hive-queen-forager.md)。记录只包含可核对的 ID、SHA、接口结果与查询摘要，不包含真实凭证。

## 1. 合并、部署与基线

用户明确批准后，使用 `gh pr merge 68 --merge --delete-branch`（显式指定仓库）合并 [集成 PR #68](https://github.com/yyydddkkk/SwarmJet/pull/68)。GitHub 记录：08:39:59 合并，提交 `37823f4f193f675744e6e6ee6790d3bee3074e21`。任务 PR #63、#64、#65、#66 于 08:40:01 自动显示为 MERGED，没有另行关闭或单独合并。

主检出保持 dev，通过 `git pull --ff-only` 更新；没有修改 web/。本轮六个开发/集成 worktree 已删除，`sj-int-pg` 已停止。主检出的 `design/topology/` 和 `.claude/launch.json` 保留。验收文档另在独立的 `docs/round6-acceptance` worktree 中编写。

先备份 `deploy/.env` 为 `deploy/.env.bak-r6`，权限 0600，再从 deploy 执行：

```sh
python3 install.py upgrade --yes --image-source repo
```

**部署结论：通过。** 升级退出码 0；进程内比较确认原有密钥类变量值没有变化；没有删除数据卷、数据目录或回滚数据库。

| 项目 | 实测结果 |
|---|---|
| 服务 | api、postgres、auth-broker、auth-gateway 均 healthy；tunnel 运行（没有独立 healthcheck）；migrate 退出码 0 |
| 数据库 | `SELECT max(version) FROM schema_version` 返回 15 |
| 控制台与 MCP | `http://localhost:8180`、`http://127.0.0.1:8180/mcp` |
| setup | 08:43:40，`GET /api/setup/status` 返回 200；GitHub configured=true，Cells enabled=true，Docker available=true，模型 available/configured=true |
| 隧道 | `https://record-suzuki-demanding-heated.trycloudflare.com`；webhookSynced=true，lastError 为空 |
| 测试 Hive | `680a4a31a140831b421fe16666bc3dfe`，仅操作 `yyydddkkk/test-for-myself` |
| 初始配置 | Queen 开启，Hive version=8，并发上限 4，turnTimeoutMinutes=120 |
| 初始方法论 | pstack v4，ID `a4bf9b9a15f80c4cf11033c7b5782b0e`，digest `6654840d490d427ec1e8118b2871fd0ddf4e20b6fa932af98127edd9f0f37728` |

管理员登录在进程内读取环境配置，Cookie 仅在内存中使用。按任务指定命令从 api 容器生成 Codex MCP 配置，Bearer Token 也只在内存中解析与使用，没有写入文件；没有读取 DeepSeek key。

## 2. 测试 Issue 与观察范围

创建前分别向用户说明内容，创建了两个无自动合并要求的探针：

- [Issue #9：Unicode 字符计数](https://github.com/yyydddkkk/test-for-myself/issues/9)：新增 `--chars FILE`，要求码点语义、错误处理、测试和 README，并保持原功能兼容。产生 [PR #10](https://github.com/yyydddkkk/test-for-myself/pull/10)。
- [Issue #11：最小 SSE 标记](https://github.com/yyydddkkk/test-for-myself/issues/11)：仅新增 `acceptance-r6.txt`，内容为 `Round 6 SSE acceptance.` 加末尾换行，运行原有测试。产生 [PR #12](https://github.com/yyydddkkk/test-for-myself/pull/12)。

第一次观察进程实际未启动 curl，漏掉了 Issue #9 的初始事件。这是验收操作失误，不能据此判断平台漏事件，也没有把这一段算作通过。随后确认 curl 已收到 ready，再创建 Issue #11，补齐完整链路。没有依赖断线补发，也没有把重新拉快照误当作增量推送。

测试仓库 main 初始 SHA 为 `86490fa5ac2a599e9d606a5d45072e0f6a6f608a`。仓库没有 CI 配置，因此空 checks 列表不记作“测试仓库 CI 通过”；测试另行独立执行。既有 PR #1、#2、#4、#5、#6 没有被合并或关闭。

## 3. SSE 的真实顺序与数据库一致性

**结论：通过。** 实际使用 curl 订阅 `GET /api/events?hive=680a4a31a140831b421fe16666bc3dfe`。Cookie 通过 stdin 的 curl config 传入，未出现在参数、文件或输出中。连接 PID 为 1144960，08:44:59.281535 收到 ready；此后持续连接到验收收尾，收到六类真实事件。

等效 curl 形式如下，变量值只存在内存中：

```sh
printf 'header = "Cookie: swarmjet_session=%s"\n' "$SJ_SESSION" |
  curl --config - --no-buffer \
    'http://localhost:8180/api/events?hive=680a4a31a140831b421fe16666bc3dfe'
unset SJ_SESSION
```

Issue #11 的关键顺序如下。“接收时间”是观察进程的 UTC 时间，不冒充数据库提交时间。

| 接收时间 | 事件与证据 |
|---|---|
| 08:46:42.035339 | message：`gh-1ead35b1b3bd39157c798ef1049d8885`，system → queen，external，正文头为 issues.opened、Number #11。DB seq=192，createdAt=08:46:42.031977 |
| 08:46:51.148709 | agent：worker `forager-edbc0290691ef15a633099cd74aed7b1` 创建；issueNumber=11，state=creating。DB createdAt=08:46:51.143769，启动方法论为 v4 |
| 08:46:51.151367 | message：派活 `msg-b4985d9dcdc91b7c0bb206ce9eaec7e4`，queen → 上述 worker；DB createdAt=08:46:51.148512 |
| 08:46:54.367026 | agent：上述 worker active |
| 08:47:19.098858 | agent：worker 关联 prNumber=12，随后 parked |
| 08:47:23.632091 | pr：#12，head=`a01568ceb6c46bb8ab964ad1899838d2cff3d017`，open、ready=false |
| 08:47:49.650260 | pr：同一 head，ready=true；独立 attest 已于 08:47:46.885437 入库 |
| 08:49:40.790047 | pr：#12，closed、merged=true |
| 08:49:40.790467 / .791080 | message：owner 的完成控制与原始 closed 事件，见第 4 节 |
| 08:49:44.277073 | agent：owner completed；DB updatedAt=08:49:44.268892 |

另收到 queue waiting/started（第 5 节）、Hive 配置变化（version 9～12）、methodology publish v6 / activate v4（第 8 节）。

主代理从观察进程内存取出四条消息事件，与 Postgres 的相同 ID 行自动逐字段比较：id、hiveId、from、to、kind、trust、chainId、createdAt、preview 九个字段全部一致。时间仅做 UTC 表示规范化；preview 与 SQL `left(body,120)` 一致。四条为上述 Issue、派活，以及 `gh-8d818b075b3dceaf526388c2d46b866d-complete` 和 `gh-8d818b075b3dceaf526388c2d46b866d`。

Agent 创建/最终状态、Issue/PR 关联也与数据库一致；PR head、合并状态与 GitHub 一致。SSE 是实体增量通知，不把它当作完整历史审计或保证补发的事件日志。未单独实测生产环境下的断网恢复和慢客户端；相关 race 回归见集成 PR。

## 4. PR 列表、网页合并、head 校验与收尾

**结论：通过。** `GET /api/hives/{id}/prs?state=open` 返回产生的 PR #10 及门禁；`state=all` 在合并后返回 #12，state=closed、merged=true、ownerId 正确，并保留 PASS attest。合并后的 ready=false、reason 为 already merged，符合已合并状态，不是门禁失败。

PR #12 合并前单条状态接口为 ready=true、reasons=[]、checks=[]，head 为 `a01568ceb6c46bb8ab964ad1899838d2cff3d017`。attest `8e16dec0cc7890887d14562f8be9963c`：result=pass、isAuthor=false，验证人 `forager-a040baccf678ebaaf7b656ff`，作者为 owner。主代理检出精确 head，确认仅新增指定文件、内容恰好 24 字节，并在该检出目录运行 `python3 -m unittest -v test_wordcount.py`：8/8 通过，0.101 秒。

用户单独明确批准 PR #12 及这个 head 后，调用：

```text
POST /api/hives/680a4a31a140831b421fe16666bc3dfe/prs/12/merge
{"headSha":"a01568ceb6c46bb8ab964ad1899838d2cff3d017"}
```

08:49:38 返回 HTTP 200：merged=true、queued=false、method=squash、headSha 为上述 SHA、ready=true、reasons=[]。GitHub 记录 mergedAt=08:49:36，合并提交 `9e3b15d7e2b7844ebbeda3dd4c4bd733ba7be8b0`。

随后对同一接口传入 40 个零的错误 head，08:50:21 返回 HTTP 409：`pull request head changed; please review it again`。没有执行第二次合并。

| 收尾证据 | 实测结果 |
|---|---|
| 审计 | `actor_type=web_administrator`，action=`pr.merge.request`，administrator=`admin`，source=web，包含正确/错误 head 两次请求记录 |
| 完成控制 | `gh-8d818b075b3dceaf526388c2d46b866d-complete`，system trust，08:49:38.612568 入库，正文 `complete\nafter_event\nPR #12 closed (merged=true)`，handled |
| 原始事件 | `gh-8d818b075b3dceaf526388c2d46b866d`，external trust，08:49:38.613047 入库，handled |
| owner | `forager-edbc0290691ef15a633099cd74aed7b1`，completed，Issue=11、PR=12 |
| Cell | `docker ps -a` 按 owner Cell 名过滤无结果，已清理 |

没有用 MCP merge_pr 或 gh merge 替代本次网页合并接口验证。PR #10 未包含在用户此次批准范围内，保持未合并。

## 5. Hive 并发上限与排队

**结论：通过。** 08:45:52 将 Hive 并发上限从 4 临时改为 1，Hive version 8→9。数据库同时确认方法论 limits.maxConcurrentAgents=4。

当时 worker 和 verifier 共占用 2 个名额，小于方法论的 4，却高于 Hive 的 1。用 MCP spawn 派无代码改动探针，requestId=`r6-queue-probe-20261020`：

- 08:45:59 返回 queued=true、queuePosition=1，agentId=`forager-dcf0035db752032497bf5bb8fd1010c6`。
- `GET /api/hives/{id}/queue` 返回同一 Agent、role=worker、position=1、正确任务文字，createdAt=08:45:59.957704、expiresAt 为次日同刻。DB 为 waiting，SSE 也收到 waiting。
- 08:46:15 恢复上限 4，Hive version=10。
- 08:46:29 补派成功，任务消息 `msg-391a685c5fb140ba4f5adc70c047cf1a`；SSE 为 creating → message → queue started，Agent 于 08:46:32 active。
- 08:46:41 Agent completed，PRNumber=0；收尾 GET queue 返回空数组，数据库保留该队列行 state=started。

该结果证明方法论的 4 没有放宽用户上限 1。未实测 24 小时队列过期；单链上限也没有另做真实压力测试，二者有集成回归测试。

## 6. 第四轮遗留 parked owner 回收

**结论：通过。** GitHub [PR #3](https://github.com/yyydddkkk/test-for-myself/pull/3) 真实状态 MERGED，mergedAt=02:31:18；head branch 正是 `swarm/680a4a31a140831b421fe16666bc3dfe/forager-ef8e754363c1dbd8fc2f37f0faf71f0a`。

升级启动后，08:41:29.267166 生成恢复的 closed 事件 `gh-f63cf21eec5ea7f11f0580ae4957c3bb`，已 handled；08:41:41.675863，遗留 owner `forager-ef8e754363c1dbd8fc2f37f0faf71f0a` 已为 completed。Cell 不在 docker 列表中。08:43:53 创建本轮 Issue 前，数据库 liveForagers=0，证明名额已释放。没有手工取消该 owner。

其 methodology_version_id 为空，符合历史 Agent 不回填启动版本的迁移策略。

## 7. 三个列表接口及真实花费

**结论：通过。**

| 接口 | 真实结果 |
|---|---|
| queue | 第 5 节出现 waiting 项，补派后空数组；字段与真实队列记录一致 |
| methodology/events?limit=5 | 倒序返回本轮 activate v4、publish v6，也能读到第五轮 actor=system 的自动回滚历史，时间与备注一致 |
| signals?since=2026-10-20T08:40:00Z | 08:58:57 返回 9 条；Issue #11 的 messageId 为 `gh-1ead35b1b3bd39157c798ef1049d8885`，kind=issue、source=github、claimed=true、claimedBy=queen；PR #12 opened/closed 事件也能对应消息，closed claimedBy 为 owner |
| foragers | HTTP 200、truncated=false，新增 Agent 返回真实 issueNumber、costUsd；遗留 owner 为 completed |

主代理将 API 花费与同 Agent 的 `llm_usage GROUP BY agent_id` 查询对照（USD）：

| Agent | API costUsd | 数据库累计/记账行数 |
|---|---:|---:|
| PR #10 owner `forager-44373488473bece6606064bafdb97075` | 0.044565（显示时舍入） | 同值，56 行 |
| PR #12 owner `forager-edbc0290691ef15a633099cd74aed7b1` | 0.004117 | 同值，11 行 |
| 排队探针 `forager-dcf0035db752032497bf5bb8fd1010c6` | 0.001903 | 同值，7 行 |

浮点末尾差异仅为表示精度；没有将花费解释为供应商实际结算。列表“一次聚合、没有 N+1”的证据来自集成 QueryTracer 回归，本次真实请求未另外开启生产 SQL tracing。

## 8. 方法论失败归属的可控复现

**第五轮第 9 节的计数归属场景：通过。** “失败版本仍 active 时实际触发自动回滚并选择目标”的分支本次未验证，不能由此扩大结论。

从现存 v4 内容复制临时 v6，只将 roles/worker.md 换成要求 `sleep 150` 的故障角色；没有修改仓库规格或 v4 内容。将 turnTimeoutMinutes 临时调为 1。先告知 Queen 这是三个专用验收探针，不需修复或重派，正式方法论恢复 v4。

| 时间 UTC | 数据库与工具证据 |
|---|---|
| 08:54:04.669188 | publish v6，ID `adae40911e5b3c071afafd96ccf27d0d`，digest `7d38f3b7297f89a5f18048a2461ddbcc9afc056865a2f7b3737bba6ad2b25762`；SSE 收到 methodology publish |
| 08:54:11.975432～12.020865 | 三探针创建，下面三个 Agent 的 methodology_version_id 均为 v6；随后真实状态 active |
| 08:55:11.517841 | 主代理通过 MCP 激活 v4，在三次终止之前；SSE 收到 methodology activate |
| 08:55:14.989293～15.970375 | 三 Agent 全部 failed；各有真实报告 `turn timed out after 1m0s` |
| 08:55:27 查询 | v6 inactive、consecutive_failures=3；v4 active、consecutive_failures=0；没有新增错误回滚激活事件 |
| 08:55:51 | turnTimeoutMinutes 恢复 120，Hive version=12，并发上限仍为 4 |

三探针及超时报告：

| Agent ID | 报告消息 ID |
|---|---|
| `forager-a6458141ce0a6d362942700c6dc3e692` | `001a15885a127-000045-edcd3e53a1512f9c6e2a57a91b26d8b2` |
| `forager-273855f5338673b295bc07304d1790b1` | `001a15885a4eb-000046-6d14040dc7fc48891a053472842db050` |
| `forager-c44f00255d7df453a295db745d7f4fb8` | `001a15885a4fe-000047-f88909e1fedfdf0f52db11058e578c13` |

三探针 Cell 均已删除，没有 PR；合计 3 条模型用量记录，估算 USD 0.000971。临时 v6 保留为 inactive 历史，不删除方法论版本。v4 digest 保持不变、Queen 保持开启。

## 9. 过程中发现的问题与分类

### 属于平台：遗留数据风险，没有新增确认的阻断缺陷

第五轮错误回滚写入的 v4.auto_rolled_back_at=`2026-10-20T06:07:04.298189Z` 仍保留，迁移没有清理它。这是已知历史残留，不是本轮重新触发的回滚。依据代码“每版本只自动回滚一次”的过滤规则，可推断该标记会阻止 v4 再自动回滚；本次没有主动破坏 active v4 来验证这一分支。

建议用户另行决定是否修复历史标记，或发布内容等价的正式新版本。此次不改历史数据。PR 列表缓存未命中串行、历史 signals 只能复原 Digest 的限制也仍保留，已在集成 PR 记录，未扩范围修改。

### 属于方法论：兼容性 triage 与重复验证者

PR #10 首个 head `1482934` 的独立 verifier 报告了 words/lines 无效 UTF-8 行为变化，但将其作为非阻塞 warning。Queen 随后先要求保留共享错误处理并补测试；worker 的 `0fc55a1` 曾恢复旧行为，又按该 triage 改成 `a2f145c`，把不兼容行为固化进测试。

主代理独立检出 `a2f145c8733f3ca2d32d62351f6d5cdc7dd81d50`：15/15 unittest 通过，但基线对照明确失败——words 和 lines 的无效 UTF-8 从退出码 1+traceback 改成 2+短错误，违反 Issue 的兼容要求。不能因测试全绿或 pass_notes 就算需求通过。

swarm 自行收到 FAIL 后更新为 `1afee1988a0c02b63e01a9c430be400a09c1fe31`。主代理读取了独立复验的真实命令与输出：15/15 unittest、码点/CRLF/空白/错误用例及基线兼容检查全部通过，结束时 GitHub head 未变。当前对应非作者 PASS attest 为 `6d6cec2e203dc9f62e567a2fb8a3e74d`；旧 head 的三个 attest 已 superseded，平台没有把旧 PASS 当作当前 head 的结论。

另外，同一 head 上出现自动 verifier 与 Queen 补派 verifier 重叠：

- `a2f145c`：自动 `forager-0a60d389ff64d87f9b5edb17`；Queen 补派 `forager-802e1b212cf9ba536fb77bc826302748`，后者 cancelled。
- `1afee198`：自动 `forager-50ad57c19fe24af65580ccb6`；Queen 补派 `forager-c54d327d51781850afb20bd5042a35a3`，后者 cancelled。

建议在 pstack triage 中优先尊重 Issue 的兼容范围，并在补派前核对同一 PR/head 已有的 active 或 waiting verifier。这属于工作方式，不增加平台硬编码流程。此次没有修改 pstack 内容或替 swarm 修 PR；只在测试达到目标后要求暂停追加工作，等待用户决定。PR #10 仍开放，未获合并批准。

## 10. 结论与收尾

| 验收项 | 结论 |
|---|---|
| schema 15、健康服务、webhook、setup | 通过 |
| curl SSE、真实 message/agent/pr 顺序、数据库一致性 | 通过（Issue #11 补验；首次观察失误不计通过） |
| PR 列表及门禁、网页合并正确 head、错误 head 409 | 通过 |
| PR #12 owner completed、Cell 清理 | 通过 |
| 遗留 parked Agent 回收与名额释放 | 通过 |
| Hive 1 不被方法论 4 覆盖、排队、补派、恢复配置 | 通过 |
| queue、methodology/events、signals、costUsd 真实数据 | 通过 |
| 旧启动版本失败不污染当前版本、不错误回滚 | 通过 |
| active 故障版本自动回滚的实际目标选择 | 未验证 |
| 生产慢客户端/断网、队列 24 小时过期、单链压力 | 未验证，相关集成测试通过不替代真实验收 |

08:58 查询：自升级后 08:41 起，测试 Hive 共 256 条模型用量记录，估算 USD 0.274230，包含遗留 owner 收尾、两条 Issue 链路、排队与超时探针。不是供应商账单金额。

09:03 再次核对：setup 返回 200，webhookSynced=true；四个有健康检查的服务均 healthy，tunnel 运行；Queen enabled=true，pstack v4 digest 不变、失败数 0，Hive version=12、并发 4、轮次超时 120，waiting 队列为 0。只剩 PR #10 的 owner parked，等待用户决定，其他本轮探针与遗留 owner 的 Cell 均不存在。`.env` 与备份仍为 0600，主检出的两个受保护未跟踪项仍存在。

09:03:24 结束 curl 订阅，累计记录 224 个 event 帧（含 ready，不含心跳注释）；本次创建的临时 MCP Token 已按精确 ID 撤销（HTTP 204），本次管理员会话已登出（HTTP 204）。没有撤销其他 Token。

本记录 PR 只提交此文件。新问题及历史标记的建议先记录，等待用户决定；不额外修改平台代码、方法论、仓库设置或合并 PR #10。
