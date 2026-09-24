# Console API v1

All paths under `/api`. JSON uses camelCase from `internal/domain/types.go`. API errors `{error:string}`. Session is HttpOnly SameSite=Strict cookie; send credentials same-origin. Mutations require header `X-Requested-With: swarmjet`. SSE uses cookie authentication. No secrets returned.

- GET `/project-defaults` -> `{agentImage,adapter,timeoutSeconds,maxReworks}`; deployment defaults for new projects.
- POST `/projects/discover` body `{metaUrl,metaRef?,gitlabUrl?,connectionId?,credentialRef?,defaultTargetBranch?}` -> `{name,suggestedId,metaUrl,metaRef,gitlabUrl,repositories,branches,warnings}`. Read-only GitLab metadata import; `connectionId` uses a saved connection bound to the Meta-Repo GitLab address, otherwise `credentialRef` names a deployment environment variable. Defaults the Meta ref to its remote default branch. Child targets default to the requested `dev`; a missing target never silently becomes `main`, and `createFrom` remains empty for explicit selection.
- Disabled projects may be saved as drafts with just stable ID, name and Meta URL. Supplied fields are validated; models/repositories/checks may be incomplete. Enabling requires the complete execution contract. Imported projects also check the remote Meta manifest before first enable or when an enabled project's source or acceptance configuration changes. A failed enable does not save or increment the version.
- GET `/runtime/status`, `/runtime/models` proxy the private Worker runtime administration service; responses contain only metadata/catalogs, never credential contents. Login configuration detection is not a provider-credit or access check.
- Saved connections are bound to the GitLab address they were verified against. `connectionId` must match the project `gitlabUrl`, and repository/Meta URLs must use the configured GitLab host; the server resolves a saved token only for the matching address, so a token is never sent to a different origin. Updating a connection's `gitlabUrl` without supplying a new `token` is rejected; a rotated token is verified against the new address. Enabling a managed project requires persisted `readyForTasks`; otherwise enabling, starting a task and the issue scanner all refuse it.
- POST `/runtime/verify` body `{model}` runs one minimal real model call and returns `{ok,model,message,durationSeconds}`. It never returns credentials. Invalid model ids are 400, a failed call 502, a timeout 504 and a disabled runtime 503. Only a successful call proves the selected model is usable.
- POST `/connections/verify` body `{gitlabUrl,token,mergeToken?}` validates a token against that exact GitLab origin without persisting it, returning `{ok,gitlabUrl,username,name,fingerprint,hasMergeToken}`. The token is never echoed. 401 invalid/expired, 403 missing scope, 404 wrong base URL, 502 unreachable.
- GET `/connections` -> Connection[] metadata only. POST `/connections` body `{name,gitlabUrl,token,mergeToken?}` verifies and stores the token encrypted; returns Connection metadata without the token. GET `/connections/{id}` -> metadata. PUT `/connections/{id}` body `{name?,gitlabUrl?,token?,mergeToken?,version}` verifies and rotates; omitting `token` keeps the stored ciphertext. DELETE `/connections/{id}` -> 204 or 409 when a project still references it.
- GET `/connections/{id}/repositories?search=&page=&perPage=` lists repositories the saved token can access, returning `{projects:[{id,name,pathWithNamespace,defaultBranch,httpUrlToRepo,webUrl,archived,description}],page,perPage,hasNext,hasPrev}` with real GitLab IDs and default branches. Responses never include the token.
- GET `/connections/{id}/repositories/{projectId}/branches` returns the repository's branch names as `string[]` using the saved token. Responses never include the token.
- POST `/projects/{id}/initialization` starts (or returns) the durable managed-Meta initialization; 202 with Initialization. GET `/projects/{id}/initialization` returns the current state (`status:"none"` when never started). POST `/projects/{id}/initialization/retry` re-runs a blocked job. POST `/projects/{id}/initialization/cancel` cancels it. POST `/projects/{id}/initialization/answers` body `{answers:{questionId:text}}` records missing information and resumes.
- Initialization statuses: `pending`,`running`,`needs_input`,`ready`,`blocked`,`cancelled`. `repositoriesConnected`, `analysisComplete` and `readyForTasks` are separate; only `ready`+`readyForTasks` means the project can be enabled or accept tasks. Incomplete verification (no commands, no isolated `INIT_VERIFY_IMAGE`, failing commands, or no justified integration config) is `blocked` and retryable, never reported as a pass; the console shows `dryRun` verification when no command ran. Reports, pinned repository SHAs and the pinned analysis commit are retained. A step that loses its lease or observes a newer record does not write its result, so a cancel is never overwritten.
- Project fields: `connectionId` (saved connection reference), `metaMode` (`managed` or `imported`), `autoMerge` (null preserves legacy enabled behavior; new projects default true) and `initStatus`. Managed projects must not set `metaUrl`; their Meta-Repo is created internally. A managed draft may omit model/checks; enabling still requires the complete execution contract.
- GET `/runtime/terminal` upgrades to WebSocket after administrator session and exact Origin checks. Input: JSON `{type:"input",data}` or `{type:"resize",cols,rows}`. Output: binary terminal bytes or JSON text `{type:"error",message}` / `{type:"exit",code}`. No terminal transcript is persisted. API forwards a private Worker secret, never browser credentials. Closing the session stops only its setup container; the profile home persists.

- POST `/login` body `{username,password}` -> `{username}`; POST `/logout` -> 204; GET `/session` -> `{username}` (401 when not signed in).
- GET `/projects` -> Project[]; POST `/projects` body Project -> saved Project; PUT `/projects/{id}` body Project (current version required) -> saved Project. Version conflicts 409. For new projects set version=0. Credentials are environment variable NAMES only.
- POST `/projects/{id}/check` -> `{ok:boolean,checks:[{name,ok,message}]}` (remote branch read; does not initialize targets).
- GET `/tasks?project=&stage=&view=&q=&cursor=&limit=` -> `{items:Task[],nextCursor?,total,counts:{all,active,attention,ended,completed}}`; POST `/tasks` body `{projectId:string,issueProjectId:number,issueIid:number,dependencies?:IssueRef[]}` -> Task (202). Project must be enabled, Issue opened and have autonomous label.
- GET `/tasks/{id}` -> Task; GET `/tasks/{id}/events?after=0` -> Event[]. GET `/tasks/{id}/stream` SSE named `update`, JSON `{task:Task,events:Event[]}` (may re-send task snapshots; events have IDs).
- New Task snapshots include `round`, `previousTaskId`, `triggerNoteIds`, `triggerText`, and the pinned `dependencyTasks` map. A round is created only once for each explicit source Issue note ID; new notes arriving during an active round are grouped into the next round.
- POST `/tasks/{id}/commands` body Command -> `{accepted:true}` (202). `id` is crypto.randomUUID(); kinds `pause`,`resume`,`cancel`,`retry`,`note`,`approve`,`reject`,`dependencies`. note/reject require text; approve requires digest equal to task.reviewDigest (current reviewed full commit set). In waiting_approval buttons approve/reject; blocked button retry; pause/resume/cancel according state. Notes about ambiguous requirements never stop an otherwise runnable task.
- GET `/tasks/{id}/artifacts?path=<relative>` -> plain text or file. Paths are relative to the task's persisted run directory (not arbitrary filesystem paths). Execution.logPath is absolute; for log request use `executions/<executionID>/output.jsonl` (actual path exposed can derive suffix after `/runs/<taskId>/`).

Task stages: queued, waiting_dependencies, preparing, developing, verifying, publishing, reviewing, waiting_approval (legacy tasks), waiting_manual_merge, acquiring_merge, merging, partial_merge, finalizing, completed, blocked, cancelled; `paused` boolean overlays a stage. Terminal completed/cancelled. `waiting_manual_merge` is used when the project has auto-merge disabled or automatic verification is incomplete. New rounds keep the source Issue open after completion.

Project defaults for form: metaRef `main`, development/review adapter chosen from globally enabled agents, image `swarmjet-agent:local`, timeoutSeconds 5400, maxReworks 3, requireCI true, integration startupTimeoutSeconds 180. Model is optional; new tasks use CLI defaults. A legacy OpenCode override uses provider/model. GitLabURL explicit. CredentialRef `GITLAB_TOKEN`, MergeCredentialRef `GITLAB_MERGE_TOKEN`. Repositories require id, projectId, URL,path,targetBranch; createFrom optional only needed to initialize missing target. Integration composeFile is relative to Meta-Repo, checks list has name, service, argv string array, required. At least one required check.

Control plane is one administrator. UI pages: login; task list with stage filtering and new task dialog; task detail with repositories/MRs, timeline, agent runs/logs, assumptions, verification, manual approval and task actions; project list/editor with repository targets, agent/model configs, integration checks. No placeholder/mock data on failures: show actionable error and retry.

### 可选 Meta 仓库发布

- `GET /api/projects/{id}/initialization/publication/namespaces?search=&page=1`：分页列出连接可见的群组 / 个人空间（不保证创建权限）。
- `GET /api/projects/{id}/initialization/publication/repositories?search=&page=1`：分页列出已有仓库，提交时校验必须为空。
- `POST /api/projects/{id}/initialization/publication`：首次请求为 `{ "mode": "create", "namespaceId": 42, "path": "project-meta" }` 或 `{ "mode": "existing", "existingProjectId": 42 }`。失败重试可发送 `{}`，继续使用持久化目标。
- 返回 202 和 Initialization；`publication.status` 为 `pending / failed / published`，失败独立于初始化状态。通过原初始化 GET 轮询；成功包含 `url`、`path`、`ref` 和 `sha`。

新建远程固定为 private。Git 操作只推送固定初始化提交到尚不存在的目标分支，拒绝覆盖已有内容，禁用 HTTP 重定向，凭证不进入状态记录。

### 开发任务验收策略

Project.acceptanceStrategy: `none | repository | gitlab_ci | custom`。新建项目省略时默认 `none`，跳过平台额外验收；已启用的 GitLab CI 要求及合并检查仍执行。历史项目空值保留原 Compose 行为。策略随项目快照固定在任务中，修改项目配置不改变已创建任务。

Project.reviewMode: `none | independent`。新项目默认 `none`，不启动审查 Agent；合并 Agent 沿用开发 Agent 的配置和会话。历史项目空值保留独立审查。关闭审查不会关闭 GitLab MR、目标分支或合并检查。

`Task.autoMergeOverride` 为可选布尔值；空值继承任务快照中的 `Project.autoMerge`。任务详情可发送 `set_auto_merge` 指令，参数为 `autoMerge: boolean`。指令异步送达，界面只在任务状态实际更新后显示生效。进入合并执行或已有仓库合并后不再接受切换；合并门禁同时核对尚未送达的关闭指令，关闭后进入 `waiting_manual_merge`，重新开启则在条件满足时继续自动合并。

管理员可对已暂停、已有提交且开发 Runner 已停止的任务发送 `continue_committed` 命令（需填写 `text` 说明），让该任务从发布阶段继续，并为本任务关闭额外验收。后续仍核对工作树、MR、项目已启用的独立审查和合并条件；项目默认策略不受此命令影响。

自定义 IntegrationConfig 新增 `mode: compose | commands`、`image`、`commands: [{name,path,argv,evidence}]`。repository 的镜像可通过 integration.image 覆盖，具体命令由开发结果 acceptanceCommands 提供；gitlab_ci 不要求 Compose。

AgentResult 的 `humanAcceptance: {prerequisites: string[], steps: [{action,expected}], notes: string[]}` 用于来源 Issue 中按任务轮次与审查提交绑定的人工复核指引；MR 描述保留其链接。`Verification.environmentFailure` 标识工具链未能可靠启动或加载；此类失败保留报告但不计入代码返工。`Task.acceptanceIncomplete` 表示自动验收证据不足，禁止自动合并。Issue 常开；仅新的 `/swarmjet run` 评论触发后续任务，评论 ID 持久化去重。


### Agent selection

- `GET /api/runtime/agents` returns `{agents:[{id,name,executable,setupCommand}], enabled:[]}`. Supported IDs: `opencode`, `codex`, `dst`, `agy`, `claude`.
- `PUT /api/runtime/agents` accepts `{enabled:["opencode","codex"]}` with existing administrator auth and CSRF protection. Empty disables all for new tasks. Unknown/duplicate IDs return 400; new installations have no implicit enabled Agents. This endpoint does not install software or start a CLI.
- Project `development.adapter` is the default for all task roles. One `agent:<id>` Issue label overrides it; unknown, disabled, model-qualified or multiple Agent labels are rejected. Polling leaves rejected Issues unclaimed and updates a deduplicated rejection note; fixing the configuration permits a subsequent poll to accept them. Manual task creation returns 400 for the same errors.
- GET `/projects/:id/labels` reports selectable label presence for each registered GitLab repository; POST to the same path creates missing labels. Enabled projects reconcile `autonomous` and globally enabled `agent:*` labels on startup, after configuration changes, and periodically. Existing labels are not edited or removed. Stage and clarification labels are ensured with defined colors when first applied.
- At acceptance, both role adapters are fixed in the task project snapshot and model overrides are cleared (`useCliDefaults:true`). Enablement/label edits affect new tasks only. CLI files are snapshotted once at first execution, on the assigned node; resume uses the exact prior CLI session ID.
- Configured images must contain the selected CLI on every node. `AGENT_CLIS` controls build-time installation independently of platform enablement. Static managed-Meta initialization continues to require OpenCode and its restricted tool policy.


### Agent image configuration (single host)

- `GET /api/runtime/images`: `{config, builds, active, defaultImage, agents, parameters, scope:"local"}`. Recipe: `{agents:[], baseImage, extraPackages:[], buildArgs:{...}}`.
- `PUT /api/runtime/images/config`: save a validated draft. Versions, registries and extra apt packages are allowlisted; credentials in registry URLs and unknown build argument keys are rejected.
- `POST /api/runtime/images/builds`: save/snapshot that recipe and start one asynchronous build (202). No implicit activation or Agent enablement.
- `POST /api/runtime/images/import`: `{reference}` checks an existing local image and records its immutable ID (201); no pull.
- `GET /api/runtime/images/builds/{id}/log`, `POST .../{id}/cancel`, `POST .../{id}/activate`: log, cancellation and explicit activation. Activation requires a successful record and closed configuration terminal (otherwise 409).
- Build states: `building`, `cancelling`, `succeeded`, `failed`, `cancelled`, `interrupted`. A Worker restart marks unfinished builds interrupted. All routes require administrator authentication, writes require CSRF protection, and the Worker additionally checks its private secret.
- Applied images override both role image fields when a new single-host task is accepted, using the immutable image ID. Existing task snapshots are never re-resolved. The project-defaults endpoint and configuration terminal also use the applied image. When BUILDER_ADMIN_URL is configured, image routes use the dedicated builder in either execution mode. Legacy distributed deployments without a builder still return 503.

## Managed execution environments (v2)

- `GET /api/networks`: `{profiles, defaultId}`; only public fields, versions, and `hasPassword`.
- `PUT /api/networks`: profile plus optional `password`; optimistic version check, append-only versions. Omitted password preserves it; changing proxy endpoints with saved credentials requires re-entry.
- `DELETE /api/networks?id={id}&version={latestVersion}`: permanently removes all versions and encrypted credentials for the profile. A stale version returns 409; deleting the platform default resets it to direct. Existing Node, build, or task references are not rewritten and can fail to resolve afterward.
- `PUT /api/networks/default`: `{id}`, empty means direct. Choices on a Node or build are `{mode:"default"|"direct"|"profile", id?}`.
- `GET/PUT /api/registry`: address, repository, pushUser, pullUser, password-presence flags. PUT accepts optional pushPassword/pullPassword, encrypted with the platform master key.
- In Compose and native development, `GET /api/registry` defaults to the generated LAN TLS registry `{mode:"builtin",address:"<LAN-IP>:5443",repository:"swarmjet/agent",builtinAvailable:true}`. `PUT` can select `mode:"custom"` with an HTTPS registry address and credentials, or return to `mode:"builtin"`. Authenticated `POST /api/registry/reveal` returns the current push/pull passwords only on explicit administrator request.
- `GET /api/registry/images`: live manifest versions in the configured repository, grouped by digest with their tags; includes externally pushed images. `GET /api/registry/images/{digest}/usage` returns blocking platform references.
- `DELETE /api/registry/images/{digest}` accepts `{expectedTags:[...]}`. It rechecks tags and platform references under the image configuration lock, refuses versions used by node or project configuration, unfinished tasks or initializations, and in-progress builds, then deletes the manifest by digest. Completed task history and build records remain. Disk reclamation is a separate registry maintenance operation.
- `GET /api/nodes`: existing node/load/online fields plus administrator `config` and node `readiness`. Readiness includes config/network versions, default image, per-image states and CLI capabilities.
- `PUT /api/nodes/{id}/config`: version, name, enabled, archived, capacity, network, image, images, memoryMB, cpus, pidsLimit. Image references must be registry digests. Zero resource limits select 2048 MiB / 2 CPUs / 256 PIDs. Archiving an active node returns 409.
- `POST /api/nodes/{id}/credential`: create/rotate a node-specific credential, returned once. `DELETE` revokes it.
- `POST /api/nodes`: authenticated heartbeat, response may include private `desired` configuration and read-only registry credentials. New nodes are paused; heartbeats never overwrite administrator config. Simultaneous live advertisements of the same identity at different addresses are rejected.
- Runtime routes accept `?node={id}` to address that Node's Runner status, models and configuration terminal. `POST /api/runtime/runner-test?node={id}` starts a short-lived restricted container from that Node's configured Runner image and returns `{ok,image,agents}` for CLI installation; it does not verify authentication, model calls or task workspace. `POST /api/runtime/network-test?node={id}` with `{target}` performs HEAD checks from the Node process and a restricted container, returning scope/status/error; no model requests. `/runtime/agents` remains platform-wide.
- Image response has `scope:"registry"` for the dedicated builder. Recipes also have repository, platform, network and resolved networkVersion. Successful distributed builds expose `reference` (repository@manifest-digest), distinct from local imageId. Apply by updating target Node configurations; preparation is asynchronous. Local import/activate are rejected by the dedicated builder.
- Dedicated builder builds always publish to the configured repository path; recipe requests cannot override it. Historical build records from other paths remain readable.
- Project `nodes` is `{mode:"auto"}` or `{mode:"selected",ids:[...]}`. Empty role images inherit the selected Node's default when this policy is supplied. Tasks freeze the resolved role images, Node config version, and network version.

All management writes retain session/CSRF validation. Network and registry secrets are not returned by normal reads or stored in workflow history. Node protocol calls remain authenticated; production remote transport requires HTTPS.
