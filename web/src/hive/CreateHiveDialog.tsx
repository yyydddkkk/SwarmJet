// 新建 Hive dialog. The repository field prefers repositories the GitHub App is
// already installed on (GET /api/setup/github/repositories), but manual
// owner/name input stays available — and is the only option before the App exists.
import { useId, useState, type FormEvent } from "react";
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
  const models = useResource(signal => hiveAPI.listModels(signal), []);
  const [step, setStep] = useState(0);
  const [queenModel, setQueenModel] = useState("");
  const [concurrency, setConcurrency] = useState("4");
  const [mode, setMode] = useState<"select" | "manual">("select");
  const [repository, setRepository] = useState("");
  const [manual, setManual] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const nameId = useId();
  const manualId = useId();

  const repos = repositories.data ?? [];
  const showManualOnly = !configured;
  const configuredProviders = new Set(
    (models.data?.providers ?? []).filter(provider => provider.configured).map(provider => provider.provider),
  );
  const availableModels = (models.data?.models ?? []).filter(model => configuredProviders.has(model.provider));
  const selectedQueenModel = availableModels.some(model => model.id === queenModel)
    ? queenModel
    : availableModels.find(model => model.id === models.data?.defaultModel)?.id ?? availableModels[0]?.id ?? "";
  // Derive the initial choice instead of setting it in an effect: a delayed
  // default must never overwrite a repository the user has just selected.
  const repositoryChoice = repository || repos[0]?.fullName || "";
  const selectedRepository = mode === "manual" || showManualOnly ? manual.trim() : repositoryChoice;

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const repo = selectedRepository;
    if (!repo) {
      setError("选择或填写一个仓库。");
      return;
    }
    if (!/^[^/\s]+\/[^/\s]+$/.test(repo)) {
      setError("仓库格式应为 owner/name，例如 yyydddkkk/SwarmJet。");
      return;
    }
    if (step === 0) {
      setError("");
      setStep(1);
      return;
    }
    if (!availableModels.some(model => model.id === selectedQueenModel)) {
      setError("请先配置模型凭证，再选择 Queen 的模型。");
      return;
    }
    if (step === 1) {
      setError("");
      setStep(2);
      return;
    }
    const maxConcurrentForagers = Number(concurrency);
    if (!Number.isSafeInteger(maxConcurrentForagers) || maxConcurrentForagers < 1) {
      setError("并发上限须为大于 0 的整数。");
      return;
    }
    setBusy(true);
    setError("");
    hiveAPI
      .createHive({
        name: name.trim() || repo.split("/")[1],
        repository: repo,
        queenEnabled: true,
        config: { queenModel: selectedQueenModel, maxConcurrentForagers },
      })
      .then(result => onCreated(result.hive.id))
      .catch(cause => setError(cause instanceof Error ? cause.message : String(cause)))
      .finally(() => setBusy(false));
  };

  return (
    <Dialog title="新建 Hive" onClose={onClose} wide>
      <form className="hg-token-form" onSubmit={submit}>
        <div className="hg-segmented" role="group" aria-label="创建进度">
          {["选择仓库", "Queen 模型", "并发上限"].map((label, index) => (
            <span key={label} className={`hg-badge ${step === index ? "hg-tone-amber" : ""}`} aria-current={step === index ? "step" : undefined}>
              {index + 1} · {label}
            </span>
          ))}
        </div>
        <p className="hg-muted">
          一个 Hive 对应一个仓库。Queen 默认开启，创建后会在对话里引导你选择方法论。
        </p>
        {step === 0 ? <>
        <label className="hg-field" htmlFor={nameId}>
          <span>名称</span>
          <input
            id={nameId}
            value={name}
            onChange={event => setName(event.target.value)}
            placeholder="例如：SwarmJet 平台"
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
                value={repositoryChoice}
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

        </> : step === 1 ? <>
          {models.loading ? <p className="hg-muted" role="status">正在读取模型与凭证…</p> : models.error ? (
            <Callout tone="amber" title="无法读取模型">
              <p>{models.error}</p>
              <button className="hg-btn hg-btn-small" type="button" onClick={models.reload}>重新读取</button>
            </Callout>
          ) : !availableModels.length ? (
            <Callout tone="amber" title="没有可用的模型凭证">
              <p>请先去 <a href="#/models" onClick={onClose}>模型</a> 页填写模型凭证，再回来为 Queen 选择模型。</p>
            </Callout>
          ) : (
            <label className="hg-field">
              <span>Queen 模型</span>
              <select aria-label="Queen 模型" value={selectedQueenModel} onChange={event => setQueenModel(event.target.value)}>
                {availableModels.map(model => <option key={model.id} value={model.id}>{model.displayName || model.id} · {model.id}</option>)}
              </select>
              <small>只显示已配置凭证的模型。Queen 负责管理 Hive 和与你对话。</small>
            </label>
          )}
        </> : <>
          <label className="hg-field">
            <span>并发上限</span>
            <input aria-label="并发上限" type="number" min="1" step="1" value={concurrency} onChange={event => setConcurrency(event.target.value)} required />
            <small>同时运行的 Forager 数量，默认 4。Queen 不占用并发名额。</small>
          </label>
          <Callout tone="mint" title="准备创建">
            <p>{name.trim() || selectedRepository.split("/")[1]} · {selectedRepository}</p>
            <p>Queen 已开启 · {selectedQueenModel}</p>
            <p>创建后进入 Queen 对话，方法论由你在对话中决定。</p>
          </Callout>
        </>}

        {error ? (
          <p className="hg-form-error" role="alert">
            {error}
          </p>
        ) : null}

        <div className="hg-dialog-actions">
          <button className="hg-btn" type="button" onClick={onClose} disabled={busy}>
            取消
          </button>
          {step > 0 ? <button className="hg-btn" type="button" disabled={busy} onClick={() => { setError(""); setStep(step - 1); }}>上一步</button> : null}
          <button className="hg-btn hg-btn-primary" type="submit" disabled={busy || (step === 0 && (status.loading || repositories.loading)) || (step === 1 && (models.loading || !!models.error || !selectedQueenModel))}>
            <Icon name="hive" size={16} />
            {busy ? "正在创建…" : step === 2 ? "创建 Hive" : "下一步"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
