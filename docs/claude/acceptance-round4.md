# SwarmJet 第四轮补充验收

验收日期：2026-10-19（America/Los_Angeles）；下文时间统一使用 UTC（2026-10-20）。从 `origin/dev` 的 `385a688832945f736e8b20717c246d7a193094e0` 建立 `docs/round4-acceptance`。测试对象为已有 `sj-r3` 部署、Hive `680a4a31a140831b421fe16666bc3dfe`、仓库 `yyydddkkk/test-for-myself`、模型 `deepseek/deepseek-v4-flash`。

已阅读 ADR 第 2、10 节、round4-prompts 公共契约、pstack manifest/roles/skills 和 methodologies README。全程未修改平台代码、部署配置或凭证，未重启服务，未创建 Issue；测试任务、消息、取消与方法论临时版本操作详列下文。MCP 工具未被当前客户端暴露，因此按给定 `swarmjet connect --client codex` 命令创建连接，连接信息仅保存在交互 Python 进程内存，经 HTTP MCP 调用工具。管理员密码也仅在内存中用于登录。本文不包含凭证。

证据来源：MCP 原始响应、Postgres SELECT、api JSON 日志、GitHub `gh pr view`、Docker 容器与 volume 列表。attest 摘要中的测试数量是 verifier 的记录，不单独作为平台行为通过的依据；状态、消息、SHA、作废、合并和清理均另行核对。本文花费为平台价格表估算值，非提供商账单。

## 1. FAIL 路径

**结论：FAIL → 修复 → 新 verifier → PASS 未验证。** 按要求最多尝试三次，首次结论依次为 PASS_NOTES、PASS、PASS_NOTES，数据库没有本轮 FAIL attest。已验证 PASS_NOTES 的反馈、修复、旧结论作废和新 verifier 路径，不能据此声称 FAIL 路径通过。

### 三次尝试与操作干预

1. [PR #4：RFC 4180 CSV](https://github.com/yyydddkkk/test-for-myself/pull/4)。要求手写无依赖解析器，只给空输入、普通逗号分隔和普通两行的种子示例，让独立 verifier 覆盖引号内逗号、换行等规格。实际首次结果为 `pass_notes`，不能算 FAIL。worker 修复提示信息缺少 offset 的 nit，产生新 head；新 verifier 给出 `pass`。
2. [PR #5：RFC 8785 canonical JSON](https://github.com/yyydddkkk/test-for-myself/pull/5)。只给普通 key 排序、嵌套 map 和空白示例，并提示可从 `json.dumps` 起步，完整契约仍是 RFC 8785。首次结果为 `pass`。
3. [PR #6：IDNA2008](https://github.com/yyydddkkk/test-for-myself/pull/6)。只给 ASCII、bücher 和空 label 示例，并提示可从内置 idna codec 起步，完整契约仍要求 IDNA2008 验证与转换。

PR #4 首次发布与 PR #3 squash 合并交叠，02:32:00.702 出现 `cell: base is not an ancestor of swarm/680a4a31a140831b421fe16666bc3dfe/forager-cb47763f5d8cbcd69a011ba44e8ed108`。02:32:11 通过 MCP send 请 worker fetch/rebase，未改原任务，02:32:30.548 发布 PR #4 成功。

PR #6 初始 head `71ac50ca1a8afae70a25e6d72031cce50e2c177a` 的事件在 02:43:16.059 被路由为 `action=inbox`，原因为 `cannot start Forager: hive concurrent forager limit reached`。当时 PR #3、#4、#5、#6 的四个 worker 都 parked。02:44:07 通过 MCP cancel_forager 取消本轮前两次已验证的 worker（#4、#5），数据库确认 cancelled；02:44:16 请 #6 worker 只创建空提交，产生 `86cfd214999e521ebbc4ff3f0bd703b7ec4eea2c`，由新 `pr_head` 事件自动派出 verifier。此恢复没有改变 IDNA 实现或测试，未手工写入任何 verdict。容量释放后到空提交恢复之间未观察到原始事件补派，观察窗口约 9 秒；此限制另列于最后一节。

### Agent、结论和消息

最终状态快照：2026-10-20 02:57:32 UTC。worker 的取消均为本轮主动收尾，不是平台自动完成。

| Agent id | 角色 / PR | 已观察状态变化 | 最终状态时间 UTC |
|---|---|---|---|
| `forager-cb47763f5d8cbcd69a011ba44e8ed108` | worker / 4 | creating → active ↔ parked → cancelled（人工） | 02:44:07.757 |
| `forager-bb387c012fa9d43025a260a9` | verifier / 4 | active → completed（未采样瞬时中间态） | 02:33:42.117 |
| `forager-fb217e3869a75a91ebb9062a` | verifier / 4 | active → completed（未采样瞬时中间态） | 02:35:07.529 |
| `forager-0fc5db78f7d20abbc2054c771ea8acde` | worker / 5 | creating → active ↔ parked → cancelled（人工） | 02:44:08.198 |
| `forager-de861a69b7d96677b0b53717` | verifier / 5 | active → completed（未采样瞬时中间态） | 02:37:47.596 |
| `forager-3ef4935549e1cea6eb78e42709f5318c` | worker / 6 | creating → active ↔ parked → cancelled（人工） | 02:56:51.035 |
| `forager-1b18aac279bd2dd47636d981` | verifier / 6 | active → completed（未采样瞬时中间态） | 02:48:44.904 |
| `forager-f9920c8712d2cdf9747e4bb0` | verifier / 6 | active → completed（未采样瞬时中间态） | 02:56:37.129 |
| `forager-aca0b791becca0373113b23463ed2a9f` | worker / 无 | creating → active → cancelled（启动失误探针） | 02:57:08.894 |
| `forager-a9bdc551f9b39447e0a4374d32cdfc6e` | worker / 无 | creating → active → parked（402）→ cancelled | 02:57:21.258 |

| PR | SHA | attester id | result | is_author | superseded_at（UTC） |
|---|---|---|---|---|---|
| 4 | `954fb5210c457827ddf329d0e730fe1ac2deebc9` | `forager-bb387c012fa9d43025a260a9` | [pass_notes](https://github.com/yyydddkkk/test-for-myself/pull/4#issuecomment-6281985105) | false | 2026-10-20T02:33:59.28468+00:00 |
| 4 | `2ac64bf2817ff33dfabb89fa96fffb165143234f` | `forager-fb217e3869a75a91ebb9062a` | [pass](https://github.com/yyydddkkk/test-for-myself/pull/4#issuecomment-6282003863) | false | 未作废 |
| 5 | `887f52da6fc5ac4232a9856b930df148c44dc011` | `forager-de861a69b7d96677b0b53717` | [pass](https://github.com/yyydddkkk/test-for-myself/pull/5#issuecomment-6282036443) | false | 未作废 |
| 6 | `86cfd214999e521ebbc4ff3f0bd703b7ec4eea2c` | `forager-1b18aac279bd2dd47636d981` | [pass_notes](https://github.com/yyydddkkk/test-for-myself/pull/6#issuecomment-6282167110) | false | 2026-10-20T02:49:49.203313+00:00 |
| 6 | `eb7febdaa917beede496c4a8d500e764d1aabc4f` | `forager-f9920c8712d2cdf9747e4bb0` | [pass_notes](https://github.com/yyydddkkk/test-for-myself/pull/6#issuecomment-6282262891) | false | 未作废 |

作者均由平台记录：#4 为 cb47763…、#5 为 0fc5db…、#6 为 3ef493…（完整 id 见上表）；所有结论均非作者提供。#6 修复版最终仍为 PASS_NOTES，新增 U+1171E joining-context 的 warning，未得到 PASS。02:56:50 取消 #6 worker，停止继续返工；该新 warning 的修复与后续 head **未验证**。

CSV verifier `forager-bb387c012fa9d43025a260a9` 给 worker 的数据库消息（seq=49，kind=report、trust=agent）关键原文：

> Verifier report for PR #4 @ 954fb5210c457827ddf329d0e730fe1ac2deebc9: PASS with notes ... One non-blocking nit: csv_records.py:137 — raise ValueError("unterminated quoted field") omits the character offset ... Evidence: parse_csv('"a') -> "unterminated quoted field" (no offset) ... Suggest including the EOF offset.

GitHub head 从 `954fb5210c457827ddf329d0e730fe1ac2deebc9` 更新为 `2ac64bf2817ff33dfabb89fa96fffb165143234f`。旧 attest 在 02:33:59.284 作废，新 verifier `forager-fb217e3869a75a91ebb9062a` 于 02:34:57.275 写入 PASS。这是 PASS_NOTES → 修复 → 新 verifier → PASS 的真实链路。

第三次 verifier `forager-1b18aac279bd2dd47636d981` 的反馈（seq=77，report/agent）包含：

> Verifier verdict on PR #6 head 86cfd214999e521ebbc4ff3f0bd703b7ec4eea2c: PASS_NOTES (2 findings).
> Finding 1 (warning) — idna_domain.py:290-291, _check_bidi ... Bidi is checked only on labels containing R/AL/AN ... mixed domains that violate rules 1/6 are accepted.
> Evidence: to_ascii('0à.א'), to_ascii('àˇ.א') -> xn--0-sfa.xn--4db xn--0ca88g.xn--4db.
> Finding 2 (nit) — Docs say A-labels are returned in lowercase canonical form, but uppercase A-labels raise ValueError.

验收方通过 GitHub contents API 读取精确 SHA 的 `idna_domain.py`，独立执行上述两个输入，确实得到同样两个字符串；这是对复现输出的独立确认。verifier 的标准解释和 fuzz 数量保留为其发现摘要。该旧 head 的 MCP pr_status 返回 `ready=true, reasons=[]`，未调用 merge_pr 合并它。worker 后续推送修复 head `eb7febdaa917beede496c4a8d500e764d1aabc4f`，自动产生新 verifier `forager-f9920c8712d2cdf9747e4bb0`。

以下五个 verifier 均查到自发 `kind=control, trust=system, body` 首行为 `complete` 且消息 `state=handled`；该控制消息与 finish_task 实现（`internal/mcp/tools.go:1271`）对应。

| verifier id | complete 消息 seq | complete 时间 UTC | completed 时间 UTC |
|---|---|---|---|
| `forager-bb387c012fa9d43025a260a9` | 50 | 02:33:40.032 | 02:33:42.117 |
| `forager-fb217e3869a75a91ebb9062a` | 57 | 02:35:02.660 | 02:35:07.529 |
| `forager-de861a69b7d96677b0b53717` | 63 | 02:37:45.814 | 02:37:47.596 |
| `forager-1b18aac279bd2dd47636d981` | 78 | 02:48:41.976 | 02:48:44.904 |
| `forager-f9920c8712d2cdf9747e4bb0` | 85 | 02:56:33.235 | 02:56:37.129 |

完成状态之外，另用 `docker ps -a`、`docker volume ls` 和宿主机目录存在性检查确认：上述五个 verifier 的容器、CLI volume、`/home/yang/swarmjet-r3-data/cells/<agent-id>` 均不存在。本轮取消的五个 worker 也均已删除同样资源。旧 #3 worker 仍 parked，`swarmjet-cell-forager-ef8e754363c1dbd8fc2f37f0faf71f0a` 仍存在。没有把 cancelled 计成 completed。

## 2. 合并入口 merge_pr

**结论：合并入口正例、无当前 head 结论的反例通过。生命周期观察：合并后 worker 未转为 completed。** 当前公共契约没有承诺合并事件自动完成 worker，因此将该现象作为本次要求观察到的生命周期缺口，单独列出，不改判合并入口。

02:30 后调用 `pr_status(hiveId, pr=3)`：

```json
{"headSha":"500062547083a10f500a7d5492cf1d02915801b6","state":"open","draft":false,"checks":[],"ready":true,"reasons":[]}
```

调用 merge_pr 时有效结论来自非作者 `forager-2810d42495f8249cb8beccd1`（pass、isAuthor=false、superseded=false）；作者为 `forager-ef8e754363c1dbd8fc2f37f0faf71f0a`。历史非作者 PASS：`a030827f8882199177aa89e60d00fcf2983190fe`（verifier `forager-664ed31ad79006b6a2ce68fb`）和 `66d86e8f4bbc7f19a53fe49ad31e83efe52647a8`（verifier `forager-ba11c2210734a43c2d9257e2`）均已作废。

接着调用 `merge_pr(pr=3)`，响应：

```json
{"headSha":"500062547083a10f500a7d5492cf1d02915801b6","ready":true,"merged":true,"queued":false,"method":"squash","message":"已合并"}
```

用 `gh pr view 3 --repo yyydddkkk/test-for-myself --json state,mergedAt,mergeCommit` 读回：

```json
{"state":"MERGED","mergedAt":"2026-10-20T02:31:18Z","mergeCommit":{"oid":"4fd05d2b16845c67475629ce2ea0d162551d20aa"}}
```

[PR #3](https://github.com/yyydddkkk/test-for-myself/pull/3) 确实合并。平台锁定的原 head 和 squash 后的 merge commit 不同，符合 squash 方式。静态核对 `internal/merge/gate.go:193` 将重新检查的 HeadSHA 传入 RequestMerge；`internal/github/checks.go:208` 再核对 head，`:267` 的直接合并请求携带 sha。此处没有抓取出站 HTTP，也没有制造检查与合并之间的并发改 SHA，故 SHA 竞态拒绝场景**未验证**；真实成功调用与读回、代码中的 SHA 参数得到交叉确认。

反例使用 PR #4 初始 head `954fb5210c457827ddf329d0e730fe1ac2deebc9`，尚无 attest 时：

```json
{"attestations":[],"checks":[],"ready":false,"reasons":["no attestation for the current head"]}
```

立即调用 `merge_pr(pr=4)` 返回 `merged=false, queued=false, ready=false, message="未合并：no attestation for the current head"`。其后 GitHub 仍为 OPEN；未绕过 gate。测试仓库没有 CI checks，因此本轮没有覆盖失败 CI、required checks 或冲突拒绝。

合并后的实际日志：02:31:21.235 `pull_request.closed` 投递给 #3 worker；02:31:22.248 开始一轮；02:31:28.584 `publish failed: cell: base is not an ancestor ...`，同轮 `turn finished` 的 `pr=0`、`publish_error` 非空。数据库随后仍为 parked，Cell 仍存在，无 finish_task complete 控制消息。没有人为完成这个旧 worker，以保留原始现象。在 02:57:32 UTC 最终快照中仍为 parked（本轮窗口内未查到其 complete 控制消息）。

## 3. 用量记账与花费上限

**用量结论：通过。** 使用 deploy/.env 中的管理员用户名/密码在内存里 POST `/api/login`，带 `X-Requested-With: swarmjet` 和 `Origin: http://localhost:8180`，返回 HTTP 200；用 session cookie GET `/api/usage?period=today`。数据库 TimeZone 为 UTC，today 与每日预算均按 `date_trunc('day', now())`，本次不是本地洛杉矶日界线。

最终安静窗口中，将 SQL 分组和接口 byAgent 对全部 14 个有用量的 Agent 逐项比较：requests、input/output/cache-read/cache-write tokens、unknown-price 数相等，cost 以 1e-12 容差比较相等，总计也相等。无用量的已取消旧 Agent 不会出现在聚合中。

接口/SQL 总计：390 次请求，input=406636、output=323805、cacheRead=20428800、cacheWrite=0，估算 costUsd=0.433592，unknownPriceRequests=0；byModel 仅 deepseek/deepseek-v4-flash。

| Agent id | requests | input | output | cache read | cost USD | unknown price |
|---|---:|---:|---:|---:|---:|---:|
| `forager-0fc5db78f7d20abbc2054c771ea8acde` | 21 | 24374 | 28260 | 713472 | 0.021313 | 0 |
| `forager-1b18aac279bd2dd47636d981` | 51 | 55624 | 44600 | 2951680 | 0.061599 | 0 |
| `forager-2810d42495f8249cb8beccd1` | 28 | 50789 | 15030 | 1091712 | 0.026603 | 0 |
| `forager-3ef4935549e1cea6eb78e42709f5318c` | 74 | 77521 | 73367 | 6531840 | 0.122845 | 0 |
| `forager-664ed31ad79006b6a2ce68fb` | 18 | 13814 | 10166 | 323840 | 0.009314 | 0 |
| `forager-a9bdc551f9b39447e0a4374d32cdfc6e` | 1 | 659 | 75 | 9472 | 0.000246 | 0 |
| `forager-aca0b791becca0373113b23463ed2a9f` | 4 | 1277 | 642 | 40192 | 0.000921 | 0 |
| `forager-ba11c2210734a43c2d9257e2` | 14 | 13884 | 9265 | 251264 | 0.008055 | 0 |
| `forager-bb387c012fa9d43025a260a9` | 19 | 15779 | 11761 | 408448 | 0.011220 | 0 |
| `forager-cb47763f5d8cbcd69a011ba44e8ed108` | 24 | 14622 | 17695 | 593536 | 0.015310 | 0 |
| `forager-de861a69b7d96677b0b53717` | 20 | 18386 | 15715 | 535936 | 0.014476 | 0 |
| `forager-ef8e754363c1dbd8fc2f37f0faf71f0a` | 25 | 36168 | 10197 | 434560 | 0.014001 | 0 |
| `forager-f9920c8712d2cdf9747e4bb0` | 77 | 69598 | 74910 | 6244608 | 0.118138 | 0 |
| `forager-fb217e3869a75a91ebb9062a` | 14 | 14141 | 12122 | 298240 | 0.009551 | 0 |

pstack v2（与仓库 methodology.yaml 一致）limits 为 maxConcurrentAgents=4、maxCostPerAgentUsd=2、maxCostPerDayUsd=10。除下面的小额预算探针外，各 Agent 均低于 2 美元，Hive 当日 0.433592 美元低于 10 美元。本轮未触发每日上限；不能以此声称每日上限已实测。未知价格模型的真实调用也未测试，仅确认现有记录无 unknown_price。

核心复核查询（只读）：

```sql
SELECT agent_id, count(*), sum(input_tokens), sum(output_tokens),
       sum(cache_read_tokens), sum(cache_write_tokens), sum(cost_usd),
       count(*) FILTER (WHERE unknown_price)
FROM llm_usage
WHERE hive_id = '680a4a31a140831b421fe16666bc3dfe'
  AND created_at >= date_trunc('day', now())
GROUP BY agent_id ORDER BY agent_id;
```

### 小额上限实验与恢复

**结论：超限后下一次模型请求被拒绝通过；派活人收到 system 通知通过；小额数值的可读通知不通过。**

实验前发生一次本地交互 Python 多行语法错误：publish 调用未执行，误先启动了 `forager-aca0b791becca0373113b23463ed2a9f`（仍在 v2）。立即取消，数据库确认 cancelled，4 条 usage 共 0.000921 美元。这不是小额预算通过证据，已计入总花费，不隐藏重试。

02:57:08.8 发布 pstack v3，仅将原 v2 的 maxCostPerAgentUsd 改为 0.0001，其余 manifest/files 保留。v3 digest=`4dba5341f56229c73b9b49ba064302abdee88728385c0db74f4e939201e6d98e`。先确认发布成功，再 spawn 正式 worker `forager-a9bdc551f9b39447e0a4374d32cdfc6e`，任务为按顺序读 README、查询 Python 版本、统计 tracked files，不改文件、不提交。

首条 llm_usage：02:57:13.479，input=659、output=75、cacheRead=9472、costUsd=0.000246、unknown_price=false。下一次模型请求被拦，最终账本只有这一条，未完成原定多个步骤、未创建 PR。平台投递的 report（messages seq=96）保存的错误为：

```text
auth-gateway 402: {"agentId":"forager-a9bdc551f9b39447e0a4374d32cdfc6e","currentUsd":0.000246,"error":"budget exceeded","limit":"maxCostPerAgentUsd","limitUsd":0.0001,"message":"maxCostPerAgentUsd exceeded: limit 0.00 USD, current spend 0.00 USD"}
```

独立的 system/event 消息（seq=95，from=system，trust=system）准确投递给本次 spawn 的派活地址 `local:d484a0cb9ce7f6b94e1494f459b2c29a`，内容：

> Model budget limit maxCostPerAgentUsd reached for agent forager-a9bdc551f9b39447e0a4374d32cdfc6e: limit 0.00 USD, current spend 0.00 USD. Its model requests are paused until the budget is raised.

这与数据库的真实花费、结构化 402 中的限额及当前花费相互印证。没有只依据 Agent 自述宣称预算生效。通知给对了派活人、上限名称明确，但两位小数格式丢失小额值，不能清楚告知实际限额/花费。

api 日志：02:57:12.552 turn started；02:57:15.596 turn finished，input_tokens=659、output_tokens=75、pr=0。数据库观察 creating → active → parked；02:57:20 激活 v2（MCP 返回 version=2、active=true），随后取消正式探针，02:57:21.258 cancelled。没有等待它在恢复预算后继续跑。

恢复后再次只读查询，v2 active=true，v1/v3 active=false；v2 digest 仍为 `9d4654b00195bbf82ad5e0c1dfd001e44998fa81b659a9308ba5ff2b78db972e`，limits 恢复 2/10 美元、并发 4，与实验前完全一致。方法论 publish/activate 均有 system 消息（seq=92/97）。

边界：本实现请求前看累计花费，请求完成后记账，所以首个 0.000246 美元请求可以越过 0.0001 上限，下一请求才拒绝；本轮验证的是这种“已超限后停止继续请求”的语义，并非严格保证总额不跨线。每日上限、并发请求下预算预留、未知价格请求的上限行为均未验证。

## 4. 发现的问题与建议

1. **合并后 worker 未完成，且尝试再次发布失败。** 现象与复现：用 merge_pr squash 合并已 ready 的 #3，closed webhook 唤醒旧 worker，随后 ancestry 错误，仍 parked、Cell 未释放。原因判断：`internal/router/router.go:318` 的 bot 过滤不包括 closed；`internal/forager/actor.go:259` 对可发布角色在 turn 结束时执行 Publish，错误后仍 parked（`:275`）；只有 finish_task 对应 complete 才终止。建议在 pstack worker 提示词中明确已合并后的完成动作，并让平台发布机制能安全处理已关闭/合并的分支，保留发布失败的明确可操作状态；不要在 Go 中写死角色工作流。本轮未修复。
2. **主分支推进阻塞其他 worker 发布。** 现象与复现：在 CSV worker 执行期间 squash 合并 #3，CSV worker 随后发布失败，手动 send 要求 rebase 后才成功。原因判断：Publisher 每次 fetch 最新 base（`internal/cell/docker/publisher.go:366`），`:433` 强制最新 base 必须是 worker 分支祖先；基础分支一旦前进就可能失败。建议把这一拒绝以结构化、可恢复的事件送给 owner，方法论定义 rebase/merge 后重试；不要让任务以“完成但 PR 未发布”的 parked 状态静默停留。本轮仅在测试仓库做运行恢复，未改平台。
3. **并发满额阻止 verifier 派生，本轮通过新事件恢复。** 现象与复现：maxConcurrentAgents=4，四个 parked worker 占满名额，#6 opened 事件进入 Hive Inbox，日志明确 concurrent limit reached；释放名额后仍需空提交触发新事件。原因判断：当前限制包含 parked worker，事件创建失败走 Inbox fallback；本轮未完整排除更长期的延迟重试。建议平台提供通用的事件等待/重试机制和容量阻塞可观测性，方法论合理安排任务生命周期与所需容量；不要在平台硬编码 verifier 专用豁免。没有验证长期释放名额后是否存在其他延迟重试机制，本轮记录的是本次事件没有形成 verifier、使用空提交恢复的事实。
4. **verifier 报告规格问题但给出 PASS_NOTES。** 现象与复现：#6 旧 head 接受反馈中列出的两个 Bidi 问题输入，非作者 attest 却为 pass_notes，gate 因允许 pass_notes 而 ready=true。验收方独立复现了输出，规格违规判断来自 verifier。原因推测：pstack 说明“能证明真实问题则 fail”，可能仍需要补充“任务明确要求的标准偏差应阻塞”的判定示例；verifier 以参考库也有同样缺口作为非阻塞理由。建议在方法论中补足 required behavior 与非阻塞 nit 的区分，保留独立验证并加入对应规格反例；平台应忠实执行 verdict/merge policy，不替 verifier 改判。本轮不把该 PASS_NOTES 当作 FAIL，也没有合并 #6。

5. **小额预算通知被格式化为 0.00。** 现象与复现：发布 0.0001 美元上限，首条用量 0.000246 美元后触发拦截，system 消息和 402 的 message 均显示 limit/current=0.00，而结构化 limitUsd/currentUsd 正确。原因判断：`internal/llmproxy/proxy.go:308` 使用 `%.2f` 格式化，错误 message 也用两位小数。建议按金额自适应保留足够小数，或直接附结构化实际数值，并加覆盖微额预算的格式测试。本轮未修复。

验收未覆盖：真实 FAIL 链路（尝试三次未产生 FAIL）、#6 最后新 warning 的修复、SHA 竞态拒绝、失败 CI/required checks/冲突拒绝、每日花费上限及并发预算边界。上述项目不得据本报告宣称通过。
