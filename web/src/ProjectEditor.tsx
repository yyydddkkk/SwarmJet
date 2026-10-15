import { useEffect, useRef, useState } from "react";
import type { AgentConfig, Check, Connection, Project, Repository } from "./types";
import type {NodeView} from "./NodeSettings";
import { api } from "./api";
import { useAgentCatalog } from "./agents";
import { ProjectLabels } from "./ProjectLabels";
import { AddRepository } from "./AddRepository";
import { ErrorBox, Field } from "./ui";
import { useToast } from "./ToastProvider";
import { formatCommand, parseCommand } from "./commands";
import { randomId } from "./ids";
import type { Account } from "./AccessSettings";
import "./wizard.css";

const agent = (): AgentConfig => ({
  adapter: "",
  model: "",
  image: "swarmjet-agent:local",
  timeoutSeconds: 5400,
});
export const newProject = (): Project => ({
  id: randomId("project"),
  nodes:{mode:"auto"},
  name: "",
  acceptanceStrategy: "none",
  reviewMode: "none",
  metaUrl: "",
  metaRef: "",
  metaMode: "imported",
  version: 0,
  enabled: false,
  gitlabUrl: "",
  credentialRef: "GITLAB_TOKEN",
  mergeCredentialRef: "GITLAB_MERGE_TOKEN",
  repositories: [],
  development: agent(),
  review: agent(),
  integration: { composeFile: "", checks: [], startupTimeoutSeconds: 180 },
  maxReworks: 3,
  requireCI: false,
});
interface Discovery {
  name: string;
  suggestedId: string;
  gitlabUrl: string;
  metaUrl: string;
  metaRef: string;
  repositories: Repository[];
  branches: Record<string, string[]>;
  warnings: string[];
}
type RegistryImage = { digest: string; reference: string; tags: string[] };
const registryDigest = /^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[a-f0-9]{64}$/;
const lines = (s: string) =>
  s
    .split("\n")
    .map((v) => v.trim())
    .filter(Boolean);
const sameAgent = (a: AgentConfig, b: AgentConfig) =>
  JSON.stringify(a) === JSON.stringify(b);
const inferOrigin = (raw: string) => {
  try {
    const u = new URL(raw);
    return /^https?:$/.test(u.protocol) ? u.origin : "";
  } catch {
    return "";
  }
};

export function ProjectEditor({
  initial,
  onSaved,
  platformAdmin = true,
}: {
  initial: Project;
  onSaved: (p: Project) => void;
  platformAdmin?: boolean;
}) {
  const toast = useToast();
  const [p, setP] = useState<Project>(() => structuredClone(initial));
  const [activeUsers, setActiveUsers] = useState<Account[]>([]);
  const [initialOwnerUserId, setInitialOwnerUserId] = useState("");
  useEffect(() => { if (initial.version === 0) void api<{ users: Account[] }>("/users?status=active").then(result => setActiveUsers(result.users || [])).catch(() => setActiveUsers([])); }, [initial.version]);
  const [nodes,setNodes]=useState<NodeView[]>([]);
  useEffect(()=>{if (platformAdmin) api<NodeView[]>("/nodes").then(setNodes).catch(()=>{});},[platformAdmin]);
  const [registryImages, setRegistryImages] = useState<RegistryImage[]>([]);
  const [registryError, setRegistryError] = useState("");
  const [registryLoading, setRegistryLoading] = useState(false);
  const [manualImages, setManualImages] = useState({ development: false, review: false });
  async function loadRegistryImages(signal?: AbortSignal) {
    setRegistryLoading(true);
    setRegistryError("");
    try {
      const result = await api<{ images: RegistryImage[] }>("/registry/images", "GET", undefined, signal);
      if (!signal?.aborted) setRegistryImages(result.images || []);
    } catch (e) {
      if (!signal?.aborted) setRegistryError((e as Error).message);
    } finally {
      if (!signal?.aborted) setRegistryLoading(false);
    }
  }
  useEffect(() => {
    if (!platformAdmin) return;
    const controller = new AbortController();
    void loadRegistryImages(controller.signal);
    return () => controller.abort();
  }, [platformAdmin]);
  const latestProject = useRef(p);
  latestProject.current = p;
  const runnerSettings = useRef<HTMLDetailsElement>(null);
  useEffect(() => {
    if (initial.development.image === "swarmjet-agent:local" || initial.review.image === "swarmjet-agent:local") {
      if (runnerSettings.current) runnerSettings.current.open = true;
    }
  }, [initial.development.image, initial.review.image]);
  const {catalog:agentCatalog,error:agentCatalogError} = useAgentCatalog();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [guidance, setGuidance] = useState("");
  const [discovering, setDiscovering] = useState(false);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [branches, setBranches] = useState<Record<string, string[]>>({});
  const [defaultBranch, setDefaultBranch] = useState("dev");
  const [sourceChanged, setSourceChanged] = useState(false);
  const [manual, setManual] = useState(false);
  const [connections, setConnections] = useState<Connection[]>([]);
  const [addingConnection, setAddingConnection] = useState(false);
  const [connectionToken, setConnectionToken] = useState("");
  const [connectionMergeToken, setConnectionMergeToken] = useState("");
  const [savingConnection, setSavingConnection] = useState(false);
  const [shareReview, setShareReview] = useState(() =>
    sameAgent(initial.development, initial.review),
  );
  const [commands, setCommands] = useState(() =>
    (initial.integration.checks || []).map((c) =>
      formatCommand(c.command || []),
    ),
  );
  const existing = p.version > 0;
  const managed = p.metaMode === "managed";
  const github = p.provider === "github";
  const platformName = github ? "GitHub" : "GitLab";
  const changeName = github ? "PR" : "MR";
  useEffect(() => {
    if (managed || !platformAdmin) return;
    api<Connection[] | null>("/connections")
      .then((list) => setConnections(list || []))
      .catch(() => setConnections([]));
  }, [managed]);
  useEffect(() => {
    if (initial.version > 0) return;
    let live = true;
    api<{ agentImage: string }>("/project-defaults")
      .then((d) => {
        if (!live || !d.agentImage) return;
        setP((old) => ({
          ...old,
          development: {
            ...old.development,
            image:
              old.development.image === "swarmjet-agent:local"
                ? d.agentImage
                : old.development.image,
          },
          review: {
            ...old.review,
            image:
              old.review.image === "swarmjet-agent:local"
                ? d.agentImage
                : old.review.image,
          },
        }));
      })
      .catch(() => {
        /* Local default remains valid when the defaults endpoint is unavailable. */
      });
    return () => {
      live = false;
    };
  }, [initial.version]);
  function set<K extends keyof Project>(key: K, value: Project[K]) {
    setP((old) => ({ ...old, [key]: value }));
    setGuidance("");
  }
  function updateRepo(index: number, patch: Partial<Repository>) {
    setP((old) => ({
      ...old,
      repositories: (old.repositories || []).map((r, i) =>
        i === index ? { ...r, ...patch } : r,
      ),
    }));
    setGuidance("");
  }
  function updateAgent(
    role: "development" | "review",
    patch: Partial<AgentConfig>,
  ) {
    setP((old) => {
      const next = { ...old[role], ...patch };
      return {
        ...old,
        ...(patch.image === "" && !old.nodes?.mode ? { nodes: { mode: "auto" } } : {}),
        [role]: next,
        ...(role === "development" && shareReview
          ? { review: { ...next } }
          : {}),
      };
    });
    setGuidance("");
  }
  function updateCheck(index: number, patch: Partial<Check>) {
    set("integration", {
      ...p.integration,
      checks: (p.integration.checks || []).map((c, i) =>
        i === index ? { ...c, ...patch } : c,
      ),
    });
  }
  function changeSource(
    key: "metaUrl" | "metaRef" | "gitlabUrl",
    value: string,
  ) {
    setP((old) => ({
      ...old,
      [key]: value,
      ...(key === "metaUrl" &&
      !old.connectionId &&
      (!old.gitlabUrl || old.gitlabUrl === inferOrigin(old.metaUrl))
        ? { gitlabUrl: inferOrigin(value) }
        : {}),
    }));
    setGuidance("");
    setWarnings([]);
    if ((p.repositories || []).length) setSourceChanged(true);
  }
  function selectConnection(id: string) {
    const connection = connections.find((item) => item.id === id);
    setP((old) => ({
      ...old,
      connectionId: id,
      gitlabUrl: connection?.gitlabUrl || old.gitlabUrl,
      mergeCredentialRef: id ? "" : old.mergeCredentialRef || "GITLAB_MERGE_TOKEN",
    }));
    if (p.repositories?.length) setSourceChanged(true);
    setWarnings([]);
    setGuidance("");
  }
  async function createConnection() {
    setError("");
    setSavingConnection(true);
    try {
      const gitlabUrl = p.gitlabUrl.trim();
      const verified = await api<{ username: string }>(
        "/connections/verify",
        "POST",
        {
          gitlabUrl,
          token: connectionToken,
          mergeToken: connectionMergeToken,
        },
      );
      const saved = await api<Connection>("/connections", "POST", {
        name: `${verified.username}@${gitlabUrl}`,
        gitlabUrl,
        token: connectionToken,
        mergeToken: connectionMergeToken,
      });
      setConnections((old) => [...old.filter((item) => item.id !== saved.id), saved]);
      setConnectionToken("");
      setConnectionMergeToken("");
      setAddingConnection(false);
      setP((old) => ({
        ...old,
        connectionId: saved.id,
        gitlabUrl: saved.gitlabUrl,
        mergeCredentialRef: "",
      }));
      if (p.repositories?.length) setSourceChanged(true);
      toast.success("GitLab 连接已保存。");
      setGuidance("请读取 Meta-Repo 清单。");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSavingConnection(false);
    }
  }
  async function discover() {
    setDiscovering(true);
    setError("");
    setGuidance("");
    setWarnings([]);
    try {
      const d = await api<Discovery>("/projects/discover", "POST", {
        metaUrl: p.metaUrl.trim(),
        metaRef: p.metaRef.trim(),
        gitlabUrl: p.gitlabUrl.trim(),
        connectionId: p.connectionId || "",
        credentialRef: p.credentialRef,
        defaultTargetBranch: defaultBranch.trim() || "dev",
      });
      const old = latestProject.current;
      const previous =
        old.gitlabUrl.replace(/\/$/, "") === d.gitlabUrl.replace(/\/$/, "")
          ? old.repositories || []
          : [];
      const ids = new Map(
        d.repositories.map((r) => [
          r.id,
          previous.find((v) => v.projectId === r.projectId)?.id || r.id,
        ]),
      );
      const next = d.repositories.map((r) => {
        const before = previous.find((v) => v.projectId === r.projectId);
        return {
          ...r,
          id: ids.get(r.id)!,
          targetBranch: before?.targetBranch || r.targetBranch,
          createFrom: before?.createFrom || "",
          sensitivePaths: before?.sensitivePaths || [],
          dependsOn: (r.dependsOn || []).map((id) => ids.get(id) || id),
        };
      });
      setBranches(
        Object.fromEntries(
          d.repositories
            .filter((r) => Object.hasOwn(d.branches, r.id))
            .map((r) => [ids.get(r.id)!, d.branches[r.id]]),
        ),
      );
      setP((current) => ({
        ...current,
        id: current.version ? current.id : d.suggestedId,
        name: current.name || d.name,
        metaUrl: d.metaUrl,
        metaRef: d.metaRef,
        gitlabUrl: d.gitlabUrl,
        repositories: next,
      }));
      setWarnings(d.warnings || []);
      setSourceChanged(false);
      setManual(false);
      toast.success(`已读取 ${d.repositories.length} 个仓库。`);
      setGuidance("已有目标分支设置已保留，保存后生效。");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setDiscovering(false);
    }
  }
  const repositories = p.repositories || [];
  const checks = p.integration.checks || [];
  const independentReview = (p.reviewMode || "independent") === "independent";
  const agentsReady = [p.development, ...(independentReview ? [p.review] : [])].every(
    (a) =>
      agentCatalog.enabled.includes(a.adapter) &&
      (a.image.trim()
        ? !p.nodes?.mode || registryDigest.test(a.image.trim())
        : !!p.nodes?.mode) &&
      a.timeoutSeconds > 0 &&
      a.timeoutSeconds <= 86400,
  );
  const repoReady =
    repositories.length > 0 &&
    !sourceChanged &&
    repositories.every(
      (r) =>
        r.id &&
        r.projectId > 0 &&
        r.url &&
        r.path &&
        r.targetBranch &&
        (!Object.hasOwn(branches, r.id) ||
          branches[r.id].includes(r.targetBranch) ||
          r.createFrom),
    );
  const strategy = p.acceptanceStrategy || "custom";
  const [acceptanceArgv, setAcceptanceArgv] = useState(() => (initial.integration.commands || []).map((c) => formatCommand(c.argv)));
  const customCommands = p.integration.mode === "commands";
  const composeReady =
    !!p.integration.composeFile.trim() &&
    checks.some((c) => c.required) &&
    checks.every((c, i) => {
      try {
        return (
          c.name.trim() &&
          c.service.trim() &&
          parseCommand(commands[i] || "").length > 0
        );
      } catch {
        return false;
      }
    });
  const commandsReady = !!p.integration.image?.trim() && !!p.integration.commands?.length && p.integration.commands.every((c, i) => {
    try { return !!c.name.trim() && !!c.path.trim() && parseCommand(acceptanceArgv[i] || "").length > 0; } catch { return false; }
  });
  const checksReady = strategy !== "custom" || (customCommands ? commandsReady : composeReady);
  const connectionReady = !!(
    p.name.trim() &&
    p.metaRef.trim() &&
    (github ? p.connectionId : p.gitlabUrl.trim()) &&
    (managed || p.metaUrl.trim())
  );
  const readiness = [
    { id: "connection", label: "Meta-Repo 信息", ok: connectionReady },
    { id: "repositories", label: "仓库与目标分支", ok: repoReady },
    { id: "execution", label: "执行器与运行环境", ok: agentsReady },
    { id: "verification", label: "额外验收（可选）", ok: checksReady },
  ];
  const ready = readiness.every((r) => r.ok);
  const saving = busy || discovering;
  async function save(enabled: boolean) {
    setError("");
    setGuidance("");
    if (!p.name.trim() || (!managed && !p.metaUrl.trim())) {
      toast.error(
        managed
          ? "请填写项目名称，再保存草稿。"
          : "请填写项目名称和 Meta-Repo 地址，再保存草稿。",
      );
      return;
    }
    if (enabled && !ready) {
      toast.error(
        "启用前还需完成：" +
          readiness
            .filter((v) => !v.ok)
            .map((v) => v.label)
            .join("、"),
      );
      return;
    }
    setBusy(true);
    try {
      const payload: Project = {
        ...p,
        name: p.name.trim(),
        metaUrl: p.metaUrl.trim(),
        enabled,
        integration: {
          ...p.integration,
          commands: strategy === "custom" && customCommands ? (p.integration.commands || []).map((c,i) => ({...c, argv: parseCommand(acceptanceArgv[i] || "")})) : p.integration.commands,
          checks: strategy === "custom" && !customCommands ? checks.map((c, i) => ({
            ...c,
            command: parseCommand(commands[i] || ""),
          })) : checks,
        },
        repositories: (sourceChanged ? [] : repositories).map((r) => ({
          ...r,
          dependsOn: lines((r.dependsOn || []).join("\n")),
          sensitivePaths: lines((r.sensitivePaths || []).join("\n")),
        })),
      };
      const { myRole: _myRole, ...projectBody } = payload;
      const result = await api<Project>(
        existing ? `/projects/${encodeURIComponent(p.id)}` : "/projects",
        existing ? "PUT" : "POST",
        existing ? projectBody : { ...projectBody, initialOwnerUserId },
      );
      setP(result);
      toast.success(enabled ? "项目配置已保存并启用。" : "草稿已保存，自动任务未启用。");
      onSaved(result);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="project-setup">
      <a className="back" href="#/projects">
        ← 返回项目配置
      </a>
      {managed && existing && <div className="notice">
        <span>初始化进度、验证结果和待补充的信息会持续保存。</span>{" "}
        <a className="button" href={`#/projects/${encodeURIComponent(p.id)}/initialization`}>查看初始化进度</a>
      </div>}
      <header className="page-head setup-heading">
        <div>
          <div className="eyebrow">WORKSPACE SETUP</div>
          <h1>{existing ? p.name : "连接你的开发工作区"}</h1>
          <p>{managed ? "管理参与开发的仓库；系统维护 Meta 清单。" : "从已有 Meta-Repo 读取仓库信息。先保存，准备好后再开启自动开发。"}</p>
        </div>
        <span className={`status ${p.enabled ? "completed" : "paused"}`}>
          {p.enabled ? "已启用" : "草稿 · 未启用"}
        </span>
      </header>
      {existing && p.enabled && <ProjectLabels projectId={p.id} provider={p.provider} />}
      <form
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void save(p.enabled);
        }}
      >
        <ErrorBox error={error} />
        {guidance && (
          <div className="notice" role="status">
            {guidance}
          </div>
        )}
        {!existing && <div className="panel panel-padding"><Field label="初始负责人（可选）" hint="留空由当前平台管理员管理；以后可在项目成员中指定负责人。"><select value={initialOwnerUserId} onChange={event => setInitialOwnerUserId(event.target.value)}><option value="">默认：当前平台管理员</option>{activeUsers.map(user => <option key={user.id} value={user.id}>{user.displayName || user.email}</option>)}</select></Field></div>}
        <nav className="setup-steps" aria-label="配置步骤">
          {platformAdmin && <>
          <a
            href="#connection"
            onClick={(e) => {
              e.preventDefault();
              document
                .getElementById("connection")
                ?.scrollIntoView({ behavior: "smooth" });
            }}
          >
            <span>01</span>连接 Meta-Repo
          </a>
          <a
            href="#repositories"
            onClick={(e) => {
              e.preventDefault();
              document
                .getElementById("repositories")
                ?.scrollIntoView({ behavior: "smooth" });
            }}
          >
            <span>02</span>确认目标分支
          </a>
          <a
            href="#execution"
            onClick={(e) => {
              e.preventDefault();
              document
                .getElementById("execution")
                ?.scrollIntoView({ behavior: "smooth" });
            }}
          >
            <span>03</span>执行与验收
          </a>
          </>}
          {!platformAdmin && <a href="#execution"><span>01</span>执行与验收</a>}
        </nav>
        <div className="setup-layout">
          <div className="setup-content">
            {platformAdmin && <>
            <section className="panel setup-section" id="connection">
              <div className="setup-section-head">
                <div>
                  <h2>连接 Meta-Repo</h2>
                  <p>提供仓库地址，其余信息交给系统读取。</p>
                </div>
                <span className="setup-label">必填</span>
              </div>
              <div className="panel-padding">
                {managed && (
                  <div className="setup-warning" role="status">
                    这是系统托管的 Meta-Repo：仓库清单与说明由初始化生成，不需要填写
                    Meta-Repo 地址。
                  </div>
                )}
                {!managed && (
                  <>
                    <div className="form-grid">
                      <Field
                        label="GitLab 连接"
                        hint="可选已保存的连接；旧项目也可继续使用部署环境变量。"
                      >
                        <select
                          value={p.connectionId || ""}
                          onChange={(e) => selectConnection(e.target.value)}
                        >
                          <option value="">部署环境变量</option>
                          {connections.filter(connection => connection.provider !== "github").map((connection) => (
                            <option key={connection.id} value={connection.id}>
                              {connection.name} · {connection.gitlabUrl}
                            </option>
                          ))}
                        </select>
                      </Field>
                      <button
                        type="button"
                        onClick={() => setAddingConnection((old) => !old)}
                      >
                        {addingConnection ? "取消新增连接" : "新增 GitLab 连接"}
                      </button>
                    </div>
                    {addingConnection && (
                      <div className="form-grid">
                        <Field label="新连接的 GitLab 地址">
                          <input
                            value={p.gitlabUrl}
                            onChange={(e) => changeSource("gitlabUrl", e.target.value)}
                            placeholder="https://gitlab.example.com"
                          />
                        </Field>
                        <Field label="Access Token">
                          <input
                            type="password"
                            value={connectionToken}
                            onChange={(e) => setConnectionToken(e.target.value)}
                            autoComplete="off"
                          />
                        </Field>
                        <Field label="合并 Token（可选）">
                          <input
                            type="password"
                            value={connectionMergeToken}
                            onChange={(e) => setConnectionMergeToken(e.target.value)}
                            autoComplete="off"
                          />
                        </Field>
                        <button
                          type="button"
                          className="primary"
                          disabled={savingConnection || !p.gitlabUrl.trim() || !connectionToken.trim()}
                          onClick={createConnection}
                        >
                          {savingConnection ? "正在验证…" : "验证并保存连接"}
                        </button>
                      </div>
                    )}
                    <div className="meta-import-row">
                      <Field label="Meta-Repo 地址">
                        <input
                          value={p.metaUrl}
                          disabled={discovering}
                          onChange={(e) => changeSource("metaUrl", e.target.value)}
                          placeholder="https://gitlab.example.com/team/project-meta.git"
                          autoComplete="off"
                        />
                      </Field>
                      <button
                        type="button"
                        className="primary"
                        disabled={saving || !p.metaUrl.trim()}
                        onClick={discover}
                      >
                        {discovering
                          ? "正在读取…"
                          : repositories.length
                            ? "重新读取清单"
                            : "读取仓库清单"}
                      </button>
                    </div>
                  </>
                )}
                <div className="form-grid">
                  <Field
                    label="项目名称"
                    hint="在控制台显示的名称，读取后会自动填写。"
                  >
                    <input
                      value={p.name}
                      onChange={(e) => set("name", e.target.value)}
                      placeholder="例如：数字员工"
                    />
                  </Field>
                  <Field label="Meta 分支" hint="留空时读取该仓库的默认分支。">
                    <input
                      value={p.metaRef}
                      disabled={discovering}
                      onChange={(e) => changeSource("metaRef", e.target.value)}
                      placeholder="自动识别"
                    />
                  </Field>
                </div>
                <details className="setup-advanced">
                  <summary>
                    连接与凭据设置<span>通常无需修改</span>
                  </summary>
                  <div className="form-grid">
                    {github ? <Field label="GitHub 连接"><input value={p.connectionId || ""} readOnly /></Field> : <Field
                      label="GitLab 地址"
                      hint="从 HTTP 地址自动识别；SSH 地址或子路径部署可在此指定。"
                    >
                      <input
                        value={p.gitlabUrl}
                        disabled={discovering}
                        onChange={(e) =>
                          changeSource("gitlabUrl", e.target.value)
                        }
                      />
                    </Field>}
                    <Field label="项目标识" hint="系统自动生成，用于内部关联。">
                      <input value={p.id} readOnly disabled={existing} />
                    </Field>
                    {!p.connectionId && <Field
                      label="开发凭据引用"
                      hint="服务器环境变量名称，不是 Token。"
                    >
                      <input
                        value={p.credentialRef}
                        onChange={(e) => set("credentialRef", e.target.value)}
                      />
                    </Field>}
                    {!p.connectionId && <Field label="合并凭据引用">
                      <input
                        value={p.mergeCredentialRef}
                        onChange={(e) =>
                          set("mergeCredentialRef", e.target.value)
                        }
                      />
                    </Field>}
                  </div>
                </details>
              </div>
            </section>
            <section className="panel setup-section" id="repositories">
              <div className="setup-section-head">
                <div>
                  <h2>仓库与目标分支</h2>
                  <p>
                    {repositories.length
                      ? `${repositories.length} 个仓库。通常只需确认 ${changeName} 合并到哪里。`
                      : "读取清单后，仓库 ID、地址、路径和依赖会自动填入。"}
                  </p>
                </div>
                {repositories.length > 0 && (
                  <span className="setup-label">来自 Meta-Repo</span>
                )}
              </div>
              <div className="panel-padding">
                {sourceChanged && (
                  <div className="setup-warning" role="status">
                    Meta-Repo
                    来源已更改。重新读取可保留匹配的分支设置；直接保存草稿会清除旧清单。
                  </div>
                )}
                {warnings.length > 0 && (
                  <div className="setup-warning">
                    <strong>读取提示</strong>
                    <ul>
                      {warnings.map((w, i) => (
                        <li key={i}>{w}</li>
                      ))}
                    </ul>
                  </div>
                )}
                {repositories.length > 0 ? (
                  <>
                    <div className="branch-bulk">
                      <Field label="统一目标分支">
                        <input
                          value={defaultBranch}
                          onChange={(e) => setDefaultBranch(e.target.value)}
                          placeholder="dev"
                        />
                      </Field>
                      <button
                        type="button"
                        disabled={!defaultBranch.trim()}
                        onClick={() =>
                          set(
                            "repositories",
                            repositories.map((r) => ({
                              ...r,
                              targetBranch: defaultBranch.trim(),
                            })),
                          )
                        }
                      >
                        应用到所有仓库
                      </button>
                      <span>也可以逐个调整</span>
                    </div>
                    <div className="repository-list">
                      {repositories.map((r, i) => {
                        const missing =
                          !!r.targetBranch &&
                          Object.hasOwn(branches, r.id) &&
                          !branches[r.id].includes(r.targetBranch);
                        return (
                          <article
                            className="repository-item"
                            key={`${i}-${r.projectId}`}
                          >
                            <div className="repository-main">
                              <div className="repository-identity">
                                <strong>{r.id || `仓库 ${i + 1}`}</strong>
                                <span>{r.path || "待填写仓库路径"}</span>
                                {(r.dependsOn || []).length > 0 && (
                                  <small>
                                    依赖 {(r.dependsOn || []).join("、")}
                                  </small>
                                )}
                              </div>
                              <div className="repository-target">
                                <Field
                                  label={`${r.id || `仓库 ${i + 1}`} 的目标分支`}
                                >
                                  <input
                                    value={r.targetBranch}
                                    list={`branches-${i}`}
                                    onChange={(e) =>
                                      updateRepo(i, {
                                        targetBranch: e.target.value,
                                      })
                                    }
                                    placeholder="dev"
                                  />
                                </Field>
                                <datalist id={`branches-${i}`}>
                                  {(branches[r.id] || []).map((b) => (
                                    <option key={b} value={b} />
                                  ))}
                                </datalist>
                                {missing && (
                                  <small className="branch-warning">
                                    该分支尚不存在，需要指定创建起点。
                                  </small>
                                )}
                              </div>
                            </div>
                            {missing && (
                              <div className="branch-initialization">
                                <Field
                                  label={`${r.id} 的创建起点`}
                                  hint="仅首次创建目标分支时使用；请明确选择现有分支或提交。"
                                >
                                  <input
                                    value={r.createFrom || ""}
                                    list={`branches-${i}`}
                                    onChange={(e) =>
                                      updateRepo(i, {
                                        createFrom: e.target.value,
                                      })
                                    }
                                    placeholder="选择现有分支或填写提交 SHA"
                                  />
                                </Field>
                              </div>
                            )}
                            <details className="repository-details">
                              <summary>仓库信息与高级设置</summary>
                              <div className="repository-facts">
                                <span>
                                  {platformName} ID
                                  <strong>{r.projectId || "尚未识别"}</strong>
                                </span>
                                <span>
                                  仓库标识<strong>{r.id || "尚未识别"}</strong>
                                </span>
                                <span className="repository-url">
                                  仓库地址<strong>{r.url || "尚未填写"}</strong>
                                </span>
                              </div>
                              <p className="muted">
                                标识用于关联依赖和任务记录，路径来自 Meta
                                清单，通常无需修改。
                              </p>
                              {!missing && (
                                <Field
                                  label="初始化引用（可选）"
                                  hint="只有目标分支不存在时才使用。"
                                >
                                  <input
                                    value={r.createFrom || ""}
                                    onChange={(e) =>
                                      updateRepo(i, {
                                        createFrom: e.target.value,
                                      })
                                    }
                                  />
                                </Field>
                              )}
                              <Field
                                label="额外敏感路径"
                                hint="标记项目特有的高风险路径，供 Agent 审查与报告使用；每行一项。"
                              >
                                <textarea
                                  rows={2}
                                  value={(r.sensitivePaths || []).join("\n")}
                                  onChange={(e) =>
                                    updateRepo(i, {
                                      sensitivePaths: lines(e.target.value),
                                    })
                                  }
                                />
                              </Field>
                              {manual && (
                                <div className="form-grid">
                                  <Field label={`仓库 ${i + 1} 标识`}>
                                    <input
                                      value={r.id}
                                      onChange={(e) =>
                                        updateRepo(i, { id: e.target.value })
                                      }
                                    />
                                  </Field>
                                  <Field label={`仓库 ${i + 1} ${platformName} ID`}>
                                    <input
                                      type="number"
                                      min={1}
                                      value={r.projectId || ""}
                                      onChange={(e) =>
                                        updateRepo(i, {
                                          projectId: Number(e.target.value),
                                        })
                                      }
                                    />
                                  </Field>
                                  <Field label={`仓库 ${i + 1} URL`}>
                                    <input
                                      value={r.url}
                                      onChange={(e) =>
                                        updateRepo(i, { url: e.target.value })
                                      }
                                    />
                                  </Field>
                                  <Field label={`仓库 ${i + 1} 路径`}>
                                    <input
                                      value={r.path}
                                      onChange={(e) =>
                                        updateRepo(i, { path: e.target.value })
                                      }
                                    />
                                  </Field>
                                  <Field label={`仓库 ${i + 1} 依赖标识`}>
                                    <textarea
                                      value={(r.dependsOn || []).join("\n")}
                                      onChange={(e) =>
                                        updateRepo(i, {
                                          dependsOn: lines(e.target.value),
                                        })
                                      }
                                    />
                                  </Field>
                                  <button
                                    className="danger-text"
                                    type="button"
                                    onClick={() =>
                                      set(
                                        "repositories",
                                        repositories.filter(
                                          (_, idx) => idx !== i,
                                        ),
                                      )
                                    }
                                  >
                                    移除此仓库
                                  </button>
                                </div>
                              )}
                            </details>
                          </article>
                        );
                      })}
                    </div>
                  </>
                ) : (
                  <div className="repository-empty">
                    <span
                      className="repository-empty-symbol"
                      aria-hidden="true"
                    >
                      ↳
                    </span>
                    <strong>不需要逐项填写仓库信息</strong>
                    <p>{managed ? "初始化完成后，已选择的仓库会在这里显示。" : "在上方读取 Meta-Repo 清单，即可批量导入所有仓库。"}</p>
                  </div>
                )}
                <details className="setup-advanced">
                  <summary>
                    手动配置仓库<span>用于暂时无法读取清单的情况</span>
                  </summary>
                  <label className="checkbox">
                    <input
                      type="checkbox"
                      checked={manual}
                      disabled={github}
                      onChange={(e) => setManual(e.target.checked)}
                    />
                    允许手动编辑仓库信息
                  </label>
                  <p className="muted">
                    启用后请展开对应仓库的高级设置。标识、路径及依赖应与 Meta
                    清单保持一致。
                  </p>
                  {!existing && <button
                    type="button"
                    disabled={!manual}
                    onClick={() =>
                      set("repositories", [
                        ...repositories,
                        {
                          id: `repo-${repositories.length + 1}`,
                          projectId: 0,
                          url: "",
                          path: "",
                          targetBranch: defaultBranch,
                          createFrom: "",
                          dependsOn: [],
                          sensitivePaths: [],
                        },
                      ])
                    }
                  >
                    ＋ 添加仓库
                  </button>}
                </details>
                {existing && !github && <AddRepository project={p} onApplied={(updated) => { setP(updated); onSaved(updated); }} />}
              </div>
            </section>
            </>}
            {!platformAdmin && <section className="panel setup-section"><div className="setup-section-head"><h2>项目基本信息</h2></div><div className="panel-padding"><Field label="项目名称"><input value={p.name} onChange={event => set("name", event.target.value)} required /></Field></div></section>}
            <section className="panel setup-section" id="execution">
              <div className="setup-section-head">
                <div>
                  <h2>Runner 与 Agent</h2>
                  <p>{independentReview ? "开发与独立审查使用不同会话。" : "开发、返工与合并沿用开发 Agent 会话。"}</p>
                  {platformAdmin && <Field label="执行节点范围"><select value={p.nodes?.mode||'auto'} onChange={e=>setP({...p,nodes:{mode:e.target.value,ids:[]}})}><option value="auto">所有符合条件的节点</option><option value="selected">指定节点</option></select></Field>}
                  {platformAdmin && p.nodes?.mode==='selected'&&nodes.map(n=><label className="checkbox" key={n.node.id}><input type="checkbox" checked={p.nodes?.ids?.includes(n.node.id)||false} onChange={e=>setP({...p,nodes:{mode:'selected',ids:e.target.checked?[...(p.nodes?.ids||[]),n.node.id]:(p.nodes?.ids||[]).filter(id=>id!==n.node.id)}})}/>{n.config?.name||n.node.id}</label>)}
                </div>
                <span className="setup-label">可稍后补充</span>
              </div>
              <div className="panel-padding">
                <p>Issue 使用 <code>agent:opencode</code>、<code>agent:codex</code> 等标签覆盖默认 Agent，统一作用于开发、审查和合并。模型使用 CLI 保存的默认配置。</p>
                <a className="button" href="#/runtime">启用 Agent 与终端配置 →</a>
                {agentCatalogError && <p role="alert">{agentCatalogError}</p>}
                <Field label="项目默认 Agent">
                  <select value={p.development.adapter} onChange={(e)=>{
                    const adapter=e.target.value;
                    setP((old)=>({...old,development:{...old.development,adapter,model:""},review:{...old.review,adapter,model:""}}));
                    setShareReview(true);
                  }}>
                    <option value="">请选择已启用的 Agent</option>
                    {!agentCatalog.enabled.includes(p.development.adapter) && p.development.adapter && <option value={p.development.adapter} disabled>{p.development.adapter}（未启用）</option>}
                    {agentCatalog.agents.filter((a)=>agentCatalog.enabled.includes(a.id)).map((a)=><option key={a.id} value={a.id}>{a.name}</option>)}
                  </select>
                </Field>
                <Field label="独立审查">
                  <select value={p.reviewMode || "independent"} onChange={(e) => set("reviewMode", e.target.value as Project["reviewMode"])}>
                    <option value="none">关闭（默认）</option>
                    <option value="independent">启动独立审查 Agent</option>
                  </select>
                </Field>
                {independentReview && <label className="checkbox">
                  <input
                    type="checkbox"
                    checked={shareReview}
                    onChange={(e) => {
                      setShareReview(e.target.checked);
                      if (e.target.checked) set("review", { ...p.development });
                    }}
                  />
                  审查沿用开发执行环境
                </label>}
                <details className="setup-advanced" ref={runnerSettings}>
                  <summary>
                    执行环境设置<span>镜像与超时已有默认值</span>
                  </summary>
                  <div className="form-grid">
                    {(
                      ["development", ...(independentReview && !shareReview ? ["review"] : [])] as (
                        "development" | "review"
                      )[]
                    ).map((role) => {
                      const label = role === "development" ? "开发" : "审查";
                      return (
                        <div key={role}>
                          <Field
                            label={`${label} Runner 镜像`}
                            hint="使用 Node 默认镜像可直接继承该节点已就绪的版本；指定版本需先在 Node 上准备。"
                          >
                            <select value={manualImages[role] ? "__manual__" : p[role].image} onChange={(e) => {
                              if (e.target.value === "__manual__") {
                                setManualImages((old) => ({ ...old, [role]: true }));
                              } else {
                                setManualImages((old) => ({ ...old, [role]: false }));
                                updateAgent(role, { image: e.target.value });
                              }
                            }}>
                              <option value="">使用 Node 默认镜像（推荐）</option>
                              {p[role].image && !registryImages.some((image) => image.reference === p[role].image) && (
                                <option value={p[role].image}>当前配置（未在仓库列表）：{p[role].image}</option>
                              )}
                              {registryImages.map((image) => (
                                <option key={image.digest} value={image.reference}>
                                  {image.tags.join("、") || "未命名版本"} · {image.digest.slice(0, 19)}…
                                </option>
                              ))}
                              <option value="__manual__">手动填写镜像摘要…</option>
                            </select>
                          </Field>
                          {manualImages[role] && <Field label={`${label}镜像摘要`}>
                            <input value={p[role].image} placeholder="registry.example.com/swarmjet/agent@sha256:…"
                              onChange={(e) => updateAgent(role, { image: e.target.value })} />
                          </Field>}
                          {p[role].image && !registryDigest.test(p[role].image) && p.nodes?.mode && (
                            <p className="muted">当前值不是仓库镜像摘要，Node 无法使用它接单；请选择默认镜像或已发布版本。</p>
                          )}
                          <Field label={`${label}超时（分钟）`}>
                            <input
                              type="number"
                              min={0.5}
                              max={1440}
                              step={0.5}
                              value={p[role].timeoutSeconds / 60}
                              onChange={(e) =>
                                updateAgent(role, {
                                  timeoutSeconds: Math.round(
                                    Number(e.target.value) * 60,
                                  ),
                                })
                              }
                            />
                          </Field>
                        </div>
                      );
                    })}
                  </div>
                  {registryError && <p className="muted" role="alert">镜像列表读取失败：{registryError}</p>}
                  {!registryError && !registryLoading && registryImages.length === 0 && <p className="muted">仓库暂无已发布镜像，仍可使用 Node 默认镜像。</p>}
                  <button type="button" disabled={registryLoading} onClick={() => void loadRegistryImages()}>
                    {registryLoading ? "读取镜像中…" : "刷新镜像列表"}
                  </button>
                </details>
              </div>
            </section>
            <section className="panel setup-section" id="verification">
              <div className="setup-section-head">
                <div>
                  <h2>额外验收（可选）</h2>
                  <p>默认关闭平台重复执行的检查；需要独立运行命令或 CI 时再开启。开发 Agent 仍自行测试。</p>
                </div>
                <span className="setup-label">可稍后补充</span>
              </div>
              <div className="panel-padding">
                <Field label="验收策略">
                  <select value={strategy} onChange={(e) => { setP((old) => ({...old, acceptanceStrategy:e.target.value as Project["acceptanceStrategy"], requireCI:e.target.value === "gitlab_ci" || e.target.value === "platform_ci"})); }}>
                    <option value="none">关闭额外验收（默认）</option>
                    <option value="repository">根据仓库约定验收</option>
                    {github ? <option value="platform_ci">目标分支必需检查</option> : <option value="gitlab_ci">使用 GitLab CI</option> }
                    <option value="custom">自定义验收（高级）</option>
                  </select>
                </Field>
                <p className="notice">{strategy === "platform_ci" ? "先发布 PR，再验证当前提交满足目标分支的必需检查；没有必需检查时不会判为通过。" : strategy === "none" ? `开发 Agent 自行运行测试并留下日志；平台跳过额外验收，${independentReview ? "仍执行独立审查与平台合并检查" : "继续执行平台合并检查"}。` : strategy === "repository" ? `Agent 从仓库说明、脚本和 CI 提出针对本次改动的检查，平台执行并记录结果；缺少工具或检查时保留 ${changeName}，交由人工处理，不自动合并。` : strategy === "gitlab_ci" ? "先创建 MR，再等待每个变更仓库当前提交的 CI。旧提交成功、跳过或不存在的流水线不算通过。" : "明确指定验收命令，或通过 Compose 启动多个服务后执行检查。"}</p>
                {strategy === "repository" && <Field label="验收镜像（可选）" hint="默认使用开发 Agent 镜像；镜像需要包含项目工具链。"><input value={p.integration.image || ""} placeholder={p.development.image} onChange={(e) => set("integration", {...p.integration, image:e.target.value})} /></Field>}
                {strategy === "custom" && <>
                  <Field label="自定义验收方式"><select value={p.integration.mode || "compose"} onChange={(e) => set("integration", {...p.integration, mode:e.target.value as "compose" | "commands"})}><option value="commands">隔离容器命令</option><option value="compose">Compose 联调</option></select></Field>
                  {customCommands ? <>
                    <Field label="验收镜像" hint="准备包含构建和测试工具链的镜像，命令在独立容器运行。"><input value={p.integration.image || ""} onChange={(e) => set("integration", {...p.integration,image:e.target.value})} /></Field>
                    {(p.integration.commands || []).map((command,index) => <fieldset className="setup-check" key={index}>
                      <legend>验收命令 {index+1}</legend>
                      <Field label="名称"><input value={command.name} onChange={(e) => set("integration",{...p.integration,commands:p.integration.commands!.map((c,i) => i===index?{...c,name:e.target.value}:c)})} /></Field>
                      <Field label="工作目录" hint="相对 Meta 工作区，例如 issue-bot。"><input value={command.path} onChange={(e) => set("integration",{...p.integration,commands:p.integration.commands!.map((c,i) => i===index?{...c,path:e.target.value}:c)})} /></Field>
                      <Field label="执行命令" hint="直接执行程序及参数；需要 shell 语法时显式使用 sh -c。"><input value={acceptanceArgv[index] || ""} onChange={(e) => setAcceptanceArgv((old) => old.map((c,i) => i===index?e.target.value:c))} /></Field>
                      <button type="button" onClick={() => {set("integration",{...p.integration,commands:p.integration.commands!.filter((_,i) => i!==index)});setAcceptanceArgv((old) => old.filter((_,i) => i!==index));}}>删除命令</button>
                    </fieldset>)}
                    <button type="button" onClick={() => {set("integration",{...p.integration,commands:[...(p.integration.commands || []),{name:"",path:".",argv:[],evidence:"项目自定义验收"}]});setAcceptanceArgv((old)=>[...old,""]);}}>＋ 添加验收命令</button>
                  </> : <>
                <Field
                  label="联调配置文件"
                  hint="相对于 Meta-Repo，例如 integration/http-check/compose.yaml。"
                >
                  <input
                    value={p.integration.composeFile}
                    placeholder="integration/compose.yaml"
                    onChange={(e) =>
                      set("integration", {
                        ...p.integration,
                        composeFile: e.target.value,
                      })
                    }
                  />
                </Field>
                {checks.map((c, i) => (
                  <fieldset className="setup-check" key={i}>
                    <legend>检查 {i + 1}</legend>
                    <div className="form-grid">
                      <Field label={`检查 ${i + 1} 名称`}>
                        <input
                          value={c.name}
                          placeholder="例如：接口联调"
                          onChange={(e) =>
                            updateCheck(i, { name: e.target.value })
                          }
                        />
                      </Field>
                      <Field
                        label={`检查 ${i + 1} 服务`}
                        hint="Compose 中执行检查的服务名称。"
                      >
                        <input
                          value={c.service}
                          placeholder="例如：verifier"
                          onChange={(e) =>
                            updateCheck(i, { service: e.target.value })
                          }
                        />
                      </Field>
                    </div>
                    <Field
                      label={`检查 ${i + 1} 命令`}
                      hint="支持带引号的参数；多步检查请调用脚本。"
                    >
                      <textarea
                        rows={2}
                        className="mono"
                        value={commands[i] || ""}
                        placeholder="python /verify.py"
                        onChange={(e) => {
                          setCommands((old) =>
                            old.map((v, idx) =>
                              idx === i ? e.target.value : v,
                            ),
                          );
                          setGuidance("");
                        }}
                      />
                    </Field>
                    <div className="row-between">
                      <label className="checkbox">
                        <input
                          type="checkbox"
                          checked={c.required}
                          onChange={(e) =>
                            updateCheck(i, { required: e.target.checked })
                          }
                        />
                        通过后才能自动合并
                      </label>
                      <button
                        type="button"
                        className="danger-text"
                        onClick={() => {
                          set("integration", {
                            ...p.integration,
                            checks: checks.filter((_, idx) => idx !== i),
                          });
                          setCommands((old) =>
                            old.filter((_, idx) => idx !== i),
                          );
                        }}
                      >
                        移除检查 {i + 1}
                      </button>
                    </div>
                  </fieldset>
                ))}
                <button
                  type="button"
                  onClick={() => {
                    set("integration", {
                      ...p.integration,
                      checks: [
                        ...checks,
                        { name: "", service: "", command: [], required: true },
                      ],
                    });
                    setCommands((old) => [...old, ""]);
                  }}
                >
                  ＋ 添加验收检查
                </button>
                  </>}
                </>}
                <details className="setup-advanced">
                  <summary>
                    运行与合并策略<span>保留默认策略即可</span>
                  </summary>
                  <div className="form-grid">
                    <Field label="环境启动超时（秒）">
                      <input
                        type="number"
                        min={1}
                        value={p.integration.startupTimeoutSeconds}
                        onChange={(e) =>
                          set("integration", {
                            ...p.integration,
                            startupTimeoutSeconds: Number(e.target.value),
                          })
                        }
                      />
                    </Field>
                    <Field label="最大返工次数">
                      <input
                        type="number"
                        min={0}
                        max={20}
                        value={p.maxReworks}
                        onChange={(e) =>
                          set("maxReworks", Number(e.target.value))
                        }
                      />
                    </Field>
                  </div>
                  <label className="checkbox">
                    <input
                      type="checkbox"
                      checked={p.requireCI}
                      onChange={(e) => set("requireCI", e.target.checked)}
                    />
                    要求 {platformName} 当前提交的 CI 通过
                  </label>
                  <p className="muted">{platformName} 合并检查仍会执行；独立审查与额外验收按项目配置运行。</p>
                </details>
              </div>
            </section>
          </div>
          {platformAdmin && <aside className="setup-readiness" aria-label="启用前检查">
            <div className="eyebrow">接入进度</div>
            <h2>{ready ? "配置已齐全" : "按你的节奏完成"}</h2>
            <p>
              {ready
                ? "保存并启用后，系统才会开始接收自动任务。"
                : "草稿随时保存。剩余配置可以之后继续补充。"}
            </p>
            <ul>
              {readiness.map((item) => (
                <li key={item.id}>
                  <span
                    className={item.ok ? "readiness-done" : "readiness-pending"}
                    aria-hidden="true"
                  >
                    {item.ok ? "✓" : "○"}
                  </span>
                  <a
                    href={`#${item.id}`}
                    onClick={(e) => {
                      e.preventDefault();
                      document
                        .getElementById(item.id)
                        ?.scrollIntoView({ behavior: "smooth" });
                    }}
                  >
                    {item.label}
                  </a>
                  <small>{item.ok ? "已填写" : "待完善"}</small>
                </li>
              ))}
            </ul>
            <div className="setup-reassurance">
              <strong>什么是仓库标识？</strong>
              <p>
                它是系统内部使用的短名称，用于关联依赖和任务。读取 Meta
                清单时会自动生成，无需单独维护。
              </p>
            </div>
          </aside>}
        </div>
        <section className="panel setup-section" id="merge-policy">
          <div className="setup-section-head">
            <div>
              <h2>合并策略</h2>
              <p>项目级自动合并开关；配置变更对新任务生效。</p>
            </div>
          </div>
          <div className="panel-padding">
            {github && <Field label="GitHub 合并方式"><select value={p.mergeMethod || "merge"} onChange={e => set("mergeMethod", e.target.value as Project["mergeMethod"])}><option value="merge">Merge commit</option><option value="squash">Squash</option><option value="rebase">Rebase</option></select></Field>}
            <div className="wizard-toggle">
              <label>
                <input
                  type="checkbox"
                  checked={p.autoMerge ?? true}
                  onChange={(e) => set("autoMerge", e.target.checked)}
                />
                <span>
                  <strong>自动合并</strong>
                  <small>
                    此设置是新任务默认值，任务详情可在合并前单独调整。关闭后系统仍会开发和创建 {changeName}，但不会发起合并；需要在
                    {platformName} 人工合并全部 {changeName} 后该轮任务完成，来源 Issue 保持打开。
                  </small>
                </span>
              </label>
              <p className="wizard-hint">
                {(p.autoMerge ?? true)
                  ? `${strategy === "none" ? "" : "已启用的额外验收、"}${independentReview ? "独立审查、" : ""}已启用的 ${platformName} CI 要求和目标分支检查通过后自动合并；冲突会交给开发 Agent 返工。`
                  : `当前为人工合并；所有 ${changeName} 合并后该轮任务完成，来源 Issue 保持打开。`}
              </p>
            </div>
          </div>
        </section>
        <footer className="save-bar setup-save">
          <span>
            {existing
              ? `配置版本 v${p.version} · 修改在保存后生效`
              : "草稿不会触发自动任务"}
          </span>
          <div className="actions">
            <button
              type="button"
              disabled={saving}
              onClick={() => void save(false)}
            >
              {busy ? "保存中…" : p.enabled ? "停用并保存" : "保存草稿"}
            </button>
            <button
              type="button"
              className="primary"
              disabled={saving || !ready}
              onClick={() => void save(true)}
            >
              {p.enabled ? "保存更改" : "保存并启用"}
            </button>
          </div>
        </footer>
      </form>
    </div>
  );
}
