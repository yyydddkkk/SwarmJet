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

## 合并与验证进度

- A → E 已按顺序执行 `git merge --no-ff`，均无冲突；每次合并后 `cd web && npm run build` 通过。GitHub PR 尚未合并。
- `python3 -m unittest discover -s deploy -p 'test_*.py'`：28 项通过。
- `cd deploy && docker compose --env-file .env.example -f compose.yaml -f compose.cells.yaml config -q`：退出码 0。
- 指定测试数据库的全量 Go race 测试运行中；B/C/D 仅改前端，后续按最终文件范围判断是否复用未受影响结果。
