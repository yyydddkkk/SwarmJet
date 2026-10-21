// Hive 方法论: round-5 catalog installation and round-4 version management.
// The page renders the
// manifest as structured cards instead of raw YAML, exposes every file, keeps
// the append-only version history and modification log, diffs any two versions,
// and switches versions behind a confirmation.
import { useEffect, useMemo, useState } from "react";
import { useDataAPI, methodologyDescription } from "../../data";
import type {
  EventRule,
  Methodology,
  MethodologyChange,
  MethodologyFile,
  MethodologyManifest,
  MethodologyRole,
  MethodologyTemplate,
  MethodologySource,
  RolePermissions,
} from "../../data";
import {
  BeeLoader,
  Callout,
  ConfirmDialog,
  EmptyState,
  ErrorNotice,
  Panel,
  StatusGrid,
  StatusItem,
} from "./primitives";
import { compactDiff, diffFiles, diffLines, diffStats } from "./diff";
import { formatDateTime, formatUsd, shortSha } from "./format";
import { Icon } from "./icons";
import { useResource } from "./use-resource";

export default function MethodologySection({ hiveId, initialVersion }: { hiveId: string; initialVersion?: number }) {
  const api = useDataAPI();
  const [viewVersion, setViewVersion] = useState<number | undefined>(initialVersion);
  useEffect(() => setViewVersion(initialVersion), [hiveId, initialVersion]);
  const current = useResource(
    (signal) => api.getMethodology(hiveId, viewVersion, signal),
    [hiveId, viewVersion],
  );
  const versions = useResource(
    (signal) => api.listMethodologyVersions(hiveId, signal),
    [hiveId],
  );
  const [notice, setNotice] = useState("");
  const [switching, setSwitching] = useState<Methodology | null>(null);
  const [switchBusy, setSwitchBusy] = useState(false);
  const [switchError, setSwitchError] = useState("");
  const [switchNote, setSwitchNote] = useState("");

  if (current.loading && !current.data) {
    return <BeeLoader label="正在读取方法论…" />;
  }
  const installed = () => {
    setViewVersion(undefined);
    setNotice("方法论已安装；新派出的 Agent 会使用这个版本。");
    current.reload();
    versions.reload();
  };

  if (
    current.errorStatus === 404 &&
    viewVersion === undefined &&
    !current.data
  ) {
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
  if (builtin)
    return <MethodologyCatalog hiveId={hiveId} onInstalled={installed} />;

  // The response is the version itself, flattened, plus its files.
  const methodology = current.data;
  const { files } = current.data;
  const versionList = versions.data?.versions ?? [];
  const changes = versions.data?.changes ?? [];
  const activeVersion =
    versions.data?.activeVersion ||
    versionList.find((item) => item.active)?.version ||
    methodology.version;
  const versionLabel = builtin ? "内置方法论" : `v${methodology.version}`;

  const activate = () => {
    if (!switching) return;
    const version = switching.version;
    setSwitchBusy(true);
    setSwitchError("");
    api
      .activateMethodology(
        hiveId,
        version,
        undefined,
        switchNote.trim() || undefined,
      )
      .then(() => {
        setNotice(`已切换到 v${version}；新的 Agent 会立刻使用这个版本。`);
        setViewVersion(version);
        setSwitching(null);
        current.reload();
        versions.reload();
      })
      .catch((cause) =>
        setSwitchError(cause instanceof Error ? cause.message : String(cause)),
      )
      .finally(() => setSwitchBusy(false));
  };

  return (
    <div className="pc-methodology">
      {builtin ? (
        <MethodologyCatalog hiveId={hiveId} onInstalled={installed} />
      ) : null}
      {notice ? (
        <div className="pc-flash pc-flash-success" role="status">
          <Icon name="check" size={16} />
          <span>{notice}</span>
        </div>
      ) : null}

      <Panel
        title="方法论"
        extra={
          <label className="pc-inline-field">
            查看版本
            <select
              value={methodology.version}
              onChange={(event) => setViewVersion(Number(event.target.value))}
              aria-label="查看方法论版本"
            >
              {versionList.length ? (
                [...versionList]
                  .sort((left, right) => right.version - left.version)
                  .map((item) => (
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
        <p className="pc-method-version" data-testid="methodology-version">
          {versionLabel}，{methodology.active ? "当前生效" : "历史版本"}
          {methodology.source?.url.startsWith("builtin:")
            ? `；${methodology.source.url.slice(8)} 来源版本 ${methodology.source.ref}`
            : ""}
        </p>
        <RoleDiagram roles={Object.keys(methodology.manifest.roles ?? {})} />
        <RulesSummary manifest={methodology.manifest} />
        <details>
          <summary>版本详情</summary>
          <p className="pc-muted">
            一个版本是一组文件；切换版本即可回滚。项目版本与内置目录版本独立编号。
          </p>
          <StatusGrid>
            <StatusItem label="版本">
              <span className="pc-version-badge">
                {versionLabel}
                {builtin ? (
                  <span className="pc-badge pc-tone-amber">平台默认</span>
                ) : methodology.active ? (
                  <span className="pc-badge pc-tone-mint">当前生效</span>
                ) : (
                  <span className="pc-badge pc-tone-neutral">历史版本</span>
                )}
              </span>
            </StatusItem>
            <StatusItem label="默认角色">
              <code>{methodology.manifest.defaultRole}</code>
            </StatusItem>
            <StatusItem label="摘要">
              <code title={methodology.digest}>
                {shortSha(methodology.digest)}
              </code>
            </StatusItem>
            <StatusItem label="修改人">
              {builtin ? "平台内置" : methodology.createdBy || "—"}
            </StatusItem>
            <StatusItem label="时间">
              {builtin ? "—" : formatDateTime(methodology.createdAt)}
            </StatusItem>
            <StatusItem label="来源">
              {methodology.source ? (
                <span className="pc-source">
                  {methodology.source.url}@{methodology.source.ref}
                  {methodology.source.sha
                    ? ` · ${shortSha(methodology.source.sha)}`
                    : ""}
                  {methodology.source.path
                    ? ` · ${methodology.source.path}`
                    : ""}
                </span>
              ) : builtin ? (
                "这个项目 还没有发布过方法论，使用平台内置版本"
              ) : (
                "控制台或 MCP 直接发布"
              )}
            </StatusItem>
          </StatusGrid>
          {methodology.note ? (
            <p className="pc-version-note">备注：{methodology.note}</p>
          ) : null}
        </details>
      </Panel>

      <FilesPanel files={files} />
      <details>
        <summary>角色、权限与全部规则</summary>
        <ManifestView manifest={methodology.manifest} />
      </details>
      <details>
        <summary>从内置目录安装方法论</summary>
        <MethodologyCatalog
          hiveId={hiveId}
          onInstalled={installed}
          hasInstalled
          installedSource={
            versionList.find((item) => item.version === activeVersion)
              ?.source ?? methodology.source
          }
        />
      </details>

      <details>
        <summary>版本历史与回滚</summary>
        <VersionHistory
          versions={versionList}
          changes={changes}
          activeVersion={activeVersion}
          viewingVersion={methodology.version}
          onView={setViewVersion}
          onSwitch={(version) => {
            setSwitchError("");
            setSwitchNote("");
            setSwitching(version);
          }}
        />
      </details>

      <details>
        <summary>比较版本差异</summary>
        <DiffPanel
          hiveId={hiveId}
          versions={versionList}
          activeVersion={activeVersion}
        />
      </details>

      {switching ? (
        <ConfirmDialog
          title={`切换到 v${switching.version}？`}
          confirmLabel={`切换到 v${switching.version}`}
          busy={switchBusy}
          error={switchError}
          onConfirm={activate}
          onCancel={() => setSwitching(null)}
        >
          <label className="pc-field">
            切换备注
            <input
              value={switchNote}
              onChange={(event) => setSwitchNote(event.target.value)}
            />
          </label>
          <p>
            当前生效的是 v{activeVersion}。切换后，新派出的 Agent 会立即使用 v
            {switching.version}的 manifest 和文件；已经在运行的 Agent 不受影响。
          </p>
          <p className="pc-muted">
            {switching.note
              ? `这一版的备注：${switching.note}`
              : "这一版没有备注。"}
          </p>
        </ConfirmDialog>
      ) : null}
    </div>
  );
}
function Description({ raw }: { raw: string }) {
  const value = methodologyDescription(raw);
  return <div className="pc-role-desc"><p>{value.text}</p>{value.detail && <details><summary>查看原文</summary><p>{value.detail}</p></details>}</div>;
}

function MethodologyCatalog({
  hiveId,
  onInstalled,
  installedSource,
  hasInstalled = false,
}: {
  hiveId: string;
  onInstalled: () => void;
  installedSource?: MethodologySource;
  hasInstalled?: boolean;
}) {
  const api = useDataAPI();
  const templates = useResource(
    (signal) => api.listMethodologyTemplates(signal),
    [],
  );
  const [selected, setSelected] = useState<MethodologyTemplate | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const install = async () => {
    if (!selected) return;
    setBusy(true);
    setError("");
    try {
      await api.installMethodology(hiveId, selected.name);
      setSelected(null);
      onInstalled();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel
      title="选择方法论"
      subtitle={
        hasInstalled || installedSource
          ? "从目录安装会生成并启用一个项目版本。目录版本与项目版本独立编号。"
          : "尚未选择方法论，可以在对话里请主 Agent 推荐"
      }
    >
      <Callout>
        {hasInstalled
          ? "安装后，新派出的 Agent 使用新版本；已经在运行的工作不受影响。"
          : "安装前不会自动派出 Agent。选择适合团队的方法论后，它会作为新版本立即生效。"}
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
        <ul className="pc-role-grid" aria-label="内置方法论目录">
          {templates.data.map((template) => (
            <li key={template.name}>
              <article className="pc-role-card">
                <h3>{template.title}</h3>
                <Description raw={template.description} />
                <p className="pc-muted">
                  <code>builtin:{template.name}</code> 目录版本{" "}
                  {template.version}
                </p>
                {installedSource?.url === `builtin:${template.name}` && (
                  <p>
                    当前来源版本：{installedSource.ref}。
                    {installedSource.ref !== template.version
                      ? "目录有新版本可安装，请查看版本说明后确认。"
                      : "已使用目录中的版本。"}
                  </p>
                )}
                <button
                  type="button"
                  className="pc-btn pc-btn-primary"
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
        <p className="pc-muted">
          内置目录暂时没有可用方法论，可以请主 Agent 帮你创建。
        </p>
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
          <Description raw={selected.description} />
          <p>
            将安装 <code>builtin:{selected.name}</code>（{selected.version}
            ），发布并立即启用这个版本。 新派出的 Agent
            会遵循其中的角色、权限和合并规则。
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
        extra={
          <span className="pc-badge pc-tone-neutral">
            {roles.length} 个角色
          </span>
        }
      >
        {roles.length === 0 ? (
          <p className="pc-muted">这个 manifest 没有定义角色。</p>
        ) : (
          <ul className="pc-role-grid">
            {roles.map(([name, role]) => (
              <li key={name} data-testid={`method-role-${name}`}>
                <RoleCard
                  name={name}
                  role={role}
                  isDefault={name === manifest.defaultRole}
                />
              </li>
            ))}
          </ul>
        )}
      </Panel>

      <div className="pc-methodology-two">
        <Panel title="常驻规则" subtitle="每一轮都会注入角色提示词。">
          {(manifest.standingOrders ?? []).length ? (
            <ol className="pc-orders">
              {(manifest.standingOrders ?? []).map((order) => (
                <li key={order}>{order}</li>
              ))}
            </ol>
          ) : (
            <p className="pc-muted">没有常驻规则。</p>
          )}
        </Panel>

        <Panel title="事件规则" subtitle="外部事件按这些规则路由到角色。">
          {(manifest.events ?? []).length ? (
            <div className="pc-table-wrap">
              <table className="pc-mini-table">
                <thead>
                  <tr>
                    <th scope="col">事件</th>
                    <th scope="col">过滤</th>
                    <th scope="col">角色</th>
                  </tr>
                </thead>
                <tbody>
                  {(manifest.events ?? []).map((event, index) => (
                    <EventRow
                      key={`${event.event}-${event.role}-${index}`}
                      event={event}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="pc-muted">没有事件规则；事件会留在待处理区域。</p>
          )}
        </Panel>
      </div>

      <div className="pc-methodology-two">
        <Panel title="合并条件" subtitle="平台在合并入口逐条检查。">
          <MergePolicyView manifest={manifest} />
        </Panel>
        <Panel title="限额" subtitle="零值表示沿用 项目设置。">
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
    <article className="pc-role-card">
      <header className="pc-role-head">
        <h3>{name}</h3>
        {isDefault ? (
          <span className="pc-badge pc-tone-amber">默认</span>
        ) : null}
      </header>
      {role.description ? (
        <Description raw={role.description} />
      ) : null}
      <dl className="pc-role-meta">
        <div>
          <dt>模型</dt>
          <dd>{role.model ? <code>{role.model}</code> : "项目默认模型"}</dd>
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
      <ul className="pc-perm-grid" aria-label={`${name} 的权限`}>
        {permissionLabels.map((item) => {
          const enabled =
            item.key === "spawn"
              ? spawn.length > 0
              : Boolean(permissions[item.key]);
          return (
            <li key={item.key} className={enabled ? "is-on" : "is-off"}>
              <Icon name={enabled ? "check" : "close"} size={16} />
              <span>{item.label}</span>
              {item.key === "spawn" && spawn.length ? (
                <code>{spawn.join(", ")}</code>
              ) : null}
            </li>
          );
        })}
      </ul>
      {role.denyTools?.length ? (
        <p className="pc-role-deny">
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
        <span>
          {(
            {
              issue_labeled: "Issue 加上指定标签时",
              issue_comment: "Issue 收到评论时",
              pr_head: "PR 有新提交时",
              pull_request: "PR 更新时",
            } as Record<string, string>
          )[event.event] ?? `收到 ${event.event} 事件时`}
        </span>
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
  const accepted = merge.acceptResults?.length
    ? merge.acceptResults
    : ["pass", "pass_notes"];
  return (
    <ul className="pc-policy-list">
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
        {merge.requireNonAuthor
          ? "结论必须来自非作者"
          : "作者可以给自己的改动下结论"}
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
      value: limits.maxCostPerAgentUsd
        ? formatUsd(limits.maxCostPerAgentUsd)
        : undefined,
    },
    {
      label: "每日花费",
      value: limits.maxCostPerDayUsd
        ? formatUsd(limits.maxCostPerDayUsd)
        : undefined,
    },
  ];
  const defined = rows.filter((row) => row.value !== undefined);
  if (defined.length === 0) {
    return (
      <p className="pc-muted">这个方法论的 limits 都是零值，沿用 项目设置。</p>
    );
  }
  return (
    <dl className="pc-limits">
      {defined.map((row) => (
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
  const [expanded, setExpanded] = useState(false);
  useEffect(() => {
    if (files.length === 0) {
      setSelectedPath("");
      return;
    }
    setSelectedPath((previous) => {
      if (previous && files.some((file) => file.path === previous))
        return previous;
      return (
        files.find((file) => file.path === "methodology.yaml")?.path ??
        files[0].path
      );
    });
  }, [files]);

  const selected = files.find((file) => file.path === selectedPath) ?? files[0];
  return (
    <Panel
      title="文件"
      extra={
        <span className="pc-badge pc-tone-neutral">{files.length} 个文件</span>
      }
    >
      {files.length === 0 ? (
        <EmptyState
          title="这个版本没有文件。"
          hint="至少应该有 methodology.yaml。"
        />
      ) : (
        <div className="pc-file-browser">
          <ul className="pc-file-list" aria-label="方法论文件">
            {files.map((file) => (
              <li key={file.path}>
                <button
                  type="button"
                  className={
                    file.path === selected?.path ? "is-active" : undefined
                  }
                  onClick={() => {
                    setSelectedPath(file.path);
                    setExpanded(true);
                  }}
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
            <details
              className="pc-file-viewer"
              open={expanded}
              onToggle={(event) => setExpanded(event.currentTarget.open)}
            >
              <summary>查看文件内容</summary>
              <div className="pc-file-viewer-head">
                <code>{selected.path}</code>
                <span className="pc-muted">{fileSize(selected.content)}</span>
              </div>
              <pre data-testid="methodology-file-content">
                {selected.content}
              </pre>
            </details>
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
  const sorted = [...versions].sort(
    (left, right) => right.version - left.version,
  );
  const records = useMemo(
    () => modificationRecords(versions, changes),
    [versions, changes],
  );
  return (
    <>
      <Panel
        title="版本历史"
        subtitle="每个版本不可变；切换只是把另一个版本设为 active。"
        extra={
          <span className="pc-badge pc-tone-neutral">
            {versions.length} 个版本
          </span>
        }
      >
        {sorted.length === 0 ? (
          <p className="pc-muted">还没有版本记录。</p>
        ) : (
          <div className="pc-table-wrap">
            <table className="pc-version-table">
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
                {sorted.map((version) => {
                  const active = version.version === activeVersion;
                  return (
                    <tr
                      key={version.version}
                      className={active ? "is-active" : undefined}
                    >
                      <td>
                        <strong>v{version.version}</strong>
                      </td>
                      <td>
                        {active ? (
                          <span className="pc-badge pc-tone-mint">
                            当前生效
                          </span>
                        ) : (
                          <span className="pc-badge pc-tone-neutral">历史</span>
                        )}
                      </td>
                      <td className="pc-version-note-cell">
                        {version.note || "—"}
                      </td>
                      <td>{version.createdBy || "—"}</td>
                      <td>{formatDateTime(version.createdAt)}</td>
                      <td className="pc-version-source-cell">
                        {version.source ? (
                          <span>
                            {version.source.url}@{version.source.ref}
                          </span>
                        ) : (
                          "控制台 / MCP"
                        )}
                      </td>
                      <td>
                        <div className="pc-row-actions">
                          <button
                            className="pc-btn pc-btn-small"
                            type="button"
                            disabled={version.version === viewingVersion}
                            onClick={() => onView(version.version)}
                          >
                            {version.version === viewingVersion
                              ? "正在查看"
                              : "查看"}
                          </button>
                          {!active ? (
                            <button
                              className="pc-btn pc-btn-small"
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
          <p className="pc-muted">还没有修改记录。</p>
        ) : (
          <ol className="pc-change-log">
            {records.map((record) => (
              <li key={record.key}>
                <span className={`pc-change-action pc-change-${record.action}`}>
                  {record.action === "activate" ? "切换" : "发布"}
                </span>
                <div className="pc-change-main">
                  <strong>v{record.version}</strong>
                  <span className="pc-muted"> · {record.actor || "—"}</span>
                  {record.note ? <p>{record.note}</p> : null}
                  {record.source ? (
                    <p className="pc-change-source">
                      来源：{record.source.url}@{record.source.ref}
                    </p>
                  ) : null}
                </div>
                <time className="pc-time">
                  {formatDateTime(record.createdAt)}
                </time>
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
    const sources = new Map(
      versions.map((version) => [version.version, version.source]),
    );
    return [...changes]
      .sort(
        (left, right) =>
          Date.parse(right.createdAt) - Date.parse(left.createdAt) ||
          right.id - left.id,
      )
      .map((change) => ({
        key: `change-${change.id}`,
        action: change.action,
        version: change.version,
        actor: change.actor,
        note: change.note,
        source:
          change.action === "publish" ? sources.get(change.version) : undefined,
        createdAt: change.createdAt,
      }));
  }
  // The version rows already carry who/when/note/source; show them as publishes
  // until the append-only change log has its own rows.
  return [...versions]
    .sort((left, right) => right.version - left.version)
    .map((version) => ({
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
  const api = useDataAPI();
  const sorted = useMemo(
    () => [...versions].sort((left, right) => left.version - right.version),
    [versions],
  );
  const [baseVersion, setBaseVersion] = useState<number | undefined>(undefined);
  const [compareVersion, setCompareVersion] = useState<number | undefined>(
    undefined,
  );

  useEffect(() => {
    if (sorted.length === 0) return;
    setCompareVersion((previous) =>
      previous !== undefined && sorted.some((item) => item.version === previous)
        ? previous
        : activeVersion,
    );
    setBaseVersion((previous) => {
      if (
        previous !== undefined &&
        sorted.some((item) => item.version === previous)
      )
        return previous;
      const before = [...sorted]
        .reverse()
        .find((item) => item.version < activeVersion);
      return (before ?? sorted[0]).version;
    });
  }, [sorted, activeVersion]);

  const base = useResource(
    (signal) =>
      baseVersion === undefined
        ? Promise.resolve(undefined)
        : api.getMethodology(hiveId, baseVersion, signal),
    [hiveId, baseVersion],
  );
  const compare = useResource(
    (signal) =>
      compareVersion === undefined
        ? Promise.resolve(undefined)
        : api.getMethodology(hiveId, compareVersion, signal),
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
    const manifestChanged = compactManifest.some(
      (line) => line.kind !== "same",
    );
    return { fileDiffs, compactManifest, stats, manifestChanged };
  }, [base.data, compare.data]);

  const sameVersion =
    baseVersion !== undefined &&
    compareVersion !== undefined &&
    baseVersion === compareVersion;

  return (
    <Panel
      title="版本差异"
      subtitle="把两个版本的 manifest 和文件放在一起比较。"
      extra={
        <div className="pc-diff-pickers">
          <label className="pc-inline-field">
            基线
            <select
              value={baseVersion ?? ""}
              aria-label="差异基线版本"
              onChange={(event) => setBaseVersion(Number(event.target.value))}
            >
              {sorted.map((version) => (
                <option key={version.version} value={version.version}>
                  v{version.version}
                </option>
              ))}
            </select>
          </label>
          <span aria-hidden="true">→</span>
          <label className="pc-inline-field">
            对比
            <select
              value={compareVersion ?? ""}
              aria-label="差异对比版本"
              onChange={(event) =>
                setCompareVersion(Number(event.target.value))
              }
            >
              {sorted.map((version) => (
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
        <p className="pc-muted">至少要有两个版本才能比较差异。</p>
      ) : sameVersion ? (
        <p className="pc-muted">基线和对像是同一个版本，换一个版本再比较。</p>
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
        <div className="pc-diff-body">
          <p className="pc-diff-summary">
            <span className="pc-tone-mint">+{result.stats.added}</span>
            <span className="pc-tone-coral">−{result.stats.removed}</span>
            <span>
              {result.stats.files} 个文件有变化 · manifest{" "}
              {result.manifestChanged ? "有变化" : "没有变化"}
            </span>
          </p>

          <section className="pc-diff-section">
            <h3>manifest</h3>
            {result.manifestChanged ? (
              <DiffCode lines={result.compactManifest} />
            ) : (
              <p className="pc-muted">两个版本的 manifest 完全一致。</p>
            )}
          </section>

          <section className="pc-diff-section">
            <h3>文件</h3>
            {result.fileDiffs.length === 0 ? (
              <p className="pc-muted">两个版本的文件完全一致。</p>
            ) : (
              <div className="pc-file-diffs">
                {result.fileDiffs.map((file) => (
                  <details key={file.path} className="pc-file-diff">
                    <summary>
                      <span
                        className={`pc-diff-kind pc-diff-kind-${file.kind}`}
                      >
                        {file.kind === "added"
                          ? "新增"
                          : file.kind === "removed"
                            ? "删除"
                            : "修改"}
                      </span>
                      <code>{file.path}</code>
                      <span className="pc-muted">
                        +
                        {
                          file.lines.filter((line) => line.kind === "add")
                            .length
                        }{" "}
                        / −
                        {
                          file.lines.filter((line) => line.kind === "del")
                            .length
                        }
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
  lines: {
    kind: "same" | "add" | "del";
    text: string;
    oldLine?: number;
    newLine?: number;
  }[];
}) {
  return (
    <pre className="pc-diff-code" data-testid="methodology-diff">
      {lines.map((line, index) => (
        <span
          key={`${index}-${line.kind}`}
          className={`pc-diff-line pc-diff-${line.kind}`}
        >
          <span className="pc-diff-sign" aria-hidden="true">
            {line.kind === "add" ? "+" : line.kind === "del" ? "−" : " "}
          </span>
          {line.text || " "}
          {"\n"}
        </span>
      ))}
    </pre>
  );
}

function RoleDiagram({ roles }: { roles: string[] }) {
  return (
    <svg
      className="pc-orbits"
      viewBox="0 0 320 110"
      role="img"
      aria-label="角色分工示意"
    >
      <circle cx="160" cy="55" r="4" />
      {roles.map((role, i) => (
        <g key={role}>
          <ellipse cx="160" cy="55" rx={45 + i * 18} ry={15 + i * 9} />
          <text x="12" y={15 + i * 18}>
            {role === "queen" ? "主 Agent" : role}
          </text>
        </g>
      ))}
    </svg>
  );
}

function RulesSummary({ manifest }: { manifest: MethodologyManifest }) {
  const events = manifest.events ?? [];
  const role = (name: string) => (name === "queen" ? "主 Agent" : name);
  const resultNames: Record<string, string> = {
    pass: "通过",
    pass_notes: "通过并附建议",
    fail: "未通过",
    blocked: "受阻",
  };
  const accepted = (
    manifest.merge.acceptResults?.length
      ? manifest.merge.acceptResults
      : ["pass", "pass_notes"]
  )
    .map((result) => resultNames[result] || result)
    .join("或");
  return (
    <div className="pc-rules-summary">
      <h3>事件规则</h3>
      {events.length ? (
        events.map((event, i) => (
          <p key={i}>
            {event.event === "issue_labeled"
              ? `Issue 加上${event.label ? `“${event.label}”` : "指定"}标签时`
              : event.event === "issue_comment"
                ? `Issue 收到${event.commentPrefix ? `以“${event.commentPrefix}”开头的` : ""}评论时`
                : event.event === "pr_head"
                  ? "PR 有新提交时"
                  : `收到 ${event.event} 事件时`}
            ，交给 {role(event.role)} 处理。
          </p>
        ))
      ) : (
        <p>没有设置自动分派的事件规则。</p>
      )}
      <h3>合并规则</h3>
      <p>
        {manifest.merge.requireAttestation
          ? `合并前需要${manifest.merge.requireNonAuthor ? "非作者" : "验证者"}给出${accepted}的结论。`
          : "合并不要求验证结论。"}
        {manifest.merge.requireChecks
          ? "GitHub 必需检查必须通过。"
          : "不要求 GitHub 检查。"}
        {manifest.merge.auto
          ? "满足条件后自动合并。"
          : "满足条件后由你确认合并。"}
      </p>
    </div>
  );
}
