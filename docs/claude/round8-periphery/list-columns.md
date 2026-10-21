# 列表列宽回归

复现：原列表使用 `table-layout: fixed`，首行是 `colSpan=4` 的项目分组且没有 `colgroup`。在 1440×900 的真实规模 mock 下，Playwright 实测第一项图标单元格 **191px**，标题列同样被等分，长摘要只露开头。

修复：保留固定表格布局与单行省略，明确四列为图标 40px、摘要占剩余宽、状态 150px、费用 90px；375px 时状态和费用改为 88px、58px。没有改数据层或手机端的 `nowrap` 规则。

命令：`cd web && PLAYWRIGHT_PORT=5278 npx playwright test tests/round8-periphery.spec.ts -g '列表图标与状态列保持设计宽度'`。结果 1/1 通过，实测列宽：

| 视口 | 图标 | 摘要 | 状态 | 费用 |
|---:|---:|---:|---:|---:|
| 1440px | 40px | 484px | 150px | 90px |
| 375px | 40px | 129px | 88px | 58px |

测试同时断言列表自身不超过视口宽度，搜索 PR 后可点击该行并打开详情。修正后的[明亮并排图](compare-bright-list.png)和[夜空并排图](compare-night-list.png)显示标题列恢复为主宽。
