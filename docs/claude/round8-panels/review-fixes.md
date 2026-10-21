# 面板复审返修（二）

本记录只针对 D 的四处复审问题；最终组合的 14 页双主题截图由集成分支统一重拍。先前并排图中项目花费、账簿、账号的旧样式不可当作这些返修的最终视觉证据。

| 修复 | 代码结果 | 针对性验证 |
|---|---|---|
| 项目切页滚动 | `PanelsSlot.tsx` 只在总览/高级方法论/指定版本/设置/花费等视图身份变化时把左侧面板滚回顶部；高级页内版本选择同样回顶部。相同视图的异步保存与资源更新不触发重置。 | 从总览底部进入高级页与第 2 版，`scrollTop=0`、版本控件在视口；设置表单滚动后保存仍停在原区段。 |
| 项目花费 | `UsagePanel.tsx` 把原生绿色 meter 改为 `--hair` 细轨和 `--accent` 填充；保留有名称和实际值的 meter 语义。排行仍是有序列表，仅显示一套编号。Agent 显示短编号，可复制完整 ID；主 Agent 使用中文名，点击进入主 Agent 详情。 | 检查轨/填充色、唯一编号、短名/无原始标识、两个完整 ID 的剪贴板内容与主 Agent 跳转，axe 实际运行且 0 violations。 |
| 平台账簿 | `ObjectPanel.tsx` 的 Ledger 使用参考附录的项目细线行，17px、400 body 项目名（按设计稿 `#ap-body .row` 最终核对）、金额靠右，明确显示接口真实提供的“今天/累计”；无本周走势或虚构数值。 | 检查行边框/字体/对齐、两种金额及实际 today/all 请求，无项目大标题卡片。 |
| 平台账号 | `PanelsSlot.tsx` 的 Account 使用参考说明、管理员细线行、斜体退出；接口没有本次登录时间，因此不显示这一行。 | 检查说明、管理员行边框/字号、退出样式以及缺失登录时间字段。 |

验证命令：`cd web && PLAYWRIGHT_PORT=5277 npx playwright test tests/panels.spec.ts -g 'project view changes start|saving project settings keeps|usage ranking uses quiet|platform ledger uses|platform account keeps' --workers=1`：5/5 通过；`npm run build` 通过。未重跑全套以免覆盖主代理正在执行的 145 项集成结果。
