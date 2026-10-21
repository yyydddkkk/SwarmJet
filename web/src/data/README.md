# 星图共享数据契约（第八轮任务 B）

C/D/E 只能从 `src/data/index.ts` 读取数据或发操作。API JSON 依据 `internal/api/{events,prs,web_lists,hives,methodology,models,setup,queen}.go` 和 `internal/domain`；没有发明历史花费或 webhook 操作。底层 HTTP 客户端在本目录 `http.ts`。

## React、画布与面板

`DataProvider({source?,children})` 默认使用 `browserDataSource`；`useDataSource()`、`useDataAPI()` 提供可替换的数据源与全部 REST 操作。

shell 在 `StarsApp.tsx` 为每个项目创建且持有一个 `ProjectStore`，所有组件共享 `StarsSlotProps.stores` 中的同一实例。React 组件调用 `useProjectState(store)`；画布使用 `store.getSnapshot()` / `store.subscribe(draw)`；不要在每个面板重新创建 store 或 SSE。`useProjectStore(id)` 仅用于独立挂载、自己管理生命周期的组件，不能代替 shell 已共享的实例。

`ProjectStore` / `createProjectStore(id, source?)`：`start()`、`stop()`、`getSnapshot()`、`subscribe(listener)`、`refresh()`、`mergePR(number, observedHeadSha)`、`loadAgentMessages(agentId)`、`acknowledgePulses()`。`getSnapshot()` 返回 `ProjectState`：`snapshot`、`derived`、`resources`、`connection`、`error`、`errorStatus`、`pulses`、`revision`、`retryDelay`。`resources` 的每个 key（`hive/agents/queen/prs/queue/signals/methodology/methodologyEvents/messages`）都有 `status: loading|ready|error` 和可选 `error`；`resources.prs` 另有可选 `stale/unavailable`。后端没有新字段也可工作。PR 区域可在 `resources.prs.status === "loading"` 时显示“正在向 GitHub 读取 PR…”。

- 浏览器只开一条不带 hive 参数的 `/api/events` 连接，按 hiveId（hive 事件按 id）分发，避免HTTP/1.1六连接上限。queue/methodology JSON没有hiveId，不能猜所属项目，收到时让所有项目重新拉快照；mock仍可精确推给指定项目。
- 首次 ready 不重复拉快照，断线重连的 ready 才重拉。各资源独立请求，最慢 PR 不阻塞项目、Agent、简报；拉取期间缓存增量，快照完成后按收到顺序重放，PR SSE 不会被晚到的列表响应覆盖。
- message 依据 ID 去重；只有实际事件产生脉冲（同一事件 ID 一次，快照不产生脉冲）。`pulses` 至多保留最新100条，画布消费后 acknowledge。
- agent 按 id、pr 按 number 浅合并。PR 事件还会回拉 canonical 列表，补齐 title/attestations/ownerId，保留当前快照周期内已观察到的SSE head/state/merged/ready，防30秒后端缓存回退；后续快照会对已观察的PR使用无缓存 getPRStatus 校准再接续增量，因此断线期间真实的新head也会生效；signals 在 external message 或 agent 事件后回拉。
- 重连 1、2、4、8、16、30 秒，之后最大30秒；ready 快照成功才重置。显示 `连接已断开，正在重连`。
- `snapshot.messages` 是 `LiveMessage = Message | MessagePreview`。SSE 没有完整 body/state/hop，使用 `messagePreview()` 显示简短内容；初始消息只读主 Agent、hive、当前管理员和活跃 Agent（creating/active/parked/hibernated）。已结束的 Agent 打开详情时调用 `store.loadAgentMessages(id)`；该方法分页、去重、缓存，保留请求期间新到的 SSE 消息。
- `derived`：`nodes`（旧画布动画需要的完整终态节点）、`mapNodes`（新星图当前可见节点）、`history`、`prGroups`、`issueGroups`、`decisions`、`needsAttention`、`agentStatuses`。新星图读 `mapNodes`，并用 `prGroups/issueGroups` 渲染小簇；不要从旧 `nodes` 自行筛选。verifier 不单独生成历史星；历史结束时间用 Agent `updatedAt` **近似**。历史坐标使用设计稿 `histPos` 的同一哈希和参数。

### 文本与可见性纯函数

从 `src/data/index.ts` 直接导入：`weightedTruncate(text,width)`、`firstSentence(task)`、`oneLineSummary(agent,{prs?,issues?},width?)`、`agentSummary(agent,links?,width?)`、`prSummary(pr,width?)`、`nodeSummary(kind,item,links?)`、`shouldCollapseLongText(text,lines=4)`、`uniqueLongTexts(texts)`、`shortSha(sha)`、`shortAgentId(id)`、`relativeTime(iso,now?,locale?)`、`localizedReason(raw)`。未知英文原因返回 `{text:"其他原因",detail:raw}`，界面应在可展开详情中展示 `detail`。

星图规则：`agentVisibility(agent,now?,acknowledged?)`、`prVisible(pr)`、`issueVisible(issue,agents,now?)`、`failedCIVisible(status)`、`historyPosition(id,center?)`、`historyStars(agents,prs,now?,acknowledged?,center?,issues?)`、`aggregatePRs(prs,agents,now?)`、`aggregateIssues(issues,agents,now?)`、`labelPriority(kind)`，常量 `SUMMARY_WIDTH`、`HISTORY_POSITION`、`LABEL_PRIORITY`。`historyStars` 每颗带 `result/endedAt/ageHours/fresh/brightness/dust/x/y`；500 颗后为星尘。聚合返回 `{visible,cluster,clusterPreview,clusterLabel}`，小簇预览最多 6 项。失败 Agent 的“知道了”用 `readAcknowledged(hiveId,agentId)`、`writeAcknowledged(hiveId,agentId)` 本地读写，两者已处理禁用存储的异常。

## 对外类型

后端实体：`Hive`、`HiveConfig`、`Agent`（含 optional costUsd/issueNumber；详情接口并不返回花费）、`QueenStatus`、`Message`、`MessagePreview`、`LiveMessage`、`PullRequest`、`QueueItem`、`Signal`、`MethodologyEvent`、`MethodologyVersion` / `MethodologyResponse`、`MethodologyVersionsResponse`、`Usage` / `UsageResponse`、`SetupStatus`、`ModelsState` / `ModelsResponse`、`Token` / `TokenSummary`、`CreatedToken`、`Principal`，及这些实体的字段类型。

共享状态/派生：`ProjectSnapshot`、`ProjectEvent`、`EventPayloads`、`DataAPI`、`DataSource`、`ProjectState`、`ConnectionState`、`EventConnection`、`StarNode`、`PulseEndpoint`、`LightPulse`。其中 PullRequest 的 canonical 字段在 SSE 新PR到来、回拉完成前可能缺省，界面不可假定 title/ownerId/attestations 已存在。

## REST 全清单

`api`（`useDataAPI()` 返回相同接口）：

- 会话：`getSession`、`login`、`logout`。
- 接入：`getSetupStatus`、`createGithubManifest`、`listGithubRepositories`。
- MCP：`listTokens`、`createToken`、`revokeToken`。只将 createToken 的 plaintext 保留在一次性表单内存。
- 模型：`listModels`、`setModelCredential`、`deleteModelCredential`。
- 项目：`listHives`、`getHive`、`getQueen`、`updateHive`、`createHive`。
- 方法论：`listMethodologyTemplates`、`installMethodology`、`getMethodology`、`listMethodologyVersions`、`listMethodologyEvents`、`activateMethodology`、`rollbackMethodology`。回滚走 activate，选择已有版本；没有另造回滚端点。
- Agent：`listForagers`、`getForager`、`cancelForager`；保留旧页面 `spawnForager`，但星图派活必须使用下方 Queen 路由。
- PR/列表：`getPRStatus`、`listPRs`（默认 all）、`listQueue`、`listSignals`、`mergePR`。界面合并必须使用共享 store.mergePR，而非直接 api.mergePR，确保409刷新与中文提示。
- 对话：`listMessages`（to/after/limit）、`sendMessage`、`talkToQueen`、`interject`、`assignIssue`。assignIssue 给 queen 发送 `请处理 Issue #n`，绝不直接 spawn。
- 用量：`getUsage`（period 仅 `today` / `all`）。API 无7天、昨晚、本周的日期明细，也无最近投递/手动同步，界面应明确不可用。

写操作沿用同源 session cookie + `X-Requested-With: swarmjet`，凭证仅在请求体，不进入 URL/localStorage/日志。错误类型 `APIError` 带 `status`；`fetchProjectSnapshot(id, signal?)` 可单独使用。

## 可注入 mock

`createMockDataSource({[hiveId]: snapshot}, partialApiOverrides?)` 返回 `source`、`calls`、`snapshots`、`setSnapshot(id,next)`、`ready(id)`、`push(id,event)`、`disconnect(id)`、`connections`。创建 store 时传 `mock.source`，或 `<StarsApp source={mock.source}/>` / `<DataProvider source={mock.source}>`。未配置的操作明确报错，不能悄悄调用真实API；默认 getMethodology/listPRs/listSignals/getPRStatus 从快照返回。ready 和断线由测试手工触发，没有隐藏测试URL或生产window开关。

## B/C/D 文件槽与 UI 语义

- B：`stars/CanvasSlot.tsx` 与 `TimelineSlot.tsx`（可自行拆分子文件）。
- C：`stars/PanelsSlot.tsx` 及详情、项目、平台面板。
- D：`stars/PeripherySlot.tsx`、`LoginSlot.tsx` 及简报、列表、图例、显示、命令行。
- 共同类型 `stars/slots.ts`，共享状态 `stars/ui.ts`，主题 `stars/theme.ts`；按任务归属改槽位，无需共同修改 StarsApp。

`StarsUI`：`getSnapshot/subscribe`、`selectProject(hiveId?)`、`selectObject({hiveId,kind,id,number?})`、`openProject(tab)`、`openPlatform(tab,setupQuery?)`、`openOverlay(overlay)`、`close()`，React 用 `useStarsUI(ui)`。platform 单独出现并清左右；list/brief 清左右；项目面板可和右详情共存；close 按 overlay → platform → selection → project → 全项目逐层退出。只有 D 注册全局快捷键/Esc；C 负责焦点 trap/归还，避免重复 close。`#/setup?...` GitHub 回调打开 platform setup，原 query 在 setupQuery（保留 connected/error）；C 应显示对应通知。

主题：`THEMES` 是 bright/paper/night 可扩展表，含 colors/render；`themeStore.getSnapshot/subscribe/setTheme/setStyle/setGrid`，React `useTheme`。choice 为 system/主题id，style 为 classic/simple。colors 写 `--paper/ink/ink2/ink3/hair/accent/veil`，字体写 `--title/body`；画布订阅主题对象 render 参数。仅偏好写 localStorage（全部try/catch且校验合法主题）。
