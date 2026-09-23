import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, APIError } from "./api";
import { useAgentCatalog } from "./agents";
import { ErrorBox, Field } from "./App";
import { MetaPublication } from "./MetaPublication";
import { TerminalPanel } from "./TerminalPanel";
import type {
  Connection,
  ConnectionRepository,
  Initialization,
  Project,
  Repository,
} from "./types";
import "./wizard.css";

const stepList = [
  { key: "connection", label: "连接 GitLab" },
  { key: "repositories", label: "选择子仓库" },
  { key: "confirm", label: "确认项目" },
  { key: "opencode", label: "配置 OpenCode" },
  { key: "initialize", label: "自动初始化" },
];

type RepositoryResponse = {
  id: number;
  name: string;
  path_with_namespace: string;
  default_branch: string | null;
  http_url_to_repo: string;
  web_url: string;
  archived: boolean;
  description: string | null;
};

type VerifyResult = {
  username: string;
  name: string;
  fingerprint: string;
  hasMergeToken: boolean;
};

function safeSegment(value: string): string {
  const cleaned = value
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[-._]+|[-._]+$/g, "");
  return cleaned || "repo";
}

function slug(value: string): string {
  const base = value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
  return (base || "project") + "-" + Math.random().toString(36).slice(2, 8);
}

// Unique working paths and stable IDs for the Meta manifest. The last path
// segment keeps the checkout shallow; collisions get a numeric suffix.
function selectionLayout(repos: ConnectionRepository[]): {
  ids: Record<number, string>;
  paths: Record<number, string>;
} {
  const ids: Record<number, string> = {};
  const paths: Record<number, string> = {};
  const usedPaths = new Set<string>();
  const usedIDs = new Set<string>();
  for (const repo of repos) {
    const base = safeSegment(
      repo.pathWithNamespace.split("/").pop() || repo.name,
    );
    let path = base;
    for (let n = 2; usedPaths.has(path); n++) path = base + "-" + n;
    usedPaths.add(path);
    paths[repo.id] = path;
    let id = base.replace(/[^a-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 60);
    if (!/^[a-zA-Z0-9]/.test(id)) id = "r-" + id;
    let candidate = id;
    for (let n = 2; usedIDs.has(candidate); n++)
      candidate = (id + "-" + n).slice(0, 80);
    usedIDs.add(candidate);
    ids[repo.id] = candidate;
  }
  return { ids, paths };
}

function agent(model: string, image: string) {
  return { adapter: "opencode", model, image, timeoutSeconds: 5400 };
}

export default function ProjectWizard({
  onSaved,
  onOpenProject,
  initialProject,
}: {
  initialProject?: Project;
  onSaved?: (p: Project) => void;
  onOpenProject: (id: string) => void;
}) {
  const [step, setStep] = useState(initialProject ? 4 : 0);
  const [error, setError] = useState("");
  const [defaultImage, setDefaultImage] = useState("swarmjet-agent:local");
  const [connections, setConnections] = useState<Connection[]>([]);
  const [connectionId, setConnectionId] = useState("");
  const [gitlabUrl, setGitlabUrl] = useState("");
  const [token, setToken] = useState("");
  const [mergeToken, setMergeToken] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [verifyResult, setVerifyResult] = useState<VerifyResult | null>(null);
  const [repos, setRepos] = useState<ConnectionRepository[]>([]);
  const [repoQuery, setRepoQuery] = useState("");
  const [repoPage, setRepoPage] = useState(1);
  const [repoHasNext, setRepoHasNext] = useState(false);
  const [loadingRepos, setLoadingRepos] = useState(false);
  const [selected, setSelected] = useState<number[]>([]);
  const [repositoryCache, setRepositoryCache] = useState<Record<number, ConnectionRepository>>({});
  const repositoryRequest = useRef(0);
  const [targets, setTargets] = useState<Record<number, string>>({});
  const [name, setName] = useState("");
  const [metaRef, setMetaRef] = useState("main");
  const [autoMerge, setAutoMerge] = useState(false);
  const [projectId, setProjectId] = useState(initialProject?.id || "");
  const [runtime, setRuntime] = useState<{
    available: boolean;
    opencode: { installed: boolean; configured: boolean; providers: string[] | null };
    message?: string;
  } | null>(null);
  const [runtimeError, setRuntimeError] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [model, setModel] = useState("");
  const {catalog:agentCatalog,error:agentCatalogError} = useAgentCatalog();
  const [defaultAgent,setDefaultAgent] = useState(initialProject?.development.adapter || "");
  const [loadingModels, setLoadingModels] = useState(false);
  const [verifyingModel, setVerifyingModel] = useState(false);
  const [modelResult, setModelResult] = useState("");
  const [modelVerified, setModelVerified] = useState("");
  const [saving, setSaving] = useState(false);
  const [init, setInit] = useState<Initialization | null>(null);
  const initRef = useRef<Initialization | null>(null);
  initRef.current = init;
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const tokenRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    api<{ agentImage: string;distributed?:boolean }>("/project-defaults")
      .then((d) => setDefaultImage(d.agentImage))
      .catch(() => {
        /* Local default remains valid. */
      });
    api<Connection[] | null>("/connections")
      .then((list) => setConnections(list || []))
      .catch(() => setConnections([]));
  }, []);

  const selectedRepos = useMemo(
    () => selected.map((id) => repositoryCache[id]).filter(Boolean),
    [repositoryCache, selected],
  );
  const layout = useMemo(() => selectionLayout(selectedRepos), [selectedRepos]);

  const loadRepos = useCallback(
    async (id: string, query: string, page: number) => {
      const request = ++repositoryRequest.current;
      setLoadingRepos(true);
      setError("");
      try {
        const result = await api<{
          projects: RepositoryResponse[] | null;
          page: number;
          hasNext: boolean;
        }>(
          "/connections/" +
            encodeURIComponent(id) +
            "/repositories?search=" +
            encodeURIComponent(query) +
            "&page=" +
            page +
            "&perPage=50",
        );
        if (request !== repositoryRequest.current) return;
        const repositories = (result.projects || []).map((repo): ConnectionRepository => ({
          id: repo.id,
          name: repo.name,
          pathWithNamespace: repo.path_with_namespace,
          defaultBranch: repo.default_branch || "",
          httpUrlToRepo: repo.http_url_to_repo,
          webUrl: repo.web_url,
          archived: repo.archived,
          description: repo.description || "",
        }));
        setRepos(repositories);
        setRepositoryCache((old) => ({ ...old, ...Object.fromEntries(repositories.map((repo) => [repo.id, repo])) }));
        setRepoHasNext(!!result.hasNext);
        setRepoPage(result.page || page);
      } catch (e) {
        if (request === repositoryRequest.current) setError((e as Error).message);
      } finally {
        if (request === repositoryRequest.current) setLoadingRepos(false);
      }
    },
    [],
  );

  async function useConnection(connection: Connection) {
    setConnectionId(connection.id);
    setGitlabUrl(connection.gitlabUrl);
    setVerifyResult({
      username: connection.username,
      name: connection.name,
      fingerprint: connection.fingerprint,
      hasMergeToken: connection.hasMergeToken,
    });
    setSelected([]);
    setTargets({});
    setStep(1);
    await loadRepos(connection.id, "", 1);
  }

  async function verifyAndSave() {
    setVerifying(true);
    setError("");
    setVerifyResult(null);
    try {
      const verified = await api<VerifyResult>(
        "/connections/verify",
        "POST",
        { gitlabUrl, token, mergeToken },
      );
      const saved = await api<Connection>("/connections", "POST", {
        name: verified.username + "@" + gitlabUrl,
        gitlabUrl,
        token,
        mergeToken,
      });
      setVerifyResult(verified);
      setToken("");
      setMergeToken("");
      setConnections((old) => [...old.filter((c) => c.id !== saved.id), saved]);
      setConnectionId(saved.id);
      setSelected([]);
      setTargets({});
      setStep(1);
      await loadRepos(saved.id, "", 1);
    } catch (e) {
      setError((e as Error).message);
      tokenRef.current?.focus();
    } finally {
      setVerifying(false);
    }
  }

  function toggleRepo(repo: ConnectionRepository) {
    setSelected((old) =>
      old.includes(repo.id)
        ? old.filter((id) => id !== repo.id)
        : [...old, repo.id],
    );
    setTargets((old) =>
      old[repo.id]
        ? old
        : {
            ...old,
            [repo.id]: repo.defaultBranch || "dev",
          },
    );
  }

  const connection = useMemo(
    () => connections.find((c) => c.id === connectionId) || null,
    [connections, connectionId],
  );

  function buildRepositories(): Repository[] {
    return selectedRepos.map((repo) => ({
      id: layout.ids[repo.id],
      projectId: repo.id,
      url: repo.httpUrlToRepo,
      path: layout.paths[repo.id],
      targetBranch: targets[repo.id] || repo.defaultBranch || "dev",
      createFrom: "",
      dependsOn: [],
      sensitivePaths: [],
    }));
  }

  function buildProject(): Project {
    return {
      id: projectId || slug(name),
      name: name.trim(),
      metaUrl: "",
      metaRef: metaRef.trim() || "main",
      version: 0,
      enabled: false,
      metaMode: "managed",
      connectionId,
      autoMerge,
      acceptanceStrategy: "repository",
      gitlabUrl: connection?.gitlabUrl || gitlabUrl,
      credentialRef: "",
      mergeCredentialRef: "",
      repositories: buildRepositories(),
      nodes:{mode:"auto"},
      development: {...agent(model, defaultImage),adapter:defaultAgent},
      review: {...agent(model, defaultImage),adapter:defaultAgent},
      integration: { composeFile: "", checks: [], startupTimeoutSeconds: 180 },
      maxReworks: 3,
      requireCI: false,
    };
  }

  const refreshRuntime = useCallback(async () => {
    setRuntimeError("");
    try {
      const status = await api<{
        available: boolean;
        opencode: {
          installed: boolean;
          configured: boolean;
          providers: string[] | null;
        };
        message?: string;
      }>("/runtime/status");
      setRuntime(status);
      return status;
    } catch (e) {
      setRuntimeError((e as Error).message);
      return null;
    }
  }, []);

  // Reuse detection must be visible as soon as the OpenCode step opens, not
  // only after the operator clicks a refresh button.
  useEffect(() => {
    if (step === 3) void refreshRuntime();
  }, [step, refreshRuntime]);

  async function checkRuntimeAndModels() {
    setModelResult("");
    const status = await refreshRuntime();
    if (!status || !status.available) return;
    setLoadingModels(true);
    try {
      const result = await api<{
        models: (string | { id?: string; provider?: string } | null)[] | null;
        warnings: string[] | null;
      }>("/runtime/models");
      const list: string[] = [];
      for (const entry of result.models || []) {
        const value =
          typeof entry === "string"
            ? entry
            : entry && entry.id
              ? entry.provider && !entry.id.includes("/")
                ? entry.provider + "/" + entry.id
                : entry.id
              : "";
        if (value && !list.includes(value)) list.push(value);
      }
      setModels(list);
      if (!model && list.length) setModel(list[0]);
      if (!list.length)
        setModelResult(
          "未发现可用模型。请在终端完成 opencode auth login 后重试。",
        );
    } catch (e) {
      setModelResult((e as Error).message);
    } finally {
      setLoadingModels(false);
    }
  }

  async function verifyModel() {
    setVerifyingModel(true);
    setModelResult("");
    try {
      await api<{ ok: boolean; message: string }>("/runtime/verify", "POST", {
        model,
      });
      setModelResult("模型验证通过：最小实际调用成功。");
      setModelVerified(model);
    } catch (e) {
      setModelVerified("");
      setModelResult((e as Error).message);
    } finally {
      setVerifyingModel(false);
    }
  }

  async function startInitialization() {
    setSaving(true);
    setError("");
    try {
      const payload = buildProject();
      let saved: Project;
      if (projectId) {
        const current = await api<Project>(
          "/projects/" + encodeURIComponent(projectId),
        );
        saved = await api<Project>(
          "/projects/" + encodeURIComponent(projectId),
          "PUT",
          { ...payload, id: projectId, version: current.version, enabled: false },
        );
      } else {
        saved = await api<Project>("/projects", "POST", payload);
      }
      setProjectId(saved.id);
      onSaved?.(saved);
      const started = await api<Initialization>(
        "/projects/" + encodeURIComponent(saved.id) + "/initialization",
        "POST",
      );
      setInit(started);
      setStep(4);
      window.location.hash = `/projects/${encodeURIComponent(saved.id)}/initialization`;
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  useEffect(() => {
    if (step !== 4 || !projectId) return;
    let live = true;
    const tick = async () => {
      try {
        const current = await api<Initialization>(
          "/projects/" + encodeURIComponent(projectId) + "/initialization",
        );
        if (live) setInit(current);
      } catch (e) {
        if (live) setError((e as Error).message);
      }
    };
    void tick();
    const timer = setInterval(() => {
      // Poll only while the job can still progress. A pending question or a
      // terminal state must not be overwritten by another poll.
      const status = initRef.current?.status;
      if (initRef.current?.publication?.status === "pending") { void tick(); return; }
      if (
        status === "ready" ||
        status === "cancelled" ||
        status === "blocked" ||
        status === "needs_input"
      )
        return;
      void tick();
    }, 3000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [step, projectId]);

  async function submitAnswers() {
    if (!projectId) return;
    setError("");
    try {
      const updated = await api<Initialization>(
        "/projects/" + encodeURIComponent(projectId) + "/initialization/answers",
        "POST",
        { answers },
      );
      setInit(updated);
      setAnswers({});
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function control(action: "retry" | "cancel") {
    if (!projectId) return;
    setError("");
    try {
      await api(
        "/projects/" +
          encodeURIComponent(projectId) +
          "/initialization/" +
          action,
        "POST",
      );
      const current = await api<Initialization>(
        "/projects/" + encodeURIComponent(projectId) + "/initialization",
      );
      setInit(current);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const canLeaveConnection = !!connectionId;
  const canLeaveRepos = selected.length > 0;
  const canLeaveConfirm =
    !!name.trim() && !!metaRef.trim() && selected.length > 0;

  return (
    <div className="project-setup wizard">
      <a className="back" href="#/projects">
        ← 返回项目配置
      </a>
      <header className="page-head setup-heading">
        <div>
          <div className="eyebrow">{initialProject ? "PROJECT INITIALIZATION" : "NEW PROJECT"}</div>
          <h1>{initialProject ? initialProject.name + " · 初始化" : "连接仓库，配置 OpenCode，自动初始化"}</h1>
          <p>
            {initialProject ? "离开页面不会中断初始化；可随时从项目列表或编辑页返回，查看进度并继续处理。" : "只需填写 GitLab 地址与 Access Token；系统会读取真实仓库信息并托管 Meta-Repo。"}
          </p>
        </div>
        <span className="status paused">{initialProject ? "进度已保存" : "未保存"}</span>
      </header>
      {!initialProject && (
        <div className="notice" role="note">
          团队已有 Meta-Repo？ <a href="#/projects/new/import">导入已有 Meta-Repo</a>
        </div>
      )}
      {!initialProject && <nav className="setup-steps" aria-label="配置步骤">
        {stepList.map((item, index) => (
          <button
            type="button"
            key={item.key}
            className={index === step ? "active" : index < step ? "done" : ""}
            disabled={index > step}
            onClick={() => setStep(index)}
          >
            <span>{String(index + 1).padStart(2, "0")}</span>
            {item.label}
          </button>
        ))}
      </nav>}
      <ErrorBox error={error} />

      {step === 0 && (
        <section className="panel setup-section" id="connection">
          <div className="setup-section-head">
            <div>
              <h2>连接 GitLab</h2>
              <p>Token 只在服务端加密保存，不会回显或写入日志。</p>
            </div>
            <span className="setup-label">必填</span>
          </div>
          <div className="panel-padding">
            {connections.length > 0 && (
              <div className="wizard-connections">
                <h3>复用已有连接</h3>
                {connections.map((item) => (
                  <div className="wizard-connection" key={item.id}>
                    <div>
                      <strong>{item.name}</strong>
                      <small>
                        {item.gitlabUrl} · {item.username}
                        {item.hasMergeToken ? " · 独立合并 Token" : " · 单 Token"}
                      </small>
                    </div>
                    <button
                      type="button"
                      className="button"
                      onClick={() => void useConnection(item)}
                    >
                      使用此连接
                    </button>
                  </div>
                ))}
              </div>
            )}
            <div className="form-grid">
              <Field label="GitLab 地址">
                <input
                  value={gitlabUrl}
                  onChange={(e) => setGitlabUrl(e.target.value)}
                  placeholder="https://gitlab.example.com"
                  autoComplete="off"
                />
              </Field>
              <Field label="Access Token" hint="需要 read_api 或 api 权限。">
                <input
                  ref={tokenRef}
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder="glpat-…"
                  autoComplete="off"
                />
              </Field>
            </div>
            <details
              className="setup-advanced"
              open={advanced}
              onToggle={(e) => setAdvanced((e.target as HTMLDetailsElement).open)}
            >
              <summary>
                高级：独立合并凭证<span>可选，默认复用同一个 Token</span>
              </summary>
              <Field
                label="合并 Token"
                hint="留空时合并使用同一个 Token；单 Token 模式没有独立的权限隔离。"
              >
                <input
                  type="password"
                  value={mergeToken}
                  onChange={(e) => setMergeToken(e.target.value)}
                  autoComplete="off"
                />
              </Field>
            </details>
            {verifyResult && (
              <div className="notice" role="status">
                连接已验证：{verifyResult.username}（指纹{" "}
                {verifyResult.fingerprint}）
              </div>
            )}
            <div className="actions wizard-actions">
              <button
                type="button"
                className="primary"
                disabled={verifying || !gitlabUrl.trim() || !token.trim()}
                onClick={() => void verifyAndSave()}
              >
                {verifying ? "正在验证…" : "验证并继续"}
              </button>
            </div>
          </div>
        </section>
      )}

      {step === 1 && (
        <section className="panel setup-section" id="repositories">
          <div className="setup-section-head">
            <div>
              <h2>选择需要一起开发的子仓库</h2>
              <p>来自 GitLab 的真实项目 ID、地址和默认分支，可搜索和翻页。</p>
            </div>
            <span className="setup-label">
              {selected.length > 0 ? "已选 " + selected.length + " 个" : "必选"}
            </span>
          </div>
          <div className="panel-padding">
            <form className="wizard-search" onSubmit={(event) => { event.preventDefault(); void loadRepos(connectionId, repoQuery, 1); }}>
              <input
                value={repoQuery}
                onChange={(e) => setRepoQuery(e.target.value)}
                placeholder="搜索仓库名称或路径"
                aria-label="搜索仓库"
              />
              <button
                type="submit"
                disabled={loadingRepos}
              >
                {loadingRepos ? "搜索中…" : "搜索"}
              </button>
            </form>
            <p className="wizard-selection-hint" role="status">已选 {selected.length} 个仓库 · 点击整行勾选，翻页和搜索会保留选择</p>
            {repos.length === 0 && !loadingRepos && (
              <div className="repository-empty">
                <strong>没有匹配的仓库</strong>
                <p>请调整搜索词，或确认 Token 的仓库访问权限。</p>
              </div>
            )}
            <ul className="wizard-repo-list">
              {repos.map((repo) => {
                const checked = selected.includes(repo.id);
                return (
                  <li
                    key={repo.id}
                    className={checked ? "wizard-repo selected" : "wizard-repo"}
                  >
                    <label>
                      <input
                        type="checkbox"
                        aria-label={"选择仓库 " + repo.pathWithNamespace}
                        checked={checked}
                        onChange={() => toggleRepo(repo)}
                      />
                      <span className="wizard-repo-info">
                        <strong>{repo.name}</strong>
                        <small>{repo.pathWithNamespace}</small>
                        <small>
                          默认分支{" "}
                          {repo.defaultBranch || "未设置"}
                          {repo.archived ? " · 已归档" : ""}
                        </small>
                      </span>
                    </label>
                  </li>
                );
              })}
            </ul>
            <div className="wizard-pager">
              <button
                type="button"
                disabled={repoPage <= 1 || loadingRepos}
                onClick={() => void loadRepos(connectionId, repoQuery, repoPage - 1)}
              >
                上一页
              </button>
              <span>第 {repoPage} 页</span>
              <button
                type="button"
                disabled={!repoHasNext || loadingRepos}
                onClick={() => void loadRepos(connectionId, repoQuery, repoPage + 1)}
              >
                下一页
              </button>
            </div>
            {selectedRepos.length > 0 && (
              <div className="wizard-targets">
                <h3>目标分支</h3>
                {selectedRepos.map((repo) => (
                  <Field
                    key={repo.id}
                    label={repo.name + " 的目标分支"}
                    hint={"将作为 " + layout.paths[repo.id] + " 的工作目录"}
                  >
                    <input
                      value={targets[repo.id] || repo.defaultBranch || "dev"}
                      aria-label={repo.name + " 的目标分支"}
                      onChange={(e) =>
                        setTargets((old) => ({
                          ...old,
                          [repo.id]: e.target.value,
                        }))
                      }
                    />
                  </Field>
                ))}
              </div>
            )}
            <div className="actions wizard-actions">
              <button type="button" onClick={() => setStep(0)}>
                上一步
              </button>
              <button
                type="button"
                className="primary"
                disabled={!canLeaveRepos}
                onClick={() => {
                  if (!name.trim())
                    setName(
                      selectedRepos[0]?.name
                        ? selectedRepos[0].name + " 工作区"
                        : "开发工作区",
                    );
                  setStep(2);
                }}
              >
                下一步
              </button>
            </div>
          </div>
        </section>
      )}

      {step === 2 && (
        <section className="panel setup-section" id="confirm">
          <div className="setup-section-head">
            <div>
              <h2>确认项目与合并策略</h2>
              <p>系统会自动创建并托管 Meta-Repo，不会在 GitLab 新建远程项目。</p>
            </div>
            <span className="setup-label">必填</span>
          </div>
          <div className="panel-padding">
            <div className="form-grid">
              <Field label="项目名称">
                <input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="例如：数字员工"
                />
              </Field>
              <Field
                label="Meta 分支"
                hint="受管 Meta-Repo 的内部分支，通常保持 main。"
              >
                <input
                  value={metaRef}
                  onChange={(e) => setMetaRef(e.target.value)}
                />
              </Field>
            </div>
            <div className="wizard-toggle">
              <label>
                <input
                  type="checkbox"
                  checked={autoMerge}
                  onChange={(e) => setAutoMerge(e.target.checked)}
                />
                <span>
                  <strong>自动合并</strong>
                  <small>
                    默认关闭。关闭时系统仍会开发、验证、创建 MR 和审查，但不会发起
                    合并，需要人工在 GitLab 完成合并后才会收尾关单。
                  </small>
                </span>
              </label>
              <p className="wizard-hint">
                {autoMerge
                  ? "开启后沿用现有自动合并门禁：有效审查、当前提交验证、CI、目标分支检查，以及敏感变更的人工作业批准。"
                  : "当前为人工合并：请在所有 MR 合并完成后等待系统检测并关闭来源 Issue。"}
              </p>
            </div>
            <div className="wizard-summary">
              <h3>将托管 {selectedRepos.length} 个仓库</h3>
              <ul>
                {selectedRepos.map((repo) => (
                  <li key={repo.id}>
                    <code>{layout.paths[repo.id]}</code>
                    <span>{repo.pathWithNamespace}</span>
                    <span className="tag">
                      目标 {targets[repo.id] || repo.defaultBranch || "dev"}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
            <div className="actions wizard-actions">
              <button type="button" onClick={() => setStep(1)}>
                上一步
              </button>
              <button
                type="button"
                className="primary"
                disabled={!canLeaveConfirm}
                onClick={() => setStep(3)}
              >
                下一步：配置 OpenCode
              </button>
            </div>
          </div>
        </section>
      )}

      {step === 3 && (
        <section className="panel setup-section" id="opencode">
          <div className="setup-section-head">
            <div>
              <h2>配置 OpenCode 登录</h2>
              <p>
                在页面内嵌终端执行登录命令；不会自动操作用户账号，也不会读取登录
                秘密。
              </p>
            </div>
            <span className="setup-label">执行器</span>
          </div>
          <div className="panel-padding">
            {runtimeError && (
              <div className="error" role="alert">
                {runtimeError}
                <button type="button" onClick={() => void refreshRuntime()}>
                  重试
                </button>
              </div>
            )}
            {runtime?.message && (
              <div className="runtime-message">{runtime.message}</div>
            )}
            {runtime?.opencode?.configured && (
              <div className="notice" role="status">
                检测到已有的 OpenCode 登录配置，可直接复用；也可以重新打开终端修改。
              </div>
            )}
            <TerminalPanel
              available={!!runtime?.available}
              onConnected={() => void refreshRuntime()}
            />
            <Field label="项目默认 Agent">
              <select value={defaultAgent} onChange={(e)=>setDefaultAgent(e.target.value)}>
                <option value="">请选择已启用的 Agent</option>
                {agentCatalog.agents.filter((a)=>agentCatalog.enabled.includes(a.id)).map((a)=><option key={a.id} value={a.id}>{a.name}</option>)}
              </select>
            </Field>
            <p>在<a href="#/runtime">执行器环境</a>统一勾选启用。Issue 标签可覆盖默认 Agent；任务模型由 CLI 的持久化配置决定。下方 OpenCode 模型仅用于 Meta 静态初始化。</p>
            {agentCatalogError && <p role="alert">{agentCatalogError}</p>}
            <div className="wizard-model">
              <button
                type="button"
                onClick={() => void checkRuntimeAndModels()}
                disabled={loadingModels}
              >
                {loadingModels ? "正在检查…" : "检查并继续"}
              </button>
              <span role="status">
                {runtime
                  ? runtime.available
                    ? runtime.opencode?.installed
                      ? "执行器可用"
                      : "执行器可用；打开终端后将检测工具"
                    : "执行器不可用"
                  : "尚未检查执行器状态"}
              </span>
            </div>
            {models.length > 0 && (
              <div className="form-grid">
                <Field label="模型" hint="登录后从模型目录选择，随后必须做一次实际调用验证。">
                  <select value={model} onChange={(e) => setModel(e.target.value)}>
                    {models.map((item) => (
                      <option key={item} value={item}>
                        {item}
                      </option>
                    ))}
                  </select>
                </Field>
                <div className="wizard-verify-model">
                  <button
                    type="button"
                    className="primary"
                    disabled={!model || verifyingModel}
                    onClick={() => void verifyModel()}
                  >
                    {verifyingModel ? "正在验证…" : "验证模型"}
                  </button>
                  {modelVerified && (
                    <span className="readiness-done" role="status">
                      已验证 {modelVerified}
                    </span>
                  )}
                </div>
              </div>
            )}
            {modelResult && (
              <div
                className={
                  modelVerified ? "notice" : "error runtime-inline-error"
                }
                role="status"
              >
                {modelResult}
              </div>
            )}
            <p className="runtime-model-help">
              检测到配置文件或模型名称不代表凭据有效；只有实际调用成功才算验证通过。
            </p>
            <div className="actions wizard-actions">
              <button type="button" onClick={() => setStep(2)}>
                上一步
              </button>
              <button
                type="button"
                className="primary"
                disabled={saving || !modelVerified || !agentCatalog.enabled.includes(defaultAgent)}
                onClick={() => void startInitialization()}
              >
                {saving ? "正在创建项目…" : "创建项目并开始初始化"}
              </button>
            </div>
          </div>
        </section>
      )}

      {step === 4 && (
        <section className="panel setup-section" id="initialize">
          <div className="setup-section-head">
            <div>
              <h2>自动初始化</h2>
              <p>
                系统只读取仓库静态内容，创建 Meta-Repo 并生成项目说明；不执行业务代码、不安装依赖、不构建或测试。
              </p>
            </div>
            <span
              className={
                "status " +
                (init?.status === "ready"
                  ? "completed"
                  : init?.status === "blocked"
                    ? "blocked"
                    : "paused")
              }
            >
              {init ? init.status : "创建中"}
            </span>
          </div>
          <div className="panel-padding">
            {init && (
              <>
                <div className="wizard-progress" aria-label="初始化进度">
                  <div style={{ width: Math.max(0, Math.min(100, init.progress)) + "%" }} />
                </div>
                <p className="wizard-message" role="status">
                  {init.message || "正在准备初始化"}
                </p>
                <ol className="wizard-funnel">
                  <li className={init.repositoriesConnected ? "done" : ""}>
                    仓库已连接
                  </li>
                  <li className={init.analysisComplete ? "done" : ""}>
                    分析完成
                  </li>
                  <li className={init.readyForTasks ? "done" : ""}>
                    Meta 仓库已初始化
                  </li>
                </ol>
                {init.error && (
                  <div className="error" role="alert">
                    {init.error}
                  </div>
                )}
                {init.reports && init.reports.length > 0 && (
                  <div className="wizard-reports">
                    <h3>生成的项目说明</h3>
                    <ul>
                      {init.reports.map((report) => (
                        <li key={report.path}>
                          <code>{report.path}</code>
                          <span>{report.description}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                {init.verification && init.verification.length > 0 && (
                  <div className="wizard-verification">
                    <h3>历史验证记录（静态初始化不再执行）</h3>
                    {init.verification.map((item, index) => (
                      <div
                        key={item.name + index}
                        className={item.passed ? "passed" : "failed"}
                      >
                        <strong>
                          {item.passed ? "通过" : item.dryRun ? "未执行" : "失败"}{" "}
                          · {item.name}
                        </strong>
                        {item.command && <code>{item.command.join(" ")}</code>}
                        {item.report && <pre>{item.report}</pre>}
                      </div>
                    ))}
                  </div>
                )}
                {init.status === "needs_input" && init.questions && (
                  <div className="wizard-questions">
                    <h3>需要补充信息</h3>
                    {init.questions
                      .filter((q) => !q.answered)
                      .map((q) => (
                        <Field key={q.id} label={q.question}>
                          <input
                            aria-label={q.question}
                            value={answers[q.id] || ""}
                            onChange={(e) =>
                              setAnswers((old) => ({
                                ...old,
                                [q.id]: e.target.value,
                              }))
                            }
                          />
                        </Field>
                      ))}
                    <button
                      type="button"
                      className="primary"
                      onClick={() => void submitAnswers()}
                    >
                      提交并继续
                    </button>
                  </div>
                )}
                {init.status === "ready" && <MetaPublication projectId={projectId} init={init} onChange={setInit} />}
                <div className="actions wizard-actions">
                  {(init.status === "running" ||
                    init.status === "pending") && (
                    <button type="button" onClick={() => void control("cancel")}>
                      取消初始化
                    </button>
                  )}
                  {init.status === "blocked" && (
                    <button
                      type="button"
                      className="primary"
                      onClick={() => void control("retry")}
                    >
                      重试初始化
                    </button>
                  )}
                  {(init.status === "cancelled" || init.status === "none") && (
                    <button type="button" onClick={async () => {
                      try {
                        setError("");
                        setInit(await api<Initialization>(`/projects/${encodeURIComponent(projectId)}/initialization`, "POST"));
                      } catch (e) { setError((e as Error).message); }
                    }}>重新开始初始化</button>
                  )}
                  {projectId && (
                    <button
                      type="button"
                      onClick={() => onOpenProject(projectId)}
                    >
                      返回项目设置
                    </button>
                  )}
                </div>
                {init.status === "ready" && !init.readyForTasks && (
                  <div className="setup-warning" role="status">
                    项目说明已保存，但验证尚未完成。请在项目中补充验收检查并手动
                    启用；在此之前不会接单。
                  </div>
                )}
              </>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
