import { useEffect, useRef, useState } from "react";
import { useDataAPI, type GithubRepository, type ModelsState } from "../data";
import type { StarsSlotProps } from "./slots";
export default function CreateProject({
  ui,
  reloadProjects,
  onClose,
}: Pick<StarsSlotProps, "ui" | "reloadProjects"> & { onClose(): void }) {
  const root = useRef<HTMLElement>(null);
  const [attempt, setAttempt] = useState(0);
  const api = useDataAPI();
  const [repos, setRepos] = useState<GithubRepository[]>();
  const [models, setModels] = useState<ModelsState>();
  const [repo, setRepo] = useState("");
  const [name, setName] = useState("");
  const [model, setModel] = useState("");
  const [count, setCount] = useState("4");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement;
    root.current?.querySelector<HTMLElement>("button")?.focus();
    return () => previous?.focus();
  }, []);
  useEffect(() => {
    let active = true;
    setError("");
    void api
      .listGithubRepositories()
      .then((r) => {
        if (active) setRepos(r.repositories);
      })
      .catch((e) => {
        if (active) {
          setRepos([]);
          setError(`仓库读取失败，可重新读取或填写 owner/name：${e.message}`);
        }
      });
    void api
      .listModels()
      .then((m) => {
        if (active) setModels(m);
      })
      .catch((e) => {
        if (active) setError(`模型读取失败，请重新读取：${e.message}`);
      });
    return () => {
      active = false;
    };
  }, [api, attempt]);
  const providers = new Set(
    models?.providers.filter((p) => p.configured).map((p) => p.provider),
  );
  const available = models?.gatewayAvailable
    ? models.models.filter((m) => providers.has(m.provider))
    : [];
  const chosen =
    available?.find((m) => m.id === model)?.id ??
    available?.find((m) => m.id === models?.defaultModel)?.id ??
    available?.[0]?.id ??
    "";
  const repository = repo || repos?.[0]?.fullName || "";
  return (
    <section
      ref={root}
      onKeyDown={(e) => {
        if (e.key !== "Tab") return;
        const nodes = [
          ...root.current!.querySelectorAll<HTMLElement>(
            "button:not(:disabled),input:not(:disabled),select:not(:disabled)",
          ),
        ];
        const first = nodes[0],
          last = nodes[nodes.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }}
      className="periphery-create"
      role="dialog"
      aria-modal="true"
      aria-label="创建项目"
    >
      <button className="periphery-close" onClick={onClose}>
        关闭创建
      </button>
      <h2>创建项目</h2>
      <p>选择仓库，开启与主 Agent 的第一次对话。</p>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (busy) return;
          const concurrency = Number(count);
          if (!/^[^/\s]+\/[^/\s]+$/.test(repository)) {
            setError("仓库格式应为 owner/name。");
            return;
          }
          if (!chosen) {
            setError("请先配置可用模型凭证。");
            return;
          }
          if (!Number.isSafeInteger(concurrency) || concurrency < 1) {
            setError("并发上限须为大于 0 的整数。");
            return;
          }
          setBusy(true);
          setError("");
          try {
            const r = await api.createHive({
              name: name.trim() || repository.split("/")[1],
              repository,
              queenEnabled: true,
              config: {
                queenModel: chosen,
                maxConcurrentForagers: concurrency,
              },
            });
            reloadProjects();
            onClose();
            ui.selectObject({
              hiveId: r.hive.id,
              kind: "queen",
              id: `${r.hive.id}:queen`,
            });
          } catch (e) {
            setError(e instanceof Error ? e.message : String(e));
          } finally {
            setBusy(false);
          }
        }}
      >
        <fieldset disabled={busy}>
          {repos === undefined && !error ? (
            <p role="status">正在读取仓库和模型…</p>
          ) : (
            <>
              <label>
                仓库
                {repos?.length ? (
                  <select
                    value={repository}
                    onChange={(e) => setRepo(e.target.value)}
                  >
                    {repos.map((r) => (
                      <option key={r.fullName}>{r.fullName}</option>
                    ))}
                  </select>
                ) : (
                  <input
                    required
                    placeholder="owner/name"
                    value={repo}
                    onChange={(e) => setRepo(e.target.value)}
                  />
                )}
              </label>
              <label>
                项目名称
                <input value={name} onChange={(e) => setName(e.target.value)} />
              </label>
              <label>
                主 Agent 模型
                <select
                  value={chosen}
                  onChange={(e) => setModel(e.target.value)}
                  disabled={!available?.length}
                >
                  {!available?.length && <option>暂无可用模型</option>}
                  {available?.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.displayName || m.id}
                    </option>
                  ))}
                </select>
              </label>
              {!available?.length && (
                <button
                  type="button"
                  onClick={() => {
                    onClose();
                    ui.openPlatform("models");
                  }}
                >
                  配置模型凭证
                </button>
              )}
              <label>
                同时工作的 Agent 名额
                <input
                  type="number"
                  min="1"
                  step="1"
                  required
                  value={count}
                  onChange={(e) => setCount(e.target.value)}
                />
              </label>
              <button disabled={busy || !chosen}>
                {busy ? "正在创建…" : "创建并打开主 Agent"}
              </button>
            </>
          )}
          {error && (
            <>
              <p role="alert">{error}</p>
              <button type="button" onClick={() => setAttempt((n) => n + 1)}>
                重新读取仓库和模型
              </button>
            </>
          )}
        </fieldset>
      </form>
    </section>
  );
}
