# 第八轮外围界面对照

每张 `compare-*.png` 左侧为 `context/design/stars/index.html`，右侧为第八轮真实规模 mock。每侧截图 1440×900，Chromium，减少动态效果开启，分别采用明亮和夜空主题。真实规模 mock 有 48 个 Agent、19 个 PR（16 个开着）、13 个 Issue、900 条消息；第一条任务超过 3000 字。截图没有凭证。`app-*.png` 和 `design-*.png` 是未拼接原图。

| 界面 | 明亮 | 夜空 | 对照差异 |
|---|---|---|---|
| 简报 | [并排图](compare-bright-brief.png) | [并排图](compare-night-brief.png) | 设计稿展示三个项目及固定示例；实现使用一个高密度项目和真实派生数据，因而决定数、文案与星图不同。结构、字号、左上位置、行间距、细线和进出位移沿用设计稿。实现增加“还有 N 个”与最近 24 小时失败项的“知道了”；右侧状态保持一行。 |
| 列表 | [并排图](compare-bright-list.png) | [并排图](compare-night-list.png) | 设计稿按项目列出当前对象；实现添加任务要求的“活跃 / 今天 / 历史”三项筛选，历史对象可搜索。数据内容不同。列表背景改为不透底的既有 `--paper`，避免下层文字透出；搜索焦点仍用设计稿的细线。 |
| 图例 | [并排图](compare-bright-legend.png) | [并排图](compare-night-legend.png) | 位置、尺寸、行距、图形定义照设计稿；实现按本轮需求增加 CI，故多一行。原稿的 h4 调整为视觉相同的 h2，让 axe 标题顺序通过。星图由另一任务实现，图形与其共享 `star-glyphs.ts`。 |
| 显示 | [并排图](compare-bright-display.png) | [并排图](compare-night-display.png) | 三行文字切换及位置尺寸与设计稿一致，主题项依次为明亮、纸本、夜空、跟随系统；没有原生选择框或复选框。背后星图和项目数因 mock 不同。 |
| 命令 | [并排图](compare-bright-command.png) | [并排图](compare-night-command.png) | 输入框、聚焦宽度、细线和底部提示沿用设计稿；实现额外显示可操作的“前往/发送”按钮。设计稿此处没有实际发送能力，实现保留后端发送与后续输入草稿。 |

设计稿没有登录与断线界面。实现分别从平台附录面板的细线框、斜体标题和字段，以及既有通知文本推导；单独证据：[明亮登录](app-bright-login.png)、[夜空登录](app-night-login.png)、[明亮断线](app-bright-disconnect.png)、[夜空断线](app-night-disconnect.png)。首次使用向导与创建项目同样采用附录面板的细线、斜体与既有颜色变量，设计稿没有可并排的对应画面。登录小标题最初沿用 `--ink3`，axe 实测明亮主题对比度仅 2.91:1；按用户批准的局部例外，仅把这段 15px 可读文字改用既有 `--ink2`，不改色表或几何样式。

本轮列表中的“今天”按对象更新时间筛选，“历史”包括所有已结束对象，包含今天结束的对象；画布的历史星结束时间按 Agent `updatedAt` 近似。费用只显示 API 已提供的 Agent `costUsd`，未填的对象留空。

验证：`cd web && npm ci && npm run build`，[构建日志](build.log)；`PLAYWRIGHT_PORT=5278 npx playwright test`，[完整日志](playwright.log)，93/93 通过。专项测试含 375px、快捷键、确认后共享 store 即时重算、同号 Issue/PR 命令消歧、发送期间保留新草稿，以及各外围界面完整 axe violations 检查。
