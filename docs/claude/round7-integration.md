# 第七轮开发与集成记录

## 范围补充确认

2026-10-20，主代理核对实际 API 后，用户明确批准以下处理，继续保持本轮不改后端：

- `internal/api/models.go:181` 的用量接口只接受 `all`、`today`；响应没有日期明细。因此显示今天、累计的真实花费；7 天走势、本周花费和昨晚花费明确标为“暂时无法获取”，不通过 mock 或累计数冒充。这三项列为集成 PR 的已知缺口。
- `internal/api/setup.go:41` 的接入 API 提供状态、GitHub App 清单和仓库列表，没有最近 webhook 投递记录与手动重新同步接口。因此展示真实同步状态和“重新读取”；投递记录与手动同步明确标为暂不可用，不把读取状态称为重新同步。

用户两项答复分别为“保持前端范围，批准如实标明缺口”和“批准如实标明缺口，保持本轮前端范围”。原始 `round7-prompts.md` 保留逐字内容，本记录说明已获批准的验收范围补充。

同日，用户另行批准“昨晚合并数”明确标为“暂时无法获取”，同时展示真实的最近已合并 PR 列表。原因是 PR 列表只有 `updatedAt`，没有 `mergedAt`；最近更新时间不能冒充合并时间。用户答复为“批准明确标明缺口，并展示真实列表”。

## 任务 E 审查

- PR：[#70](https://github.com/yyydddkkk/SwarmJet/pull/70)，头提交 `8205642`。
- 已逐行核对：只改 pstack 的 verifier 指令、验证 skill、Queen 风格和目录版本 `1 → 2`；`methodology.yaml` 的事件规则、合并策略不变。
- 主代理独立执行 `go test ./internal/methodology/`，通过。开发者执行指定测试数据库的 `go test -race -p 1 ./...`，日志退出码为 0；主代理核对日志末尾和 PR 四项 CI，均通过。集成完成后另跑最终全量测试。
- 关键行为：兼容要求必须与固定基线用相同输入、环境检查；未获允许的行为变化判 `fail`；同一 PR/head 依赖自动验证，仅在自动 verifier 失败或取消且无工作中、排队中的同 head verifier 时补派。

## 任务 A 初审发现

以下问题已交给开发者修复，最终以提交和独立测试为准：

- 快照及独立列表异步响应可能晚到，覆盖新状态；需 generation、请求序号和 stop 防护。
- PR SSE 直接读取最新状态，而列表有 30 秒缓存；列表不能把最新 head、ready 覆盖回旧值。重拉快照也须正确处理这个问题，同时保留重连期间的新变化。
- 每项目独占 SSE 会耗尽 HTTP/1.1 的同源连接。主代理真实 Chromium 最小实验：7 条流中仅 6 条 ready，普通 fetch 一秒后仍未完成，关闭流后恢复。采用后端已支持的全项目事件流，在浏览器内部按项目分发；缺少项目标识的 queue、methodology 事件触发规范快照重拉，不能串项目。
- 重新拉快照期间的重复 ready 不能丢掉已缓冲消息；只有真实 message 事件触发脉冲。
- 损坏偏好不能让主题初始化崩溃；`.gitignore` 的宽泛 `data/` 不能漏提交新数据层。
- 字体仅提交本地 WOFF2、许可证和可复现来源；中文采用常用字与实际界面文字子集，并核对实际字重及保留字体名称要求。

## 任务 A 审查结论

- PR：[#71](https://github.com/yyydddkkk/SwarmJet/pull/71)，固定审查提交 `ce3bd86`。上述重要问题已分别提交修复并补测试。未改 `internal/`、`deploy/`、`cmd/`。
- 主代理独立执行 `cd web && npm ci && npm run build && npx playwright test`，退出码 0，87 项全部通过（46.6 秒）；`git diff --check origin/dev...HEAD` 通过。
- 主代理独立读取字体：Noto 两个子集各覆盖 4,295 个字符，字重轴 400–500；WOFF2 共 2,572,424 字节，public 中没有 TTF。六套字体许可证齐全，IBM Plex 使用官方原始 WOFF2，避免修改保留名称字体。当前界面无 Google Fonts 外部请求依赖。
- A 的画布和面板仍是约定的基础槽位；详细视觉与功能由 B/C/D 完成。A 的旧 HTTP mock 没有新列表、事件接口，因此其骨架截图展示断线和空 Agent，并不构成本轮集成视觉验收；D 将提供完整的新前端 mock，最终截图在集成后重做。
- B/C/D 从审查通过的 `ce3bd86` 分出，各自在独立 worktree 开发。

## 集成修复

- 消息时间比较：主代理新增真实回归用例，`12:00:00Z` 的提问在 `12:00:00.5Z` 收到回答后仍显示待决定，修复前测试失败。原因是 RFC3339 可省略小数秒，字符串顺序不代表时间顺序。快照排序、Agent 最新外发消息和问答配对改为比较解析后的时间；数据层 15 项测试和构建通过。此修复独立提交，不改变共享接口。
- 首轮项目加载：D 指出 shell 的初始空数组无法代表真正没有项目。集成分支增加加载状态，初次列表请求未完成或失败时只展示读取中、明确错误和重试；成功返回空列表后才挂载首次使用。真实浏览器用延迟及 503 响应验证不会闪现空项目入口；数据层/骨架 16 项测试和构建通过。此修复独立提交，不增加共享 props。
- 会话过期：独立复核指出 401 原来只会回登录，缺少原因说明。登录页现显示“会话已过期，请重新登录后继续。”，登录成功后清除；对应浏览器回归和构建通过，独立提交。

## 合并与验证进度

- A → E 已按顺序执行 `git merge --no-ff`，均无冲突；每次合并后 `cd web && npm run build` 通过。GitHub PR 尚未合并。
- `python3 -m unittest discover -s deploy -p 'test_*.py'`：28 项通过。
- `cd deploy && docker compose --env-file .env.example -f compose.yaml -f compose.cells.yaml config -q`：退出码 0。
- `TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:55450/postgres?sslmode=disable' go test -race -p 1 ./...`：主代理在集成分支实际执行，全部通过，退出码 0；ACP 偶发用例本次一次通过。B/C/D 仅改前端，最终核对 Go、方法论、部署文件未再变化后复用结果。
- A、E 两个任务 PR 的 GitHub CI 均四项全绿；最终集成 PR 仍需另等 CI 全绿。

## B / C / D 审查与独立验证

- B：[#74](https://github.com/yyydddkkk/SwarmJet/pull/74)，最终 `17663d3`。主代理对原 `90ecfe4` 实际执行 npm ci、构建、完整 103 项测试，全部通过；补字体就绪重绘修复后独立再跑画布 17 项，全部通过。核对六种主题/风格组合截图、脉冲仅由消息触发、冻结画面不变、派活走主 Agent、合并和完成有限动画、空闲停止。独立重测 300 Agent / 50 PR：全部项目 p95 1.2 ms、项目 p95 2.9 ms；各一秒空闲新增绘制帧为 0。计时为 Chromium 中 Canvas 绘制回调，不包含浏览器合成。
- C：[#72](https://github.com/yyydddkkk/SwarmJet/pull/72)，最终 `132f34a`。主代理独立 npm ci、构建、原版完整 103 项测试通过；独立复核另以 mock 复现 P1 新 head 沿用旧检查即可合并、P2 切项目残留前项目版本。开发者分别以 `3a02f82`、`298bb7f` 修复，独立复核七项回归全过；最终开发者全量 112 项通过。另独立修复双次退出、关闭左侧误关右侧、排名导航和事件全文；方法论默认首屏改为角色图与可读规则，详细元数据和权限按需展开。完整旧功能对照见 `round7-panels.md`。
- D：[#73](https://github.com/yyydddkkk/SwarmJet/pull/73)，最终 `edd6e2b`。开发代理提交后遇模型容量错误，由主代理接管推送和建 PR。主代理独立 npm ci、构建、完整 102 项测试通过；随后独立复现旧首次图例计时器关闭后来手动图例，以独立 `fix:` 提交修复。外围 16 项及构建通过；旧测试复用未受影响结果。登录、简报、列表的 axe 证据 violation 数组均为空；已核对仅有开发依赖 `@axe-core/playwright`，没有运行时新增依赖。完整口径见 `round7-d-periphery.md`。

## 待确认的接口缺口

现有 API 没有最近工具调用轨迹，运行时相关路由直接返回 404。C 如实显示“最近的工具调用：平台暂未提供；下方显示真实消息。”，没有复制演示工具日志。此项已另向用户请求确认，尚未把它记为已批准范围；旧 Diff、终端本来是占位，继续如实说明。

## 最终合并顺序与旧页面删除

- A → E → C → D → B 均已使用 `git merge --no-ff`，无代码冲突。三路测试配置统一可选 `PLAYWRIGHT_PORT`，默认 5274，因此配置无冲突。
- 每次合并后都实际执行前端 build。D 首次 build 因集成 worktree 尚未安装新开发依赖 axe 而失败；执行 `npm ci` 后 build 通过。没有绕过检查或修改测试来隐藏失败。
- C 的旧功能对照已逐项核对，B/D 补齐入口；删除 `web/src/hive/`：ConversationStream、CreateHiveDialog、ForagerPage、HiveConsoleApp、HivePage、HiveSettings、HivesPage、IconLab、LoginScreen、MethodologyPage、ModelsPage、SetupPage、UsagePanel，以及旧 components、diff、format、CSS、icons、pixel-icons、pixel、types、use-resource。唯一仍需的 HTTP 客户端从 `hive/api.ts` 移至 `data/http.ts`，fixture 和测试类型统一引用数据层。
- `main.tsx` 仅挂载新界面，移除 legacy 路由分支与骨架中的旧创建链接；新建项目由 D 的入口负责。
- 旧测试 `message-trust.spec.ts` 迁移为 `provenance.spec.ts`，三项分别验证四种来源、未知来源和外部事件全文提示，全部通过。
- 随旧页删除专用测试：`app-shell.spec.ts`、`backend-shapes.spec.ts`、`create-hive-round5.spec.ts`、`forager-round4.spec.ts`、`hive-console.spec.ts`、`methodology-round5.spec.ts`、`methodology.spec.ts`、`models.spec.ts`、`pixel-icons.spec.ts`、`queen-round5.spec.ts`、`settings-round5.spec.ts`、`setup.spec.ts`。新数据层、画布、面板、外围测试验证新位置和共享契约；旧蜂巢像素图的实现断言随对应代码删除。
- 删除后 `npm run build` 及迁移的三项来源测试通过；后续另跑全部新界面测试和联合验收。
