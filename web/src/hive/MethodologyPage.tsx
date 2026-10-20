// Hive 方法论: round-5 catalog installation and round-4 version management.
// The page renders the
// manifest as structured cards instead of raw YAML, exposes every file, keeps
// the append-only version history and modification log, diffs any two versions,
// and switches versions behind a confirmation.
import { useEffect, useMemo, useState } from "react";
import { hiveAPI } from "./api";
import type {
  EventRule,
  Methodology,
  MethodologyChange,
  MethodologyFile,
  MethodologyManifest,
  MethodologyRole,
  MethodologyTemplate,
  RolePermissions,
} from "./types";
import {
  BeeLoader,
  Callout,
  ConfirmDialog,
  EmptyState,
  ErrorNotice,
  Panel,
  StatusGrid,
  StatusItem,
} from "./components";
import { compactDiff, diffFiles, diffLines, diffStats } from "./diff";
import { formatDateTime, formatUsd, shortSha } from "./format";
import { Icon } from "./icons";
import { useResource } from "./use-resource";

export default function MethodologySection({ hiveId }: { hiveId: string }) {
  const [viewVersion, setViewVersion] = useState<number | undefined>(undefined);
  const current = useResource(
    signal => hiveAPI.getMethodology(hiveId, viewVersion, signal),
    [hiveId, viewVersion],
  );
  const versions = useResource(
    signal => hiveAPI.listMethodologyVersions(hiveId, signal),
    [hiveId],
  );
  const [notice, setNotice] = useState("");
  const [switching, setSwitching] = useState<Methodology | null>(null);
  const [switchBusy, setSwitchBusy] = useState(false);
  const [switchError, setSwitchError] = useState("");

  if (current.loading && !current.data) {
    return <BeeLoader label="正在读取方法论…" />;
  }
  const installed = () => {
    setViewVersion(undefined);
    setNotice("方法论已安装；新派出的 Forager 会使用这个版本。");
    current.reload();
    versions.reload();
  };

  if (current.errorStatus === 404 && viewVersion === undefined && !current.data) {
    return <MethodologyCatalog hiveId={hiveId} onInstalled={installed} />;
  }
  if (current.error && !current.data) {
    return (
      <ErrorNotice
        message={current.error}
        onRetry={current.reload}
        auth={current.errorStatus === 401}
        admin={current.errorStatus === 403}
      />
    );
  }
  if (!current.data) return null;

  // The platform's minimal fallback is not an installed methodology.
  const builtin = current.data.builtin === true;
  if (builtin) return <MethodologyCatalog hiveId={hiveId} onInstalled={installed} />;

  // The response is the version itself, flattened, plus its files.
  const methodology = current.data;
  const { files } = current.data;
  const versionList = versions.data?.versions ?? [];
  const changes = versions.data?.changes ?? [];
  const activeVersion =
    versions.data?.activeVersion ||
    versionList.find(item => item.active)?.version ||
    methodology.version;
  const versionLabel = builtin ? "内置方法论" : `v${methodology.version}`;

  const activate = () => {
    if (!switching) return;
    const version = switching.version;
    setSwitchBusy(true);
    setSwitchError("");
    hiveAPI
      .activateMethodology(hiveId, version)
      .then(() => {
        setNotice(`已切换到 v${version}；新的 Agent 会立刻使用这个版本。`);
        setViewVersion(version);
        setSwitching(null);
        current.reload();
        versions.reload();
      })
      .catch(cause => setSwitchError(cause instanceof Error ? cause.message : String(cause)))
      .finally(() => setSwitchBusy(false));
  };

  return (
    <div className="hg-methodology">
      {builtin ? <MethodologyCatalog hiveId={hiveId} onInstalled={installed} /> : null}
      {notice ? (
        <div className="hg-flash hg-flash-success" role="status">
          <Icon name="check" size={16} />
          <span>{notice}</span>
        </div>
      ) : null}

      <Panel
        title="方法论"
        subtitle="一个版本就是一组文件；改动会生成新版本，切换版本即回滚。manifest 是平台真正执行的部分。"
        extra={
          <label className="hg-inline-field">
            查看版本
            <select
              value={methodology.version}
              onChange={event => setViewVersion(Number(event.target.value))}
              aria-label="查看方法论版本"
            >
              {versionList.length ? (
                [...versionList]
                  .sort((left, right) => right.version - left.version)
                  .map(item => (
                    <option key={item.version} value={item.version}>
                      v{item.version}
                      {item.active ? "（当前生效）" : ""}
                    </option>
                  ))
              ) : (
                <option value={methodology.version}>{versionLabel}</option>
              )}
            </select>
          </label>
        }
      >
        <StatusGrid>
          <StatusItem label="版本">
            <span className="hg-version-badge" data-testid="methodology-version">
              {versionLabel}
              {builtin ? (
                <span className="hg-badge hg-tone-amber">平台默认</span>
              ) : methodology.active ? (
                <span className="hg-badge hg-tone-mint">当前生效</span>
              ) : (
                <span className="hg-badge hg-tone-neutral">历史版本</span>
              )}
            </span>
          </StatusItem>
          <StatusItem label="默认角色">
            <code>{methodology.manifest.defaultRole}</code>
          </StatusItem>
          <StatusItem label="摘要">
            <code title={methodology.digest}>{shortSha(methodology.digest)}</code>
          </StatusItem>
          <StatusItem label="修改人">{builtin ? "平台内置" : methodology.createdBy || "—"}</StatusItem>
          <StatusItem label="时间">{builtin ? "—" : formatDateTime(methodology.createdAt)}</StatusItem>
          <StatusItem label="来源">
            {methodology.source ? (
              <span className="hg-source">
                {methodology.source.url}@{methodology.source.ref}
                {methodology.source.sha ? ` · ${shortSha(methodology.source.sha)}` : ""}
                {methodology.source.path ? ` · ${methodology.source.path}` : ""}
              </span>
            ) : builtin ? (
              "这个 Hive 还没有发布过方法论，使用平台内置版本"
            ) : (
              "控制台或 MCP 直接发布"
            )}
          </StatusItem>
        </StatusGrid>
        {methodology.note ? <p className="hg-version-note">备注：{methodology.note}</p> : null}
      </Panel>

      <ManifestView manifest={methodology.manifest} />

      <FilesPanel files={files} />

      <VersionHistory
        versions={versionList}
        changes={changes}
        activeVersion={activeVersion}
        viewingVersion={methodology.version}
        onView={setViewVersion}
        onSwitch={version => {
          setSwitchError("");
          setSwitching(version);
        }}
      />

      <DiffPanel
        hiveId={hiveId}
        versions={versionList}
        activeVersion={activeVersion}
      />

      {switching ? (
        <ConfirmDialog
          title={`切换到 v${switching.version}？`}
          confirmLabel={`切换到 v${switching.version}`}
          busy={switchBusy}
          error={switchError}
          onConfirm={activate}
          onCancel={() => setSwitching(null)}
        >
          <p>
            当前生效的是 v{activeVersion}。切换后，新派出的 Agent 会立即使用 v{switching.version}
            的 manifest 和文件；已经在运行的 Agent 不受影响。
          </p>
          <p className="hg-muted">
            {switching.note ? `这一版的备注：${switching.note}` : "这一版没有备注。"}
          </p>
        </ConfirmDialog>
      ) : null}
    </div>
  );
}

function MethodologyCatalog({
  hiveId,
  onInstalled,
}: {
  hiveId: string;
  onInstalled: () => void;
}) {
  const templates = useResource(signal => hiveAPI.listMethodologyTemplates(signal), []);
  const [selected, setSelected] = useState<MethodologyTemplate | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const install = async () => {
    if (!selected) return;
    setBusy(true);
    setError("");
    try {
      await hiveAPI.installMethodology(hiveId, selected.name);
      setSelected(null);
      onInstalled();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel title="选择方法论" subtitle="尚未选择方法论，可以在对话里请 Queen 推荐">
      <Callout>
        安装前不会自动派出 Forager。选择适合团队的方法论后，它会作为新版本立即生效。
      </Callout>
      {templates.loading && !templates.data ? (
        <BeeLoader label="正在读取内置目录…" />
      ) : templates.error ? (
        <ErrorNotice
          message={templates.error}
          onRetry={templates.reload}
          auth={templates.errorStatus === 401}
          admin={templates.errorStatus === 403}
        />
      ) : templates.data?.length ? (
        <ul className="hg-role-grid" aria-label="内置方法论目录">
          {templates.data.map(template => (
            <li key={template.name}>
              <article className="hg-role-card">
                <h3>{template.title}</h3>
                <p className="hg-role-desc">{template.description}</p>
                <p className="hg-muted"><code>builtin:{template.name}</code> · {template.version}</p>
                <button
                  type="button"
                  className="hg-btn hg-btn-primary"
                  aria-label={`安装 ${template.title}`}
                  onClick={() => {
                    setSelected(template);
                    setError("");
                  }}
                >
                  安装
                </button>
              </article>
            </li>
          ))}
        </ul>
      ) : (
        <p className="hg-muted">内置目录暂时没有可用方法论，可以请 Queen 帮你创建。</p>
      )}
      {selected ? (
        <ConfirmDialog
          title={`安装 ${selected.title}？`}
          confirmLabel="确认安装"
          busy={busy}
          error={error}
          onConfirm={() => void install()}
          onCancel={() => setSelected(null)}
        >
          <p>{selected.description}</p>
          <p>
            将安装 <code>builtin:{selected.name}</code>（{selected.version}），发布并立即启用这个版本。
            新派出的 Forager 会遵循其中的角色、权限和合并规则。
          </p>
        </ConfirmDialog>
      ) : null}
    </Panel>
  );
}

function ManifestView({ manifest }: { manifest: MethodologyManifest }) {
  const roles = Object.entries(manifest.roles ?? {}).sort(([left], [right]) =>
    left.localeCompare(right),
  );
  return (
    <>
      <Panel
        title="角色与权限"
        subtitle={`默认角色：${manifest.defaultRole}。权限由平台执行，不靠提示词。`}
        extra={<span className="hg-badge hg-tone-neutral">{roles.length} 个角色</span>}
      >
        {roles.length === 0 ? (
          <p className="hg-muted">这个 manifest 没有定义角色。</p>
        ) : (
          <ul className="hg-role-grid">
            {roles.map(([name, role]) => (
              <li key={name} data-testid={`method-role-${name}`}>
                <RoleCard name={name} role={role} isDefault={name === manifest.defaultRole} />
              </li>
            ))}
          </ul>
        )}
      </Panel>

      <div className="hg-methodology-two">
        <Panel title="常驻规则" subtitle="每一轮都会注入角色提示词。">
          {(manifest.standingOrders ?? []).length ? (
            <ol className="hg-orders">
              {(manifest.standingOrders ?? []).map(order => (
                <li key={order}>{order}</li>
              ))}
            </ol>
          ) : (
            <p className="hg-muted">没有常驻规则。</p>
          )}
        </Panel>

        <Panel title="事件规则" subtitle="外部事件按这些规则路由到角色。">
          {(manifest.events ?? []).length ? (
            <div className="hg-table-wrap">
              <table className="hg-mini-table">
                <thead>
                  <tr>
                    <th scope="col">事件</th>
                    <th scope="col">过滤</th>
                    <th scope="col">角色</th>
                  </tr>
                </thead>
                <tbody>
                  {(manifest.events ?? []).map((event, index) => (
                    <EventRow key={`${event.event}-${event.role}-${index}`} event={event} />
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="hg-muted">没有事件规则；事件会留在 Inbox。</p>
          )}
        </Panel>
      </div>

      <div className="hg-methodology-two">
        <Panel title="合并条件" subtitle="平台在合并入口逐条检查。">
          <MergePolicyView manifest={manifest} />
        </Panel>
        <Panel title="限额" subtitle="零值表示沿用 Hive 设置。">
          <LimitsView manifest={manifest} />
        </Panel>
      </div>
    </>
  );
}

const permissionLabels: { key: keyof RolePermissions; label: string }[] = [
  { key: "publish", label: "发布分支 / PR" },
  { key: "message", label: "给同伴发消息" },
  { key: "spawn", label: "派生子 Agent" },
  { key: "attest", label: "记录结论" },
  { key: "merge", label: "调用合并" },
  { key: "methodology", label: "修改方法论" },
];

function RoleCard({
  name,
  role,
  isDefault,
}: {
  name: string;
  role: MethodologyRole;
  isDefault: boolean;
}) {
  const permissions = role.permissions ?? ({} as RolePermissions);
  const spawn = permissions.spawn ?? [];
  return (
    <article className="hg-role-card">
      <header className="hg-role-head">
        <h3>{name}</h3>
        {isDefault ? <span className="hg-badge hg-tone-amber">默认</span> : null}
      </header>
      {role.description ? <p className="hg-role-desc">{role.description}</p> : null}
      <dl className="hg-role-meta">
        <div>
          <dt>模型</dt>
          <dd>{role.model ? <code>{role.model}</code> : "Hive 默认模型"}</dd>
        </div>
        <div>
          <dt>生命周期</dt>
          <dd>{role.lifecycle === "resident" ? "常驻" : "随任务结束"}</dd>
        </div>
        {role.prompt ? (
          <div>
            <dt>提示词</dt>
            <dd>
              <code>{role.prompt}</code>
            </dd>
          </div>
        ) : null}
      </dl>
      <ul className="hg-perm-grid" aria-label={`${name} 的权限`}>
        {permissionLabels.map(item => {
          const enabled =
            item.key === "spawn" ? spawn.length > 0 : Boolean(permissions[item.key]);
          return (
            <li key={item.key} className={enabled ? "is-on" : "is-off"}>
              <Icon name={enabled ? "check" : "close"} size={16} />
              <span>{item.label}</span>
              {item.key === "spawn" && spawn.length ? <code>{spawn.join(", ")}</code> : null}
            </li>
          );
        })}
      </ul>
      {role.denyTools?.length ? (
        <p className="hg-role-deny">
          禁用工具：<code>{role.denyTools.join(", ")}</code>
        </p>
      ) : null}
    </article>
  );
}

function EventRow({ event }: { event: EventRule }) {
  const filter = event.label
    ? `label = ${event.label}`
    : event.commentPrefix
      ? `评论前缀 ${event.commentPrefix}`
      : "—";
  return (
    <tr>
      <td>
        <code>{event.event}</code>
      </td>
      <td>{filter}</td>
      <td>
        <code>{event.role}</code>
      </td>
    </tr>
  );
}

function MergePolicyView({ manifest }: { manifest: MethodologyManifest }) {
  const merge = manifest.merge;
  const accepted = merge.acceptResults?.length ? merge.acceptResults : ["pass", "pass_notes"];
  return (
    <ul className="hg-policy-list">
      <li className={merge.requireAttestation ? "is-on" : "is-off"}>
        <Icon name={merge.requireAttestation ? "check" : "close"} size={16} />
        {merge.requireAttestation ? "合并前需要一条结论" : "合并不要求结论"}
      </li>
      <li className="is-on">
        <Icon name="check" size={16} />
        接受的结论：<code>{accepted.join(", ")}</code>
      </li>
      <li className={merge.requireNonAuthor ? "is-on" : "is-off"}>
        <Icon name={merge.requireNonAuthor ? "check" : "close"} size={16} />
        {merge.requireNonAuthor ? "结论必须来自非作者" : "作者可以给自己的改动下结论"}
      </li>
      <li className={merge.requireChecks ? "is-on" : "is-off"}>
        <Icon name={merge.requireChecks ? "check" : "close"} size={16} />
        {merge.requireChecks ? "GitHub 必需检查必须通过" : "不检查 GitHub CI"}
      </li>
      <li className={merge.auto ? "is-on" : "is-off"}>
        <Icon name={merge.auto ? "check" : "close"} size={16} />
        {merge.auto ? "满足条件后自动合并" : "满足条件后仍需人工确认"}
      </li>
      <li className="is-on">
        <Icon name="check" size={16} />
        合并方式：<code>{merge.method || "squash"}</code>
      </li>
    </ul>
  );
}

function LimitsView({ manifest }: { manifest: MethodologyManifest }) {
  const limits = manifest.limits ?? {};
  const rows: { label: string; value: string | undefined }[] = [
    { label: "并发 Agent", value: limits.maxConcurrentAgents?.toString() },
    { label: "消息跳数", value: limits.maxHops?.toString() },
    { label: "每条链 Agent", value: limits.maxAgentsPerChain?.toString() },
    {
      label: "单 Agent 花费",
      value: limits.maxCostPerAgentUsd ? formatUsd(limits.maxCostPerAgentUsd) : undefined,
    },
    {
      label: "每日花费",
      value: limits.maxCostPerDayUsd ? formatUsd(limits.maxCostPerDayUsd) : undefined,
    },
  ];
  const defined = rows.filter(row => row.value !== undefined);
  if (defined.length === 0) {
    return <p className="hg-muted">这个方法论的 limits 都是零值，沿用 Hive 设置。</p>;
  }
  return (
    <dl className="hg-limits">
      {defined.map(row => (
        <div key={row.label}>
          <dt>{row.label}</dt>
          <dd>{row.value}</dd>
        </div>
      ))}
    </dl>
  );
}

function FilesPanel({ files }: { files: MethodologyFile[] }) {
  const [selectedPath, setSelectedPath] = useState("");
  useEffect(() => {
    if (files.length === 0) {
      setSelectedPath("");
      return;
    }
    setSelectedPath(previous => {
      if (previous && files.some(file => file.path === previous)) return previous;
      return files.find(file => file.path === "methodology.yaml")?.path ?? files[0].path;
    });
  }, [files]);

  const selected = files.find(file => file.path === selectedPath) ?? files[0];
  return (
    <Panel
      title="文件"
      subtitle="整组文件按相对路径原样下发到 Cell 的只读配置目录。"
      extra={<span className="hg-badge hg-tone-neutral">{files.length} 个文件</span>}
    >
      {files.length === 0 ? (
        <EmptyState title="这个版本没有文件。" hint="至少应该有 methodology.yaml。" />
      ) : (
        <div className="hg-file-browser">
          <ul className="hg-file-list" aria-label="方法论文件">
            {files.map(file => (
              <li key={file.path}>
                <button
                  type="button"
                  className={file.path === selected?.path ? "is-active" : undefined}
                  onClick={() => setSelectedPath(file.path)}
                  aria-pressed={file.path === selected?.path}
                >
                  <Icon name="diff" size={16} />
                  <span>{file.path}</span>
                  <code>{fileSize(file.content)}</code>
                </button>
              </li>
            ))}
          </ul>
          {selected ? (
            <div className="hg-file-viewer">
              <div className="hg-file-viewer-head">
                <code>{selected.path}</code>
                <span className="hg-muted">{fileSize(selected.content)}</span>
              </div>
              <pre data-testid="methodology-file-content">{selected.content}</pre>
            </div>
          ) : null}
        </div>
      )}
    </Panel>
  );
}

function fileSize(content: string): string {
  const bytes = new TextEncoder().encode(content).length;
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KB`;
}

function VersionHistory({
  versions,
  changes,
  activeVersion,
  viewingVersion,
  onView,
  onSwitch,
}: {
  versions: Methodology[];
  changes: MethodologyChange[];
  activeVersion: number;
  viewingVersion: number;
  onView: (version: number) => void;
  onSwitch: (version: Methodology) => void;
}) {
  const sorted = [...versions].sort((left, right) => right.version - left.version);
  const records = useMemo(() => modificationRecords(versions, changes), [versions, changes]);
  return (
    <>
      <Panel
        title="版本历史"
        subtitle="每个版本不可变；切换只是把另一个版本设为 active。"
        extra={<span className="hg-badge hg-tone-neutral">{versions.length} 个版本</span>}
      >
        {sorted.length === 0 ? (
          <p className="hg-muted">还没有版本记录。</p>
        ) : (
          <div className="hg-table-wrap">
            <table className="hg-version-table">
              <thead>
                <tr>
                  <th scope="col">版本</th>
                  <th scope="col">状态</th>
                  <th scope="col">备注</th>
                  <th scope="col">修改人</th>
                  <th scope="col">时间</th>
                  <th scope="col">来源</th>
                  <th scope="col">操作</th>
                </tr>
              </thead>
              <tbody>
                {sorted.map(version => {
                  const active = version.version === activeVersion;
                  return (
                    <tr key={version.version} className={active ? "is-active" : undefined}>
                      <td>
                        <strong>v{version.version}</strong>
                      </td>
                      <td>
                        {active ? (
                          <span className="hg-badge hg-tone-mint">当前生效</span>
                        ) : (
                          <span className="hg-badge hg-tone-neutral">历史</span>
                        )}
                      </td>
                      <td className="hg-version-note-cell">{version.note || "—"}</td>
                      <td>{version.createdBy || "—"}</td>
                      <td>{formatDateTime(version.createdAt)}</td>
                      <td className="hg-version-source-cell">
                        {version.source ? (
                          <span>
                            {version.source.url}@{version.source.ref}
                          </span>
                        ) : (
                          "控制台 / MCP"
                        )}
                      </td>
                      <td>
                        <div className="hg-row-actions">
                          <button
                            className="hg-btn hg-btn-small"
                            type="button"
                            disabled={version.version === viewingVersion}
                            onClick={() => onView(version.version)}
                          >
                            {version.version === viewingVersion ? "正在查看" : "查看"}
                          </button>
                          {!active ? (
                            <button
                              className="hg-btn hg-btn-small"
                              type="button"
                              onClick={() => onSwitch(version)}
                            >
                              切换到此版本
                            </button>
                          ) : null}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <Panel title="修改记录" subtitle="发布和切换只追加，不覆盖。">
        {records.length === 0 ? (
          <p className="hg-muted">还没有修改记录。</p>
        ) : (
          <ol className="hg-change-log">
            {records.map(record => (
              <li key={record.key}>
                <span className={`hg-change-action hg-change-${record.action}`}>
                  {record.action === "activate" ? "切换" : "发布"}
                </span>
                <div className="hg-change-main">
                  <strong>v{record.version}</strong>
                  <span className="hg-muted"> · {record.actor || "—"}</span>
                  {record.note ? <p>{record.note}</p> : null}
                  {record.source ? (
                    <p className="hg-change-source">
                      来源：{record.source.url}@{record.source.ref}
                    </p>
                  ) : null}
                </div>
                <time className="hg-time">{formatDateTime(record.createdAt)}</time>
              </li>
            ))}
          </ol>
        )}
      </Panel>
    </>
  );
}

type ChangeRecord = {
  key: string;
  action: string;
  version: number;
  actor: string;
  note?: string;
  source?: Methodology["source"];
  createdAt: string;
};

function modificationRecords(
  versions: Methodology[],
  changes: MethodologyChange[],
): ChangeRecord[] {
  if (changes.length) {
    const sources = new Map(versions.map(version => [version.version, version.source]));
    return [...changes]
      .sort((left, right) => right.createdAt.localeCompare(left.createdAt) || right.id - left.id)
      .map(change => ({
        key: `change-${change.id}`,
        action: change.action,
        version: change.version,
        actor: change.actor,
        note: change.note,
        source: change.action === "publish" ? sources.get(change.version) : undefined,
        createdAt: change.createdAt,
      }));
  }
  // The version rows already carry who/when/note/source; show them as publishes
  // until the append-only change log has its own rows.
  return [...versions]
    .sort((left, right) => right.version - left.version)
    .map(version => ({
      key: `version-${version.version}`,
      version: version.version,
      action: "publish",
      actor: version.createdBy,
      note: version.note,
      source: version.source,
      createdAt: version.createdAt,
    }));
}

function DiffPanel({
  hiveId,
  versions,
  activeVersion,
}: {
  hiveId: string;
  versions: Methodology[];
  activeVersion: number;
}) {
  const sorted = useMemo(
    () => [...versions].sort((left, right) => left.version - right.version),
    [versions],
  );
  const [baseVersion, setBaseVersion] = useState<number | undefined>(undefined);
  const [compareVersion, setCompareVersion] = useState<number | undefined>(undefined);

  useEffect(() => {
    if (sorted.length === 0) return;
    setCompareVersion(previous =>
      previous !== undefined && sorted.some(item => item.version === previous)
        ? previous
        : activeVersion,
    );
    setBaseVersion(previous => {
      if (previous !== undefined && sorted.some(item => item.version === previous)) return previous;
      const before = [...sorted].reverse().find(item => item.version < activeVersion);
      return (before ?? sorted[0]).version;
    });
  }, [sorted, activeVersion]);

  const base = useResource(
    signal =>
      baseVersion === undefined
        ? Promise.resolve(undefined)
        : hiveAPI.getMethodology(hiveId, baseVersion, signal),
    [hiveId, baseVersion],
  );
  const compare = useResource(
    signal =>
      compareVersion === undefined
        ? Promise.resolve(undefined)
        : hiveAPI.getMethodology(hiveId, compareVersion, signal),
    [hiveId, compareVersion],
  );

  const result = useMemo(() => {
    if (!base.data || !compare.data) return null;
    const fileDiffs = diffFiles(base.data.files, compare.data.files);
    const manifestLines = diffLines(
      JSON.stringify(base.data.manifest, null, 2),
      JSON.stringify(compare.data.manifest, null, 2),
    );
    const compactManifest = compactDiff(manifestLines);
    const stats = diffStats(fileDiffs);
    const manifestChanged = compactManifest.some(line => line.kind !== "same");
    return { fileDiffs, compactManifest, stats, manifestChanged };
  }, [base.data, compare.data]);

  const sameVersion =
    baseVersion !== undefined && compareVersion !== undefined && baseVersion === compareVersion;

  return (
    <Panel
      title="版本差异"
      subtitle="把两个版本的 manifest 和文件放在一起比较。"
      extra={
        <div className="hg-diff-pickers">
          <label className="hg-inline-field">
            基线
            <select
              value={baseVersion ?? ""}
              aria-label="差异基线版本"
              onChange={event => setBaseVersion(Number(event.target.value))}
            >
              {sorted.map(version => (
                <option key={version.version} value={version.version}>
                  v{version.version}
                </option>
              ))}
            </select>
          </label>
          <span aria-hidden="true">→</span>
          <label className="hg-inline-field">
            对比
            <select
              value={compareVersion ?? ""}
              aria-label="差异对比版本"
              onChange={event => setCompareVersion(Number(event.target.value))}
            >
              {sorted.map(version => (
                <option key={version.version} value={version.version}>
                  v{version.version}
                </option>
              ))}
            </select>
          </label>
        </div>
      }
    >
      {sorted.length < 2 ? (
        <p className="hg-muted">至少要有两个版本才能比较差异。</p>
      ) : sameVersion ? (
        <p className="hg-muted">基线和对像是同一个版本，换一个版本再比较。</p>
      ) : base.error || compare.error ? (
        <ErrorNotice
          message={base.error || compare.error}
          onRetry={() => {
            base.reload();
            compare.reload();
          }}
        />
      ) : !base.data || !compare.data ? (
        <BeeLoader label="正在比较两个版本…" />
      ) : result ? (
        <div className="hg-diff-body">
          <p className="hg-diff-summary">
            <span className="hg-tone-mint">+{result.stats.added}</span>
            <span className="hg-tone-coral">−{result.stats.removed}</span>
            <span>
              {result.stats.files} 个文件有变化 · manifest{" "}
              {result.manifestChanged ? "有变化" : "没有变化"}
            </span>
          </p>

          <section className="hg-diff-section">
            <h3>manifest</h3>
            {result.manifestChanged ? (
              <DiffCode lines={result.compactManifest} />
            ) : (
              <p className="hg-muted">两个版本的 manifest 完全一致。</p>
            )}
          </section>

          <section className="hg-diff-section">
            <h3>文件</h3>
            {result.fileDiffs.length === 0 ? (
              <p className="hg-muted">两个版本的文件完全一致。</p>
            ) : (
              <div className="hg-file-diffs">
                {result.fileDiffs.map(file => (
                  <details key={file.path} className="hg-file-diff">
                    <summary>
                      <span className={`hg-diff-kind hg-diff-kind-${file.kind}`}>
                        {file.kind === "added" ? "新增" : file.kind === "removed" ? "删除" : "修改"}
                      </span>
                      <code>{file.path}</code>
                      <span className="hg-muted">
                        +{file.lines.filter(line => line.kind === "add").length} / −
                        {file.lines.filter(line => line.kind === "del").length}
                      </span>
                    </summary>
                    <DiffCode lines={compactDiff(file.lines)} />
                  </details>
                ))}
              </div>
            )}
          </section>
        </div>
      ) : null}
    </Panel>
  );
}

function DiffCode({
  lines,
}: {
  lines: { kind: "same" | "add" | "del"; text: string; oldLine?: number; newLine?: number }[];
}) {
  return (
    <pre className="hg-diff-code" data-testid="methodology-diff">
      {lines.map((line, index) => (
        <span key={`${index}-${line.kind}`} className={`hg-diff-line hg-diff-${line.kind}`}>
          <span className="hg-diff-sign" aria-hidden="true">
            {line.kind === "add" ? "+" : line.kind === "del" ? "−" : " "}
          </span>
          {line.text || " "}
          {"\n"}
        </span>
      ))}
    </pre>
  );
}
