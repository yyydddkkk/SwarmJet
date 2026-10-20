# 第七轮集成界面截图

所有画面使用可注入 mock/假 SSE 和普通 HTTP fixture，不包含真实凭证，也不代表真实部署验收。

- `brief.png`：简报。
- `bright-classic.png`、`paper-classic.png`、`night-classic.png`、`bright-simple.png`：三个主题及简洁风格。
- `project-panel.png`、`platform-settings.png`、`list.png`、`login.png`：项目、平台、列表与登录入口。
- `brief-axe.json`、`list-axe.json`、`login-axe.json`：最终无障碍检查，violation 数组均为空。
- `performance.json`：300 Agent、50 PR 的真实 Chromium 绘制回调计时，不含浏览器合成；两种镜头空闲时均停止重绘。

完整命令、修复、已批准的缺口及验收边界见 [集成记录](../round7-integration.md)。
