# 外围字号重置回归

完整应用壳的 `stars.css` 基础规则 `.stars-app button/input/select { font: inherit }` 在 `periphery.css` 后导入，并压过了设计稿移植样式。修复前在 1440×900 真实规模 mock 中，简报行计算值为 **15px**（设计 17px）；列表搜索框也被重置成 15px 正体（设计 20px 斜体）。

仅把基础重置选择器改为 `:where(.stars-app) button/input/select`，保留其继承行为，让组件自身规则生效；没有逐项加 `!important`、改设计字号或调整间距。回归命令：`cd web && PLAYWRIGHT_PORT=5278 npx playwright test tests/round8-periphery.spec.ts -g '真实应用壳按设计计算外围字号'`，1/1 通过。

实测：1440px 简报行 **17px**、列表搜索 **20px italic**、工具栏 **16px**；375px 工具栏 **14px**，列表无横向溢出。1440×900 简报从顶部未滚动时，可见区域下边界 y=788px，“需要留意”最后一行下边界也是 y=788px；需要你与留意项完整落在可见区内，但没有额外垂直余量。列宽回归仍为 1440px 的 40/484/150/90px 和 375px 的 40/129/88/58px。新的明亮、夜空并排截图在本目录 `compare-*.png`。
