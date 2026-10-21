# 慢 PR 首屏目标复核

命令：`cd web && PLAYWRIGHT_PORT=5278 npx playwright test tests/round8-data.spec.ts -g '20 second PR request'`

在修正测试为“正在进行”首行的明确 Agent 任务摘要后，摘要和可见性断言通过；随后原有 `<1000ms` 墙钟断言失败，读数 **1173ms**。该次运行是源码 Vite 测试服务器；没有调整阈值、跳过测试或修改后端。完整测试日志 `playwright-restored.log` 早于这项断言修正，应结合本记录阅读。最终组合测试与构建产物性能对照由根任务执行。
