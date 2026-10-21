# 第八轮 PR 列表读取提速验证

真实部署验收尚未执行。本记录使用隔离的测试数据库和假的 GitHub HTTP 服务；没有读取真实凭证，也没有操作测试仓库以外的 Issue 或 PR。

## 原因与改法

旧实现对历史 PR 串行请求 GitHub，并在网络请求期间持有全局缓存锁。现在用迁移 16 的 `pr_snapshots` 持久化标题、状态、合并标记、关闭时间、URL、head 和读取门禁结果。首次按分页批量读取开放/关闭列表；已知终态直接读数据库，reopen webhook 可恢复开放状态。

开放 PR 先检查本地即可确认的草稿、验证缺失等阻碍；确实需要判断的 PR 最多并行检查 4 个，同一 PR 的并发成功和失败都共享一次请求。实际 HTTP 读取并行收集门禁输入，与原实时路径共用判断函数。合并仍走实时 head 检查，原 409 规则保持。

列表过期后立即返回旧结果并后台刷新，有变化时走现有 `swarmjet_events` 发 PR 事件；事件从持久化结果水合，不重复请求 GitHub。webhook 同步关闭/重开状态，热列表也同步更新。读取开始时间用于保护在途结果，避免同一秒内的新 webhook head 被旧请求覆盖；推送采用持久化的最终结果。

## 可选新增字段和迁移

- 列表整体 `stale: true`：当前返回上次结果，后台正在刷新。
- 单项 `unavailable: true`：该 PR 状态暂时无法获取，其余 PR 正常返回；原因显示“状态暂时无法获取”。
- 迁移 16 新建 `pr_snapshots`，可重复执行。原 `open/all` 含义不变，`all` 仍只包括最近 7 天关闭/合并的 PR。
- 用户明确批准对 `internal/router/event.go`、`router.go` 做最小 webhook 状态与时间传递，不改变派工、验证和合并逻辑。

## 测量

`TestPRListColdAndWarmWithDelayedGitHub` 创建 15 个历史 PR、3 个开放 PR，数据库没有预热记录；假的 GitHub 每一次 HTTP 请求都延迟 1 秒。开放 PR 非草稿，使用内置门禁，要求 CI、不要求独立验证，因此 3 个都需要完整读取检查。

|读取|主代理复测耗时|GitHub 请求数|
|---|---:|---:|
|冷读|2.017745946 秒|17|
|热读|1.62588 毫秒|新增 0|

冷读的 17 次包括开放列表 1 次、关闭列表 1 次、3 个开放 PR 各 5 次并行只读请求。测试断言冷读 ≤3 秒、热读 ≤300 毫秒。GitHub App 签发凭证和仓库解析在假服务之外；这些数据不能代替升级后的真实部署冷、热验收，也没有把它们直接当成第七轮 21 秒的实机对照。

```sh
TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:55450/postgres?sslmode=disable' go test -count=1 -run TestPRListColdAndWarmWithDelayedGitHub -v ./internal/api
TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:55450/postgres?sslmode=disable' go test -race -p 1 ./...
```

主代理在最终代码 `dee940d` 上真实运行以上命令，均通过。原始输出在 [性能日志](round8-pr-list/performance.log)、[全量 race 日志](round8-pr-list/go-race.log)。本次没有遇到已知 ACP 偶发失败。

新增回归涵盖终态零网络、开放/关闭分页、两项目网络并发、同 PR 并发成功与失败合并、过期旧结果与变化事件、单项失败隔离、webhook 热缓存/事件水合、重复迁移、同秒 head 顺序、目标分支变化、GitHub `head_sha` 解析以及可选 CI 的既有语义。详见 [顺序回归输出](round8-pr-list/ordering.log)。
