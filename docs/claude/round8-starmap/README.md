# 第八轮 C：星图证据

## 同数据截图

[截图测试](../../../web/tests/starmap-visual.spec.ts)在 1440×900 的 Chromium 中调用一次 `round8Project()`，将同一个 `ProjectSnapshot` 注入实现的 `ProjectStore`，并把同一次 `deriveProject(snapshot)` 的节点、历史、CI、决策传给设计稿。参考页先清空内置的三个项目和对象，再用这些实体重建一个同名项目；被聚合的 PR/Issue 也传入参考页，由设计稿 `layoutSys` 聚合。计数断言覆盖节点、历史和两个小簇；[fixture-manifest.json](fixture-manifest.json)记录结果。镜头都进入对应的全部项目层或项目层。画布截图由 `canvas.toDataURL()` 得到，所以不会混进设计稿外围的简报和图例。设计稿本身不会读取 API，这个适配只在测试页执行，没有修改设计稿文件。

| 风格 / 主题 | 全部项目 | 项目 |
|---|---|---|
| 经典 / 明亮 | [并排](compare-bright-classic-all.jpg) | [并排](compare-bright-classic-project.jpg) |
| 经典 / 纸本 | [并排](compare-paper-classic-all.jpg) | [并排](compare-paper-classic-project.jpg) |
| 经典 / 夜空 | [并排](compare-night-classic-all.jpg) | [并排](compare-night-classic-project.jpg) |
| 简洁 / 明亮 | [并排](compare-bright-simple-all.jpg) | [并排](compare-bright-simple-project.jpg) |
| 简洁 / 纸本 | [并排](compare-paper-simple-all.jpg) | [并排](compare-paper-simple-project.jpg) |
| 简洁 / 夜空 | [并排](compare-night-simple-all.jpg) | [并排](compare-night-simple-project.jpg) |

小卡片和 ⊙ 面板使用包含 DOM 的整页截图，等待进出过渡完成后截取：

| 主题 | PR 小簇 | 历史星 | ⊙ 面板 |
|---|---|---|---|
| 明亮 | [并排](compare-bright-cluster-dom.jpg) | [并排](compare-bright-history-dom.jpg) | [并排](compare-bright-observer-dom.jpg) |
| 夜空 | [并排](compare-night-cluster-dom.jpg) | [并排](compare-night-history-dom.jpg) | [并排](compare-night-observer-dom.jpg) |

可见差异：

1. 环境动画在正常模式持续运行，设计稿与实现的采样时刻不能锁相；彗星、PR 和 verifier 的轨道相位不同。两边均在镜头缓动结束后截图。
2. 实现的标签直接使用数据层 `mapNodes` 摘要与优先级避让；设计稿的样例标签通过其自身 `short` 函数生成，部分 ID、Issue 文字和可见标签位置不同。实现遵守本轮真实数据的一行摘要规则。
3. 全部项目层实现写出“15 件事等你”，设计稿只在项目名旁放强调色点；这里依照本轮“项目名和 N 件事等你”的明确规则。
4. 两页背景装饰星的随机起点不同，但都使用设计稿的大小、亮度、十字比例和视差；真实历史星由同一数据层坐标生成，并剔除项目范围内的装饰星。
5. DOM 并排图中，左侧设计稿还显示其外围页眉、工具栏、项目标题；右侧测试只挂载 C 的 CanvasSlot，所以没有 E 负责的外围。小簇卡片在相同八项内容下采用设计稿的边框、字体、间距与淡入。⊙ 参考卡片只有设计稿写死的 PR #8，实现在同一 fixture 的 15 个真实决策中给出可逐项进入详情的滚动列表，因此卡片更高。这属于设计稿单项示例与真实数据量的差异，功能没有隐藏其余决策。

历史结束时间取 Agent `updatedAt` 近似，点击历史星的小卡片明确标注这个口径。

## 验证与性能

`npm ci`、`npm run build` 通过；完整 Web Playwright 命令为 `PLAYWRIGHT_PORT=5276 npx playwright test --reporter=line --workers=1`，最终 **110 passed**，见 [full-test-log.txt](full-test-log.txt)。默认两 worker 的一轮曾有 B 的 1 秒冷启动时间断言在即时编译负载下测得 1110 ms；单 worker 全套的同项测得 617 ms，未修改该断言或数据层。专项回归包含完成飞出、小簇点击、历史星悬停全文与点击、全部项目点击进入、运行中切换减少动态效果、页面隐藏停止、合并动画、两种来源的失败 CI 点击、Issue 拖拽、镜头和时间轴兼容。小簇、历史星和 ⊙ 卡片另测进退场中间帧、关闭后不可聚焦，以及明亮/夜空的 axe 全部违规项为空。实际侧板、列表和平台设置覆盖区的画布文字按 DOM 矩形屏蔽；减少动态效果下 DOM 延后挂载、开关卡片会补绘一次，随后静止。

性能测试使用明亮/经典，1440×1000，DPR 1，Chromium 153；**未开启减少动态效果**。第八轮常规 `round8Project()` 有 48 个 Agent、35 颗历史星，正常项目层约 30 fps；压力 fixture 有 300 个活跃 Agent、900 个历史 Agent、16 个开着的 PR、13 个 Issue、900 条消息和 3000 字以上任务提示词，超过 600 个图形时保持长彗星尾和其余设计动效，但将环境绘制降至约 20 fps。帧耗时从绘制回调开始的镜头/位置更新计到标签避让、覆盖区 DOM 矩形采集、卡片图形、状态写入及下一帧调度之后；跳过的 `requestAnimationFrame` 回调不计入绘制帧。事件过程收集压力 fixture 的帧耗时；随后静置 2.2 秒，取 5 秒空闲窗口的 CDP `SystemInfo.getProcessInfo` renderer `cpuTime` 差除以实际墙钟时长，换算为**单核百分比**。常规 fixture 在镜头稳定后同样取 5 秒。采样时单 worker、单页面、稳定的一个 renderer PID。CPU 包含该 renderer 的合成/光栅等开销，因此不能由 JavaScript 帧耗时乘帧率推算。

| 场景 / 镜头 | 帧耗时 p50 | p95 | CPU / 单核 | 5 秒帧数 | 原始数据 |
|---|---:|---:|---:|---:|---|
| 常规 / 项目 | 0.9 ms | 1.2 ms | 26.0% | 151（约 30 fps） | [JSON](performance-round8.json) |
| 300/900 / 全部项目 | 1.4 ms | 2.6 ms | 23.4% | 100（约 20 fps） | [JSON](performance-all.json) |
| 300/900 / 项目 | 3.3 ms | 5.8 ms | 61.4% | 100（约 20 fps） | [JSON](performance-project.json) |

这次测试不覆盖真实后端部署、D/E 合入后的整页交互，也不把设计稿的固定 PR #8 硬编码成产品决策。
