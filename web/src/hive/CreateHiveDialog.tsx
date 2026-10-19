// 新建 Hive dialog. The repository field prefers repositories the GitHub App is
// already installed on (GET /api/setup/github/repositories), but manual
// owner/name input stays available — and is the only option before the App exists.
import { useEffect, useId, useState, type FormEvent } from "react";
import { hiveAPI } from "./api";
import { Callout, Dialog } from "./components";
import { Icon } from "./icons";
import { useResource } from "./use-resource";

export default function CreateHiveDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (hiveId: string) => void;
}) {
  const status = useResource(signal => hiveAPI.getSetupStatus(signal), []);
  const configured = status.data?.github.configured ?? false;
  const repositories = useResource(
    signal =>
      configured
        ? hiveAPI.listGithubRepositories(signal).then(result => result.repositories)
        : Promise.resolve([]),
    [configured],
  );
  const [name, setName] = useState("");
  const [mode, setMode] = useState<"select" | "manual">("select");
  const [repository, setRepository] = useState("");
  const [manual, setManual] = useState("");
  const [queenEnabled, setQueenEnabled] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const nameId = useId();
  const manualId = useId();

  const repos = repositories.data ?? [];
  const showManualOnly = !configured;

  useEffect(() => {
    if (mode !== "select") return;
    if (!repos.length) return;
    if (repos.some(repo => repo.fullName === repository)) return;
    setRepository(repos[0].fullName);
  }, [mode, repos, repository]);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const repo = mode === "manual" || showManualOnly ? manual.trim() : repository;
    if (!name.trim()) {
      setError("给 Hive 起个名字。");
      return;
    }
    if (!repo) {
      setError("选择或填写一个仓库。");
      return;
    }
    if (!/^[^/\s]+\/[^/\s]+$/.test(repo)) {
      setError("仓库格式应为 owner/name，例如 yyydddkkk/SwarmJet。");
      return;
    }
    setBusy(true);
    setError("");
    hiveAPI
      .createHive({ name: name.trim(), repository: repo, queenEnabled })
      .then(result => onCreated(result.hive.id))
      .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
      .finally(() => setBusy(false));
  };

  return (
    <Dialog title="新建 Hive" onClose={onClose} wide>
      <form className="hg-token-form" onSubmit={submit}>
        <p className="hg-muted">
          一个 Hive 对应一个仓库。创建后可以派 Forager，Queen 是否开启之后也能改。
        </p>
        <label className="hg-field" htmlFor={nameId}>
          <span>名称</span>
          <input
            id={nameId}
            value={name}
            onChange={event => setName(event.target.value)}
            placeholder="例如：SwarmJet 平台"
            required
          />
        </label>

        {showManualOnly ? (
          <Callout tone="amber" title="GitHub App 还没配置">
            <p>
              先去 <a href="#/setup">接入页</a> 创建 GitHub App，就能直接选择已安装的仓库。
              现在仍可以手动输入 owner/name，但 Forager 暂时无法推送或开 PR。
            </p>
          </Callout>
        ) : null}

        {!showManualOnly ? (
          <div className="hg-segmented" role="group" aria-label="仓库填写方式">
            <button
              className="hg-btn hg-btn-small"
              type="button"
              aria-pressed={mode === "select"}
              onClick={() => setMode("select")}
            >
              从已安装仓库选择
            </button>
            <button
              className="hg-btn hg-btn-small"
              type="button"
              aria-pressed={mode === "manual"}
              onClick={() => setMode("manual")}
            >
              手动输入
            </button>
          </div>
        ) : null}

        {mode === "select" && !showManualOnly ? (
          <label className="hg-field">
            <span>仓库</span>
            {repositories.loading ? (
              <select disabled aria-label="仓库">
                <option>正在读取已安装仓库…</option>
              </select>
            ) : repos.length ? (
              <select
                aria-label="仓库"
                value={repository}
                onChange={event => setRepository(event.target.value)}
              >
                {repos.map(repo => (
                  <option key={repo.fullName} value={repo.fullName}>
                    {repo.fullName}
                    {repo.private ? "（私有）" : ""} · {repo.defaultBranch}
                  </option>
                ))}
              </select>
            ) : (
              <select disabled aria-label="仓库">
                <option>还没有安装 App 的仓库，请先到接入页安装</option>
              </select>
            )}
          </label>
        ) : (
          <label className="hg-field" htmlFor={manualId}>
            <span>仓库（owner/name）</span>
            <input
              id={manualId}
              value={manual}
              onChange={event => setManual(event.target.value)}
              placeholder="yyydddkkk/SwarmJet"
              aria-label="仓库"
            />
          </label>
        )}

        <label className="hg-checkbox">
          <input
            type="checkbox"
            checked={queenEnabled}
            onChange={event => setQueenEnabled(event.target.checked)}
          />
          <span>
            <strong>开启 Queen（蜂王）</strong>
            <small>Queen 负责判断和派活；关闭时事件留在 Inbox，由人或本地 Agent 处理。</small>
          </span>
        </label>

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
            <Icon name="hive" size={16} />
            {busy ? "正在创建…" : "创建 Hive"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
