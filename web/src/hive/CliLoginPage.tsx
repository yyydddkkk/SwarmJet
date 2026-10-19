// CLI 登录 (#/cli-login): the OpenCode login terminal moved into `serve`
// (round 3, task E). This page only reads GET /api/setup/status, then mounts the
// terminal when Cell and the login service are both available.
import { hiveAPI } from "./api";
import CliTerminal from "./CliTerminal";
import { BeeLoader, Callout, ErrorNotice, Panel, StatusGrid, StatusItem } from "./components";
import { Icon } from "./icons";
import { useResource } from "./use-resource";

export default function CliLoginPage() {
  const status = useResource(signal => hiveAPI.getSetupStatus(signal), []);

  return (
    <div className="hg-page">
      <header className="hg-page-head">
        <div>
          <h1>CLI 登录</h1>
          <p className="hg-page-sub">
            在这里登录一次 OpenCode，新的 Cell 会自动使用登录结果，不需要再配
            <code>OPENCODE_AUTH_CONTENT</code>。
          </p>
        </div>
        <button className="hg-btn" type="button" disabled={status.loading} onClick={status.reload}>
          <Icon name="refresh" size={16} />
          {status.loading ? "正在刷新…" : "刷新状态"}
        </button>
      </header>

      {status.loading && !status.data ? (
        <BeeLoader label="正在读取登录状态…" />
      ) : status.error && !status.data ? (
        <ErrorNotice message={status.error} onRetry={status.reload} auth={status.errorStatus === 401} />
      ) : status.data ? (
        <Panel
          title="OpenCode 登录"
          subtitle="登录结果保存在平台数据卷中；Forager 每次启动时读取最新结果。"
        >
          <StatusGrid>
            <StatusItem label="Cell">
              {status.data.cells.enabled ? "已启用" : "未启用"}
            </StatusItem>
            <StatusItem label="登录终端">
              {status.data.cliLogin.available ? "可用" : "不可用"}
            </StatusItem>
            <StatusItem label="OpenCode">
              {status.data.cliLogin.loggedIn ? (
                <span className="hg-tone-mint">已登录</span>
              ) : (
                <span className="hg-tone-amber">未登录</span>
              )}
            </StatusItem>
          </StatusGrid>

          {!status.data.cliLogin.available ? (
            !status.data.cells.enabled ? (
              <Callout tone="amber" title="Cell 未启用，登录终端不可用">
                <p>
                  CLI 登录需要 Docker 与 Cell 运行环境。用 <code>compose.cells.yaml</code> 叠加启动服务并挂载
                  Docker socket 后，刷新本页即可使用。只部署控制台时，可以继续用{" "}
                  <code>OPENCODE_AUTH_CONTENT</code> 环境变量提供登录内容。
                </p>
              </Callout>
            ) : (
              <Callout tone="coral" title="登录服务未就绪">
                <p>
                  Cell 已启用，但 <code>serve</code> 没有可用的 CLI 登录处理器。确认{" "}
                  <code>/api/runtime/*</code> 已挂载，并检查 Docker 与数据目录权限后重试。
                </p>
              </Callout>
            )
          ) : (
            <>
              {!status.data.cells.enabled ? (
                <Callout tone="neutral" title="Cell 未启用">
                  <p>
                    登录终端仍然可用，登录结果会保存下来；以后启用 Cell 时，新的 Forager 会自动使用它。
                  </p>
                </Callout>
              ) : null}
              <CliTerminal available={status.data.cliLogin.available} />
            </>
          )}
        </Panel>
      ) : null}
    </div>
  );
}
