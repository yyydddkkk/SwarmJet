# 第七轮任务 D：外围体验验收

日期：2026-10-20。基线为任务 A 审查通过的 ce3bd86。修改仅涉及外围槽、独立组件/CSS、前端测试与本验收记录。

## 实现与数据口径

- 简报默认展开，四组内容来自共享 ProjectStore 的快照与派生状态，以及真实 setup/models/today usage。工作中最多三条，其余进入列表。失败、长时间空闲、模型服务不可连接与无可用凭证分开表述；configured 仅作为已保存状态，不推断凭证验证结果。
- 昨晚 Agent 完成数按浏览器本地时间昨晚 18:00 至今天 06:00、终态 completed 的 updatedAt 统计，界面标明该口径。PR 无 mergedAt、用量无日期明细，昨晚合并数与花费明确显示「暂时无法获取」，另提供近期已合并 PR 入口与真实 today 花费。
- 列表提供搜索、项目/主 Agent/Agent/PR/事件详情入口、插话、取消、带确认的合并、问题答复和 Issue 派活；排队显示任务、角色、位置，具体等待原因缺失时明确不可获取。停用项目排最后，有待决定事项优先。
- 合并使用 props.stores 的共享 store.mergePR，确认时冻结所见 headSha；409 刷新并提示重新查看，清除旧确认；排队结果明确提示等待。答复发送 kind=reply、原 chainId、原提问者地址。仅未认领 issue 且主 Agent 已启用时提供派活。
- 命令严格匹配项目名、PR #n、#n 和平台词；同号时 PR #n 指向 PR，#n 指向 Issue 或关联 Agent。长句原样发给主 Agent，失败保留草稿。
- D 是唯一全局快捷键/Esc 注册者；冒泡并尊重 defaultPrevented，忽略修饰键、组合输入及编辑控件中的字母命令。Esc 一次退出一层，列表搜索框也能退出。新建项目弹层管理焦点进入、Tab 循环与归还，忙时禁用表单。
- 首次三步分别依据 GitHub configured + installations、setup.models.available + 模型目录服务状态、实际项目列表点亮。平台关闭后重新读取；setup/models/usage 独立处理失败并可重试。
- 新建项目复用旧创建表单的 provider 已配置过滤、模型选择和正整数并发校验逻辑，选择仓库后通过数据层 createHive；reloadProjects 并 selectObject 打开主 Agent。B 的全部项目空白点击通过 window CustomEvent('swarmjet:create-project') 接入；可见「创建项目」按钮是键盘入口。B 独占观测者与时间轴。
- THEMES 自动生成显示选项；主题/风格/经纬线和简报偏好均仅存界面设置，localStorage 读写 try/catch。图例首次进入自动打开约七秒；减少动态、可见焦点、手机宽度答复与合并均保留。

## 验证

执行 `cd web && npm ci && npm run build`。执行 `PLAYWRIGHT_PORT=5277 npx playwright test`，最终结果：102/102 通过（约 1.2 分钟）。配置默认端口仍为 5274，use.baseURL 与 webServer 同取 PLAYWRIGHT_PORT。

新增 `web/tests/periphery.spec.ts` 覆盖十五项：简报四组与快照、列表搜索/跳转/插话/取消/派活、全部快捷键及输入隔离、精确命令与长句、同号 Issue/PR、首次使用组合、项目创建、登录失败、SSE 断线、手机合并与 reply 链、409、焦点与空白事件、独立用量故障、queue/非 Issue 事件、偏好与图例自动关闭、已有项目第三步。

登录、简报、列表均通过 axe-core 4.14.0 检查，严重/critical 为零。证据在 [round7-d-evidence](round7-d-evidence/)：每个页面有截图和 `*-axe.json`（检查时间、引擎版本、通过规则数量、实际 violation 数组）。

`web/tests/stars-http-mock.ts` 补齐默认 StarsApp 的 PR 列表、PR 详情/合并、queue、signals、方法论事件 HTTP 接口。正常连接的 FakeEventSource 保持打开，可手动 push/ready/disconnect；正常截图不会通过隐藏错误来伪装成功。原有 legacy mock/用例逻辑未修改。

新增依赖仅开发依赖 `@axe-core/playwright`，用于自动无障碍检查；没有新增运行时依赖。

## 集成边界

- 当前分支以 A 为基础，C 的真实详情/项目/平台面板与 B 的完整画布/时间轴由各任务集成。D 通过共享 ui 动作对接它们，未修改 StarsApp、ui、theme、slots、数据层或后端。
- 集成负责人将删除 StarsApp 的 legacy 创建链接，并使 listHives 首轮未完成/失败时先显示读取/错误；成功返回空数组后才挂首次使用，避免把尚未返回当成没有项目。
- 真实部署数据、C 面板与 B 画布的联合视觉验收由集成 PR 执行；本分支没有合并 PR、推 tag 或发布 Release。
