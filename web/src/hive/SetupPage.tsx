// 接入 (#/setup): create the GitHub App, see the webhook tunnel, and mint tokens
// for local agents. The backend is being built in parallel; every shape here
// follows docs/claude/round3-prompts.md "公共契约" section 5.
import { useId, useState } from "react";
import { hiveAPI } from "./api";
import { formatTimestamp } from "./format";
import type { CreatedToken, SetupStatus, TokenScope, TokenSummary } from "./types";
import {
  BeeLoader,
  Callout,
  ConfirmDialog,
  CopyButton,
  CopyField,
  Dialog,
  ErrorNotice,
  Panel,
  StatusGrid,
  StatusItem,
} from "./components";
import { GithubMark, Icon } from "./icons";
import { useResource } from "./use-resource";

export default function SetupPage({ query }: { query: URLSearchParams }) {
  const status = useResource(signal => hiveAPI.getSetupStatus(signal), []);
  const tokens = useResource(signal => hiveAPI.listTokens(signal), []);
  const [createOpen, setCreateOpen] = useState(false);
  const [revoking, setRevoking] = useState<TokenSummary | null>(null);
  const [revokeError, setRevokeError] = useState("");
  const [revokeBusy, setRevokeBusy] = useState(false);
  const [notice, setNotice] = useState("");

  const connected = query.get("github") === "connected";
  const callbackError = query.get("error") ?? "";

  const revoke = () => {
    if (!revoking) return;
    setRevokeBusy(true);
    setRevokeError("");
    hiveAPI
      .revokeToken(revoking.id)
      .then(() => {
        setNotice(`已吊销连接「${revoking.name}」。`);
        setRevoking(null);
        tokens.reload();
      })
      .catch(cause => setRevokeError(cause instanceof Error ? cause.message : String(cause)))
      .finally(() => setRevokeBusy(false));
  };

  return (
    <div className="hg-page">
      <header className="hg-page-head">
        <div>
          <h1>接入</h1>
          <p className="hg-page-sub">
            先创建 GitHub App，再看 webhook 隧道，最后生成本地 Agent 的连接配置。
          </p>
        </div>
        <button
          className="hg-btn"
          type="button"
          disabled={status.loading}
          onClick={() => {
            status.reload();
            tokens.reload();
          }}
        >
          <Icon name="refresh" size={16} />
          {status.loading ? "正在刷新…" : "刷新状态"}
        </button>
      </header>

      {connected ? (
        <div className="hg-flash hg-flash-success" role="status">
          <Icon name="check" size={16} />
          <span>GitHub App 已创建并保存。下一步把它安装到仓库，然后就能新建 Hive 了。</span>
        </div>
      ) : null}
      {callbackError ? (
        <div className="hg-flash hg-flash-error" role="alert">
          <Icon name="alert" size={16} />
          <span>GitHub App 连接失败：{callbackError}。请重新点击“一键创建 GitHub App”。</span>
        </div>
      ) : null}
      {notice ? (
        <div className="hg-flash hg-flash-success" role="status">
          <Icon name="check" size={16} />
          <span>{notice}</span>
        </div>
      ) : null}

      {status.loading && !status.data ? (
        <BeeLoader label="正在读取接入状态…" />
      ) : status.error && !status.data ? (
        <ErrorNotice message={status.error} onRetry={status.reload} auth={status.errorStatus === 401} />
      ) : status.data ? (
        <>
          <GitHubPanel status={status.data} onChanged={status.reload} />
          <TunnelPanel status={status.data} />
          <Panel
            title="连接本地 Agent"
            subtitle="为 Claude Code、Codex 生成 MCP 连接配置。明文 Token 只在创建后显示一次。"
            extra={
              <button className="hg-btn hg-btn-primary" type="button" onClick={() => setCreateOpen(true)}>
                <Icon name="plus" size={16} />
                新建连接
              </button>
            }
          >
            {tokens.loading && !tokens.data ? (
              <p className="hg-muted">正在读取连接…</p>
            ) : tokens.error && !tokens.data ? (
              <ErrorNotice message={tokens.error} onRetry={tokens.reload} />
            ) : (tokens.data?.tokens ?? []).length === 0 ? (
              <p className="hg-muted">
                还没有连接。新建一个后，把生成的配置贴到本地 Agent 里即可通过 MCP 连上平台。
              </p>
            ) : (
              <ul className="hg-token-list">
                {(tokens.data?.tokens ?? []).map(token => (
                  <li key={token.id} className="hg-token-row">
                    <div className="hg-token-main">
                      <strong>{token.name}</strong>
                      <span className="hg-token-meta">
                        {token.scope === "admin" ? "管理员作用域" : "默认作用域"} ·{" "}
                        {formatTimestamp(token.createdAt)}
                      </span>
                    </div>
                    <button
                      className="hg-btn hg-btn-danger hg-btn-small"
                      type="button"
                      aria-label={`吊销连接 ${token.name}`}
                      onClick={() => {
                        setRevokeError("");
                        setRevoking(token);
                      }}
                    >
                      吊销
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </>
      ) : null}

      {createOpen ? (
        <CreateTokenDialog
          onClose={() => setCreateOpen(false)}
          onCreated={() => tokens.reload()}
        />
      ) : null}

      {revoking ? (
        <ConfirmDialog
          title={`吊销连接「${revoking.name}」？`}
          confirmLabel="吊销"
          danger
          busy={revokeBusy}
          error={revokeError}
          onConfirm={revoke}
          onCancel={() => setRevoking(null)}
        >
          <p>用这个 Token 的本地 Agent 会立即失去访问权限，需要重新创建连接。</p>
        </ConfirmDialog>
      ) : null}
    </div>
  );
}

function GitHubPanel({ status, onChanged }: { status: SetupStatus; onChanged: () => void }) {
  const [owner, setOwner] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const ownerId = useId();
  const github = status.github;

  const startManifest = async () => {
    setBusy(true);
    setError("");
    try {
      const result = await hiveAPI.createGithubManifest(owner.trim());
      // GitHub App Manifest flow: a cross-origin POST of the manifest JSON from a
      // hidden form. React cannot submit this itself, so build it by hand.
      const form = document.createElement("form");
      form.method = "POST";
      form.action = result.action;
      form.target = "_self";
      form.style.display = "none";
      const field = document.createElement("input");
      field.type = "hidden";
      field.name = "manifest";
      field.value = JSON.stringify(result.manifest);
      form.appendChild(field);
      document.body.appendChild(form);
      form.submit();
      window.setTimeout(() => form.remove(), 30_000);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel
      title={
        <span className="hg-panel-title">
          <GithubMark size={16} />
          GitHub
        </span>
      }
      subtitle="一键创建 GitHub App，凭证加密保存到数据库；也可以继续用环境变量配置。"
      extra={
        github.configured ? (
          <span className="hg-badge hg-tone-mint">
            <span className="hg-dot hg-tone-mint" aria-hidden="true" />
            已配置
          </span>
        ) : (
          <span className="hg-badge hg-tone-amber">
            <span className="hg-dot hg-tone-amber" aria-hidden="true" />
            未配置
          </span>
        )
      }
    >
      {github.configured ? (
        <>
          <StatusGrid>
            <StatusItem label="App 名称">
              <code>{github.appSlug || `App ${github.appId}`}</code>
            </StatusItem>
            <StatusItem label="App ID">{github.appId || "—"}</StatusItem>
            <StatusItem label="来源">
              {github.source === "env" ? "由环境变量配置" : "已保存在数据库中"}
            </StatusItem>
            <StatusItem label="已安装">{github.installations} 个账号或仓库</StatusItem>
          </StatusGrid>
          <div className="hg-link-row">
            {github.htmlUrl ? (
              <a className="hg-btn" href={github.htmlUrl} target="_blank" rel="noreferrer">
                <Icon name="external" size={16} />
                在 GitHub 上查看
              </a>
            ) : null}
            {github.installUrl ? (
              <a className="hg-btn hg-btn-primary" href={github.installUrl} target="_blank" rel="noreferrer">
                <Icon name="plus" size={16} />
                安装到仓库
              </a>
            ) : null}
            <button className="hg-btn" type="button" onClick={onChanged}>
              <Icon name="refresh" size={16} />
              重新读取
            </button>
          </div>
          <p className="hg-muted">
            安装完成后回到这里点“重新读取”，新建 Hive 时就能从已安装仓库里选择。
          </p>
        </>
      ) : (
        <>
          <p>
            还没有 GitHub App。点击下面的按钮会生成一份 App 清单并跳转到 GitHub 确认，
            确认后自动回到这里，无需手动填 App ID 和私钥。
          </p>
          <div className="hg-form-grid">
            <label className="hg-field" htmlFor={ownerId}>
              <span>组织名（可选）</span>
              <input
                id={ownerId}
                value={owner}
                onChange={event => setOwner(event.target.value)}
                placeholder="留空表示创建在个人账号下"
              />
            </label>
            <button className="hg-btn hg-btn-primary" type="button" disabled={busy} onClick={startManifest}>
              <GithubMark size={16} />
              {busy ? "正在生成清单…" : "一键创建 GitHub App"}
            </button>
          </div>
          {error ? (
            <p className="hg-form-error" role="alert">
              {error}
            </p>
          ) : null}
        </>
      )}
    </Panel>
  );
}

function TunnelPanel({ status }: { status: SetupStatus }) {
  const tunnel = status.tunnel;
  return (
    <Panel
      title="Webhook 隧道"
      subtitle="没有公网域名时，由 cloudflared 临时隧道接收 GitHub webhook，并自动同步地址。"
      extra={
        <span className={tunnel.enabled ? "hg-badge hg-tone-mint" : "hg-badge hg-tone-neutral"}>
          <span
            className={`hg-dot ${tunnel.enabled ? "hg-tone-mint" : "hg-tone-neutral"}`}
            aria-hidden="true"
          />
          {tunnel.enabled ? "已启用" : "未启用"}
        </span>
      }
    >
      {!tunnel.enabled ? (
        <Callout title="隧道未启用">
          <p>
            在安装脚本里选择启用隧道，或设置 <code>SWARMJET_TUNNEL_URL</code> 后重启平台。
            有公网域名时也可以直接把 webhook 指向控制台地址。
          </p>
        </Callout>
      ) : (
        <>
          <StatusGrid>
            <StatusItem label="公网地址">
              {tunnel.url ? (
                <a href={tunnel.url} target="_blank" rel="noreferrer">
                  {tunnel.url}
                </a>
              ) : (
                "等待隧道分配地址"
              )}
            </StatusItem>
            <StatusItem label="Webhook 同步">
              <span className={tunnel.webhookSynced ? "hg-tone-mint" : "hg-tone-amber"}>
                {tunnel.webhookSynced ? "已同步" : "未同步"}
              </span>
            </StatusItem>
            <StatusItem label="最近错误">
              {tunnel.lastError ? <span className="hg-tone-coral">{tunnel.lastError}</span> : "无"}
            </StatusItem>
          </StatusGrid>
          <p className="hg-muted">
            地址变化时平台会自动调用 GitHub 更新 webhook；同步失败会在上面显示原因。
          </p>
        </>
      )}
    </Panel>
  );
}

function CreateTokenDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const [created, setCreated] = useState<CreatedToken | null>(null);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<TokenScope>("default");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const nameId = useId();

  return (
    <Dialog title={created ? "连接已创建" : "新建连接"} onClose={onClose} wide={!!created}>
      {created ? (
        <div className="hg-created-connection">
          <Callout tone="amber" title="关闭后无法再次查看">
            下面的明文 Token 只显示这一次。请现在把它复制到本地 Agent 的配置里。
          </Callout>
          <div className="hg-token-plaintext">
            <span>Token 明文</span>
            <code data-testid="token-plaintext">{created.plaintext}</code>
            <CopyButton text={created.plaintext} ariaLabel="复制 Token 明文" />
          </div>
          <p className="hg-muted">
            作用域：{created.token.scope === "admin" ? "管理员" : "默认"} · 名称：
            {created.token.name}
          </p>
          <CopyField label="MCP 地址" text={created.connect.mcpUrl} />
          <CopyField label="Claude Code" text={created.connect.claudeCode} />
          <CopyField label="Codex 配置" text={created.connect.codex} />
          <CopyField
            label="JSON 配置"
            text={JSON.stringify(created.connect.json, null, 2)}
          />
          <div className="hg-dialog-actions">
            <button className="hg-btn hg-btn-primary" type="button" onClick={onClose}>
              我已保存，关闭
            </button>
          </div>
        </div>
      ) : (
        <form
          className="hg-token-form"
          onSubmit={event => {
            event.preventDefault();
            if (!name.trim()) {
              setError("给这个连接起个名字，方便以后吊销。");
              return;
            }
            setBusy(true);
            setError("");
            hiveAPI
              .createToken({ name: name.trim(), scope })
              .then(result => {
                setCreated(result);
                onCreated();
              })
              .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
              .finally(() => setBusy(false));
          }}
        >
          <label className="hg-field" htmlFor={nameId}>
            <span>名称</span>
            <input
              id={nameId}
              value={name}
              onChange={event => setName(event.target.value)}
              placeholder="例如：工作笔记本"
              required
            />
          </label>
          <fieldset className="hg-field hg-fieldset">
            <legend>作用域</legend>
            <label className="hg-radio">
              <input
                type="radio"
                name="scope"
                value="default"
                checked={scope === "default"}
                onChange={() => setScope("default")}
              />
              <span>
                <strong>默认</strong>
                <small>调用 MCP 工具，适合 Claude Code、Codex</small>
              </span>
            </label>
            <label className="hg-radio">
              <input
                type="radio"
                name="scope"
                value="admin"
                checked={scope === "admin"}
                onChange={() => setScope("admin")}
              />
              <span>
                <strong>管理员</strong>
                <small>包含管理类工具，只在确实需要时使用</small>
              </span>
            </label>
          </fieldset>
          <p className="hg-muted">
            创建后会显示一次明文 Token，以及 Claude Code、Codex、JSON 三种配置。
          </p>
          {error ? (
            <p className="hg-form-error" role="alert">
              {error}
            </p>
          ) : null}
          <div className="hg-dialog-actions">
            <button className="hg-btn" type="button" onClick={onClose} disabled={busy}>
              取消
            </button>
            <button className="hg-btn hg-btn-primary" type="submit" disabled={busy}>
              {busy ? "正在创建…" : "创建连接"}
            </button>
          </div>
        </form>
      )}
    </Dialog>
  );
}
