# 第八轮开发与集成记录

本轮规格：round8-prompts.md。视觉依据：本轮 design/stars/index.html 和 frontend-stars-design.md 第 10 节。主目录用户改动保留，代码开发和集成均在独立 worktree。

## 开发与合并计划

A（perf/pr-list）与 B（feat/frontend-data-v2）从 origin/dev bb53602 并行开发。B 经审查后，C/D/E 从 B 分支开始。集成分支 integrate/round8 从 origin/dev 开始，按 B → A → D → E → C，以 --no-ff 合并，每步前端构建。所有远端 PR 合并、部署均等待用户明确批准。

## 已确认的范围补充

用户明确批准任务 A 对 internal/router/event.go、router.go 做最小改动，仅传递并保存 webhook PR 状态与时间，不改变派工、验证或合并逻辑。

## 验证与审查

基线验证：origin/dev bb53602 前端 `npm ci`、`npm run build` 和 `npx playwright test` 已真实运行；85 项 Playwright 全部通过（1.3 分钟）。原有测试包含“空闲停止绘制”等旧规格断言，本轮会按新规格更新为环境动画与隐藏/减少动态时停止。

尚在开发；本文件后续仅记录实际执行结果。真实部署验收留待用户批准集成 PR 后进行。未验证项目必须明确标为“未验证”。

## 第七轮问题清单

|编号|验收关注点|当前结论|
|---|---|---|
|1|见 frontend-issues-round7.md 第 1 条|未验证|
|2|见 frontend-issues-round7.md 第 2 条|未验证|
|3|见 frontend-issues-round7.md 第 3 条|未验证|
|4|见 frontend-issues-round7.md 第 4 条|未验证|
|5|见 frontend-issues-round7.md 第 5 条|未验证|
|6|见 frontend-issues-round7.md 第 6 条|未验证|
|7|见 frontend-issues-round7.md 第 7 条|未验证|
|8|见 frontend-issues-round7.md 第 8 条|未验证|
|9|见 frontend-issues-round7.md 第 9 条|未验证|
|10|见 frontend-issues-round7.md 第 10 条|未验证|
|11|见 frontend-issues-round7.md 第 11 条|未验证|
|12|见 frontend-issues-round7.md 第 12 条|未验证|
|13|见 frontend-issues-round7.md 第 13 条|未验证|
|14|见 frontend-issues-round7.md 第 14 条|未验证|
|15|见 frontend-issues-round7.md 第 15 条|未验证|
|16|见 frontend-issues-round7.md 第 16 条|未验证|
|17|见 frontend-issues-round7.md 第 17 条|未验证|
|18|见 frontend-issues-round7.md 第 18 条|未验证|
|19|见 frontend-issues-round7.md 第 19 条|未验证|
|20|见 frontend-issues-round7.md 第 20 条|未验证|
|21|见 frontend-issues-round7.md 第 21 条|未验证|
|22|见 frontend-issues-round7.md 第 22 条|未验证|
|23|见 frontend-issues-round7.md 第 23 条|未验证|
|24|见 frontend-issues-round7.md 第 24 条|未验证|
|25|见 frontend-issues-round7.md 第 25 条|未验证|
|26|见 frontend-issues-round7.md 第 26 条|未验证|
|27|见 frontend-issues-round7.md 第 27 条|未验证|
