# 星图共享数据契约（任务 A）

B/C/D 只能从 `src/data/index.ts` 读取数据或发操作。`hive/types.ts` 仅为旧页面重导出本目录类型。API JSON 依据 `internal/api/{events,prs,web_lists,hives,methodology,models,setup,queen}.go` 和 `internal/domain`；没有发明历史花费或 webhook 操作。

## React、画布与面板

`DataProvider({source?,children})` 默认使用 `browserDataSource`；`useDataSource()`、`useDataAPI()` 提供可替换的数据源与全部 REST 操作。

shell 在 `StarsApp.tsx` 为每个项目创建且持有一个 `ProjectStore`，所有组件共享 `StarsSlotProps.stores` 中的同一实例。React 组件调用 `useProjectState(store)`；画布使用 `store.getSnapshot()` / `store.subscribe(draw)`；不要在每个面板重新创建 store 或 SSE。`useProjectStore(id)` 仅用于独立挂载、自己管理生命周期的组件，不能代替 shell 已共享的实例。

`ProjectStore` / `createProjectStore(id, source?)`：`start()`、`stop()`、`getSnapshot()`、`subscribe(listener)`、`refresh()`、`mergePR(number, observedHeadSha)`、`acknowledgePulses()`。`getSnapshot()` 稳定返回 `ProjectState`：`snapshot`、`derived`、`connection`、`error`、`errorStatus`、`pulses`、`revision`、`retryDelay`。`stop` 清连接、计时器及异步提交；401 暴露 errorStatus，真实 REST 同时通知 shell 回登录。

- 浏览器只开一条不带 hive 参数的 `/api/events` 连接，按 hiveId（hive 事件按 id）分发，避免HTTP/1.1六连接上限。queue/methodology JSON没有hiveId，不能猜所属项目，收到时让所有项目重新拉快照；mock仍可精确推给指定项目。
- 每次 ready（含重连）重新拉快照，拉取期间缓存增量，快照完成后按收到顺序重放；旧请求不能覆盖新快照。
- message 依据 ID 去重；只有实际事件产生脉冲（同一事件 ID 一次，快照不产生脉冲）。`pulses` 至多保留最新100条，画布消费后 acknowledge。
- agent 按 id、pr 按 number 浅合并。PR 事件还会回拉 canonical 列表，补齐 title/attestations/ownerId，保留当前快照周期内已观察到的SSE head/state/merged/ready，防30秒后端缓存回退；signals 在 external message 或 agent 事件后回拉。
- 重连 1、2、4、8、16、30 秒，之后最大30秒；ready 快照成功才重置。显示 `连接已断开，正在重连`。
- `snapshot.messages` 是 `LiveMessage = Message | MessagePreview`。SSE 没有完整 body/state/hop，使用 `messagePreview()` 显示简短内容；完整对话通过 `api.listMessages` 分页读取。初始快照会遍历各 inbox 分页并去重，包含当前管理员地址。
- `derived`：`nodes`、`decisions`、`needsAttention`、`agentStatuses`；`deriveProject` / `agentStatus` / `messageEndpoint` / `messagePreview` 可独立使用。verifier 判断 `roleName`，卫星 `parentId = hiveId:pr:number`；queen ID 为 `hiveId:queen`；Agent ID 保持原 id。

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

`createMockDataSource({[hiveId]: snapshot}, partialApiOverrides?)` 返回 `source`、`calls`、`snapshots`、`setSnapshot(id,next)`、`ready(id)`、`push(id,event)`、`disconnect(id)`、`connections`。创建 store 时传 `mock.source`，或 `<StarsApp source={mock.source}/>` / `<DataProvider source={mock.source}>`。未配置的操作明确报错，不能悄悄调用真实API；默认 getMethodology/listPRs/listSignals 从快照返回。ready 和断线由测试手工触发，没有隐藏测试URL或生产window开关。

## B/C/D 文件槽与 UI 语义

- B：`stars/CanvasSlot.tsx` 与 `TimelineSlot.tsx`（可自行拆分子文件）。
- C：`stars/PanelsSlot.tsx` 及详情、项目、平台面板。
- D：`stars/PeripherySlot.tsx`、`LoginSlot.tsx` 及简报、列表、图例、显示、命令行。
- 共同类型 `stars/slots.ts`，共享状态 `stars/ui.ts`，主题 `stars/theme.ts`；按任务归属改槽位，无需共同修改 StarsApp。

`StarsUI`：`getSnapshot/subscribe`、`selectProject(hiveId?)`、`selectObject({hiveId,kind,id,number?})`、`openProject(tab)`、`openPlatform(tab,setupQuery?)`、`openOverlay(overlay)`、`close()`，React 用 `useStarsUI(ui)`。platform 单独出现并清左右；list/brief 清左右；项目面板可和右详情共存；close 按 overlay → platform → selection → project → 全项目逐层退出。只有 D 注册全局快捷键/Esc；C 负责焦点 trap/归还，避免重复 close。`#/setup?...` GitHub 回调打开 platform setup，原 query 在 setupQuery（保留 connected/error）；C 应显示对应通知。

主题：`THEMES` 是 bright/paper/night 可扩展表，含 colors/render；`themeStore.getSnapshot/subscribe/setTheme/setStyle/setGrid`，React `useTheme`。choice 为 system/主题id，style 为 classic/simple。colors 写 `--paper/ink/ink2/ink3/hair/accent/veil`，字体写 `--title/body`；画布订阅主题对象 render 参数。仅偏好写 localStorage（全部try/catch且校验合法主题）。
