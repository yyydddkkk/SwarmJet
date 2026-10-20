import { useEffect, useRef, useState } from "react";
import {
  messagePreview,
  useDataAPI,
  type SetupStatus,
  type ModelsState,
  type UsageResponse,
} from "../data";
import type { StarsSlotProps } from "./slots";
import { THEMES, themeStore, useTheme, type ThemeChoice } from "./theme";
import { useStarsUI, type Overlay } from "./ui";
import { command, preference } from "./periphery-command";
import CreateProject from "./CreateProject";
import "./periphery.css";
export default function PeripherySlot(props: StarsSlotProps) {
  const { ui, projects, stores } = props;
  const state = useStarsUI(ui);
  const theme = useTheme();
  const api = useDataAPI();
  const input = useRef<HTMLInputElement>(null);
  const panel = useRef<HTMLElement>(null);
  const legendSeen = useRef(preference.read("sj-legend-seen") === "1");
  const legendTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [text, setText] = useState("");
  const [search, setSearch] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [confirm, setConfirm] = useState("");
  const [mergeHead, setMergeHead] = useState("");
  const [notice, setNotice] = useState("");
  const [create, setCreate] = useState(false);
  const [skip, setSkip] = useState(() => preference.read("sj-brief") === "off");
  const [setup, setSetup] = useState<SetupStatus>();
  const [models, setModels] = useState<ModelsState>();
  const [usage, setUsage] = useState<UsageResponse>();
  const [platformError, setPlatformError] = useState("");
  const [platformAttempt, setPlatformAttempt] = useState(0);
  const entries = [...stores].flatMap(([id, store]) => {
    const state = store.getSnapshot();
    return state.snapshot && state.derived
      ? [{ id, store, state, derived: state.derived, s: state.snapshot }]
      : [];
  });
  const current = projects.find((p) => p.hive.id === state.hiveId)?.hive;
  const route = command(text, projects, stores, ui);
  const loaded = entries.length === projects.length;
  useEffect(() => {
    const unsubscribe = ui.subscribe(() => {
      if (ui.getSnapshot().overlay !== "legend") {
        clearTimeout(legendTimer.current);
        legendTimer.current = undefined;
      }
    });
    return () => {
      unsubscribe();
      clearTimeout(legendTimer.current);
    };
  }, [ui]);
  useEffect(() => {
    const open = () => {
      while (
        ui.getSnapshot().overlay ||
        ui.getSnapshot().platformPanel ||
        ui.getSnapshot().selection ||
        ui.getSnapshot().projectPanel
      )
        ui.close();
      setCreate(true);
    };
    window.addEventListener("swarmjet:create-project", open);
    return () => window.removeEventListener("swarmjet:create-project", open);
  }, [ui]);
  useEffect(() => {
    let active = true;
    setPlatformError("");
    const failure = (label: string, e: unknown) => {
      if (active)
        setPlatformError((previous) =>
          [previous, `${label}：${e instanceof Error ? e.message : String(e)}`]
            .filter(Boolean)
            .join("；"),
        );
    };
    void api
      .getSetupStatus()
      .then((value) => {
        if (active) setSetup(value);
      })
      .catch((e) => failure("接入状态读取失败", e));
    void api
      .listModels()
      .then((value) => {
        if (active) setModels(value);
      })
      .catch((e) => failure("模型状态读取失败", e));
    void api
      .getUsage({ period: "today" })
      .then((value) => {
        if (active) setUsage(value);
      })
      .catch((e) => failure("花费读取失败", e));
    return () => {
      active = false;
    };
  }, [api, projects.length, state.platformPanel, platformAttempt]);
  useEffect(() => {
    if (
      preference.read("sj-brief") !== "off" &&
      !ui.getSnapshot().platformPanel
    )
      ui.openOverlay("brief");
  }, [ui]);
  useEffect(() => {
    const live = ui.getSnapshot();
    if (
      live.overlay ||
      live.platformPanel ||
      live.selection ||
      create ||
      legendSeen.current
    )
      return;
    legendSeen.current = true;
    preference.write("sj-legend-seen", "1");
    ui.openOverlay("legend");
    legendTimer.current = setTimeout(() => {
      legendTimer.current = undefined;
      if (ui.getSnapshot().overlay === "legend") ui.close();
    }, 7000);
  }, [state.overlay, state.platformPanel, state.selection, create, ui]);
  useEffect(() => {
    if (!state.overlay || state.overlay === "timeline") return;
    const previous = document.activeElement as HTMLElement;
    if (state.overlay === "list")
      panel.current?.querySelector<HTMLInputElement>("input")?.focus();
    else panel.current?.querySelector<HTMLButtonElement>("button")?.focus();
    return () => {
      if (previous?.isConnected) previous.focus();
    };
  }, [state.overlay]);
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (
        e.defaultPrevented ||
        e.ctrlKey ||
        e.metaKey ||
        e.altKey ||
        e.isComposing
      )
        return;
      const target = e.target as HTMLElement;
      const key = e.key.toLowerCase();
      if (key === "escape") {
        e.preventDefault();
        if (create) setCreate(false);
        else if (confirm) setConfirm("");
        else ui.close();
        return;
      }
      if (target?.closest("input,textarea,select,[contenteditable]")) return;
      const overlays = {
        t: "timeline",
        c: "list",
        b: "brief",
        l: "legend",
      } as const;
      if (key in overlays) {
        e.preventDefault();
        const o = overlays[key as keyof typeof overlays];
        ui.getSnapshot().overlay === o ? ui.close() : ui.openOverlay(o);
      } else if (key === "/") {
        e.preventDefault();
        input.current?.focus();
      } else if (key === "a") {
        e.preventDefault();
        if (!ui.getSnapshot().hiveId && projects[0])
          ui.selectProject(projects[0].hive.id);
        ui.openProject();
      } else if (key === ",") {
        e.preventDefault();
        ui.openPlatform();
      } else if (key === "s")
        themeStore.setStyle(
          themeStore.getSnapshot().style === "classic" ? "simple" : "classic",
        );
      else if (key === "g")
        themeStore.setGrid(!themeStore.getSnapshot().render.grid);
      else if (key === "d")
        themeStore.setTheme(themeStore.getSnapshot().dark ? "bright" : "night");
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [ui, projects, create, confirm]);
  async function act(key: string, action: () => Promise<unknown>) {
    if (busy) return;
    setBusy(key);
    setError("");
    setNotice("");
    try {
      await action();
      setConfirm("");
      return true;
    } catch (e) {
      setConfirm("");
      setError(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy("");
    }
  }
  const queen = (id: string) =>
    ui.selectObject({ hiveId: id, kind: "queen", id: `${id}:queen` });
  const decisions = entries.flatMap((e) =>
    e.derived.decisions.map((d) => ({ e, d })),
  );
  const active = entries.flatMap((e) =>
    e.s.agents
      .filter((a) => ["active", "creating"].includes(a.state))
      .map((a) => ({ e, a })),
  );
  const attention = entries.flatMap((e) =>
    e.s.agents
      .filter(
        (a) =>
          a.state === "failed" ||
          e.derived.agentStatuses[a.id]?.includes("仍占"),
      )
      .map((a) => ({ e, a })),
  );
  const selected = entries.find((e) => e.id === state.hiveId);
  const activeCount = selected
    ? selected.s.agents.filter((a) => ["active", "creating"].includes(a.state)).length
    : active.length;
  const decisionCount = selected ? selected.derived.decisions.length : decisions.length;
  const summary = !loaded
    ? "正在读取项目状态…"
    : `${selected ? (selected.s.queen.enabled ? "" : "主 Agent 已停用，") : `${projects.length} 个项目，`}${activeCount} 个 Agent 在工作${selected?.s.queue.length ? `，${selected.s.queue.length} 个排队中` : ""}${decisionCount ? `，${decisionCount} 件事等你决定` : ""}。`;
  const decisionRow = ({ e, d }: (typeof decisions)[number]) =>
    d.kind === "pr" ? (
      <div className="periphery-row need" key={`${e.id}:${d.id}`}>
        <button
          onClick={() =>
            ui.selectObject({
              hiveId: e.id,
              kind: "pr",
              id: d.id,
              number: d.pr.number,
            })
          }
        >
          PR #{d.pr.number} {d.pr.title || "等待合并"}
        </button>
        {confirm === `${e.id}:${d.id}` && (
          <small>所见版本 {mergeHead.slice(0, 12)}</small>
        )}
        {confirm === `${e.id}:${d.id}` ? (
          <button
            disabled={!!busy}
            onClick={() =>
              void act(d.id, async () => {
                const result = await e.store.mergePR(d.pr.number, mergeHead);
                setNotice(
                  result.queued
                    ? "合并已排队，请等待处理结果。"
                    : result.merged
                      ? "PR 已合并。"
                      : result.message || "合并尚未完成，请重新查看条件。",
                );
              })
            }
          >
            确认合并 PR #{d.pr.number}
          </button>
        ) : (
          <button
            onClick={() => {
              setMergeHead(d.pr.headSha);
              setConfirm(`${e.id}:${d.id}`);
            }}
          >
            合并
          </button>
        )}
      </div>
    ) : (
      <div className="periphery-row need" key={d.id}>
        <button
          onClick={() =>
            d.message.from === "queen"
              ? queen(e.id)
              : ui.selectObject({
                  hiveId: e.id,
                  kind: "agent",
                  id: d.message.from.replace(/^forager:/, ""),
                })
          }
        >
          {messagePreview(d.message)}
        </button>
        <form
          onSubmit={(ev) => {
            ev.preventDefault();
            const body = String(
              new FormData(ev.currentTarget).get("reply") || "",
            );
            if (body.trim())
              void act(d.id, async () => {
                await api.sendMessage(e.id, {
                  to: d.message.from,
                  kind: "reply",
                  chainId: d.message.chainId,
                  body,
                });
                await e.store.refresh();
              });
          }}
        >
          <input
            name="reply"
            aria-label={`答复 ${messagePreview(d.message)}`}
            required
          />
          <button disabled={!!busy}>答复</button>
        </form>
      </div>
    );
  return (
    <>
      <header className="stars-mark">
        <b>SwarmJet</b>
        <button
          onClick={() => {
            if (state.overlay) ui.close();
            ui.selectProject();
          }}
        >
          全部项目
        </button>
        {current && <span> / {current.name}</span>}
      </header>
      <section className="stars-cartouche">
        <h1>{current?.name ?? "全部项目"}</h1>
        <span>{summary}</span>
        <select
          aria-label="选择项目"
          value={state.hiveId ?? ""}
          onChange={(e) => ui.selectProject(e.target.value || undefined)}
        >
          <option value="">全部项目</option>
          {projects.map((p) => (
            <option key={p.hive.id} value={p.hive.id}>
              {p.hive.name}
            </option>
          ))}
        </select>
        <button
          onClick={() => {
            while (
              ui.getSnapshot().overlay ||
              ui.getSnapshot().platformPanel ||
              ui.getSnapshot().selection ||
              ui.getSnapshot().projectPanel
            )
              ui.close();
            setCreate(true);
          }}
        >
          创建项目
        </button>
      </section>
      <nav className="stars-tools" aria-label="星图工具">
        {(["brief", "list", "timeline", "legend", "display"] as Overlay[]).map(
          (o, i) => (
            <button
              key={o}
              aria-pressed={state.overlay === o}
              onClick={() =>
                state.overlay === o ? ui.close() : ui.openOverlay(o)
              }
            >
              {["简报", "列表", "时间", "图例", "显示"][i]}
            </button>
          ),
        )}
        <button onClick={() => ui.openPlatform()}>平台设置</button>
        {current && <button onClick={() => ui.openProject()}>项目面板</button>}
      </nav>
      {(!projects.length ||
        (setup && (!setup.github.configured || !setup.models.available))) && (
        <section className="periphery-onboarding" aria-label="首次使用">
          <h2>开始你的第一个项目</h2>
          {!setup ? (
            <p role="status">
              {platformError || "正在读取接入和模型状态…"}
              {platformError && (
                <button onClick={() => setPlatformAttempt((n) => n + 1)}>
                  重新读取状态
                </button>
              )}
            </p>
          ) : (
            <ol>
              {[
                [
                  setup.github.configured && setup.github.installations > 0,
                  "接入 GitHub",
                  () => ui.openPlatform("setup"),
                ],
                [
                  setup.models.available && models?.gatewayAvailable === true,
                  "填写模型凭证",
                  () => ui.openPlatform("models"),
                ],
                [
                  projects.length > 0,
                  "创建第一个项目",
                  () => {
                    while (
                      ui.getSnapshot().overlay ||
                      ui.getSnapshot().platformPanel ||
                      ui.getSnapshot().selection ||
                      ui.getSnapshot().projectPanel
                    )
                      ui.close();
                    setCreate(true);
                  },
                ],
              ].map(([done, label, run]) => (
                <li key={String(label)} data-complete={!!done}>
                  <span aria-hidden="true">{done ? "★" : "☆"}</span>
                  <button onClick={run as () => void}>
                    {String(label)}
                    {done ? "，已完成" : "，待完成"}
                  </button>
                </li>
              ))}
            </ol>
          )}
        </section>
      )}
      {state.overlay && state.overlay !== "timeline" && (
        <section
          ref={panel}
          tabIndex={-1}
          className={`periphery-surface periphery-${state.overlay}`}
          role="dialog"
          aria-label={
            {
              brief: "简报",
              list: "列表",
              legend: "图例",
              display: "显示设置",
            }[state.overlay]
          }
        >
          <button className="periphery-close" onClick={() => ui.close()}>
            关闭
          </button>
          {state.overlay === "brief" && (
            <>
              <h2>今天，有这些事</h2>
              <p>从项目的最新状态开始。</p>
              <h3>需要你</h3>
              {!loaded ? (
                <p role="status">正在读取项目…</p>
              ) : decisions.length ? (
                decisions.map(decisionRow)
              ) : (
                <p>暂时没有等你决定的事。</p>
              )}
              <h3>正在进行</h3>
              {active.slice(0, 3).map(({ e, a }) => (
                <button
                  className="periphery-line"
                  key={a.id}
                  onClick={() =>
                    ui.selectObject({ hiveId: e.id, kind: "agent", id: a.id })
                  }
                >
                  {e.s.hive.name} / {a.task || a.id}
                  <span>{e.derived.agentStatuses[a.id]}</span>
                </button>
              ))}
              {!active.length && (
                <p>{loaded ? "当前没有工作中的 Agent。" : "正在读取 Agent…"}</p>
              )}
              {active.length > 3 && (
                <button onClick={() => ui.openOverlay("list")}>
                  查看其余 {active.length - 3} 个 Agent
                </button>
              )}
              <h3>需要留意</h3>
              {attention.map(({ e, a }) => (
                <button
                  className="periphery-line"
                  key={a.id}
                  onClick={() =>
                    ui.selectObject({ hiveId: e.id, kind: "agent", id: a.id })
                  }
                >
                  {a.task || a.id}
                  <span>
                    {a.state === "failed"
                      ? "失败"
                      : e.derived.agentStatuses[a.id]}
                  </span>
                </button>
              ))}
              {setup &&
                (!setup.models.available ||
                  models?.gatewayAvailable === false) && (
                  <button onClick={() => ui.openPlatform("models")}>
                    {models?.gatewayAvailable === false
                      ? "模型服务无法连接，请检查服务。"
                      : setup.models.configured
                        ? "凭证已保存，目前无可用凭证。"
                        : "尚无可用模型凭证。"}
                  </button>
                )}
              {platformError && (
                <p role="alert">
                  无法读取平台状态：{platformError}
                  <button onClick={() => setPlatformAttempt((n) => n + 1)}>
                    重新读取状态
                  </button>
                </p>
              )}
              {!attention.length && !platformError && (
                <p>
                  {setup && loaded
                    ? "已读取最新平台与项目状态。"
                    : "正在检查平台与项目状态…"}
                </p>
              )}
              <h3>昨晚完成</h3>
              <p>
                Agent 完成{" "}
                {
                  entries
                    .flatMap((e) => e.s.agents)
                    .filter(
                      (a) =>
                        a.state === "completed" &&
                        Date.parse(a.updatedAt) >=
                          new Date().setHours(-6, 0, 0, 0) &&
                        Date.parse(a.updatedAt) <
                          new Date().setHours(6, 0, 0, 0),
                    ).length
                }{" "}
                个（按昨晚 18:00 至今天 06:00 的 completed 更新时间统计）
              </p>
              <p>昨晚合并数、昨晚花费：暂时无法获取。</p>
              <p>
                {usage
                  ? `今天花费 $${usage.totals.costUsd.toFixed(2)}`
                  : "正在读取今天花费…"}
              </p>
              {entries.flatMap((e) =>
                e.s.prs
                  .filter((p) => p.merged)
                  .slice(0, 3)
                  .map((p) => (
                    <button
                      className="periphery-line"
                      key={`${e.id}:${p.number}`}
                      onClick={() =>
                        ui.selectObject({
                          hiveId: e.id,
                          kind: "pr",
                          id: `${e.id}:pr:${p.number}`,
                          number: p.number,
                        })
                      }
                    >
                      近期已合并 PR #{p.number} {p.title}
                    </button>
                  )),
              )}
              <footer>
                <button onClick={() => ui.close()}>进入星图</button>
                <label>
                  <input
                    type="checkbox"
                    checked={skip}
                    onChange={(e) => {
                      setSkip(e.target.checked);
                      preference.write(
                        "sj-brief",
                        e.target.checked ? "off" : "on",
                      );
                    }}
                  />
                  下次直接进入星图
                </label>
              </footer>
            </>
          )}
          {state.overlay === "list" && (
            <>
              <h2>列表</h2>
              <label>
                查找项目、Agent、PR 或 Issue
                <input
                  autoFocus
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
              </label>
              <h3>等你决定</h3>
              {decisions
                .filter(({ e, d }) =>
                  `${e.s.hive.name} ${d.kind === "pr" ? `PR #${d.pr.number} ${d.pr.title}` : messagePreview(d.message)}`
                    .toLowerCase()
                    .includes(search.toLowerCase()),
                )
                .map(decisionRow)}
              {[...entries]
                .sort(
                  (a, b) =>
                    Number(b.s.queen.enabled) - Number(a.s.queen.enabled) ||
                    Number(b.derived.needsAttention) -
                      Number(a.derived.needsAttention),
                )
                .map((e) => (
                  <section key={e.id}>
                    <h3>
                      <button
                        onClick={() => {
                          ui.close();
                          ui.selectProject(e.id);
                        }}
                      >
                        {e.s.hive.name}
                      </button>
                      {!e.s.queen.enabled && " / 已停用"}
                    </h3>
                    {`${e.s.hive.name} 主 Agent`
                      .toLowerCase()
                      .includes(search.toLowerCase()) && (
                      <button
                        className="periphery-line"
                        onClick={() => queen(e.id)}
                      >
                        主 Agent / 打开详情
                      </button>
                    )}
                    {[...e.s.agents]
                      .sort(
                        (a, b) =>
                          Number(
                            ["completed", "cancelled", "disabled"].includes(
                              a.state,
                            ),
                          ) -
                          Number(
                            ["completed", "cancelled", "disabled"].includes(
                              b.state,
                            ),
                          ),
                      )
                      .filter((a) =>
                        `${e.s.hive.name} ${a.id} ${a.task} ${a.issueNumber}`
                          .toLowerCase()
                          .includes(search.toLowerCase()),
                      )
                      .map((a) => (
                        <article className="periphery-row" key={a.id}>
                          <button
                            onClick={() =>
                              ui.selectObject({
                                hiveId: e.id,
                                kind: "agent",
                                id: a.id,
                              })
                            }
                          >
                            {a.task || a.id} / {e.derived.agentStatuses[a.id]} /
                            详情
                          </button>
                          <form
                            onSubmit={(ev) => {
                              ev.preventDefault();
                              const body = String(
                                new FormData(ev.currentTarget).get("body") ||
                                  "",
                              );
                              if (body.trim())
                                void act(a.id, () =>
                                  api.interject(e.id, a.id, body, a.chainId),
                                );
                            }}
                          >
                            <input
                              name="body"
                              required
                              aria-label={`向 ${a.id} 插话`}
                            />
                            <button disabled={!!busy}>插话</button>
                          </form>
                          {![
                            "completed",
                            "cancelled",
                            "failed",
                            "disabled",
                          ].includes(a.state) && (
                            <button
                              disabled={!!busy}
                              onClick={() =>
                                void act(a.id, async () => {
                                  await api.cancelForager(a.id);
                                  await e.store.refresh();
                                })
                              }
                            >
                              取消 Agent
                            </button>
                          )}
                        </article>
                      ))}
                    {e.s.prs
                      .filter((p) =>
                        `${e.s.hive.name} PR #${p.number} ${p.title}`
                          .toLowerCase()
                          .includes(search.toLowerCase()),
                      )
                      .map((p) => (
                        <button
                          className="periphery-line"
                          key={p.number}
                          onClick={() =>
                            ui.selectObject({
                              hiveId: e.id,
                              kind: "pr",
                              id: `${e.id}:pr:${p.number}`,
                              number: p.number,
                            })
                          }
                        >
                          PR #{p.number} {p.title} /{" "}
                          {p.merged
                            ? "已合并"
                            : p.ready
                              ? "等你决定"
                              : "检查中"}{" "}
                          / 详情
                        </button>
                      ))}
                    {e.s.queue
                      .filter((q) =>
                        `${q.task} ${q.role} ${q.agentId}`
                          .toLowerCase()
                          .includes(search.toLowerCase()),
                      )
                      .map((q) => (
                        <p key={`queue:${q.agentId}`}>
                          排队中 / {q.task} / {q.role} / 第 {q.position}{" "}
                          位。等待可用名额；具体等待原因暂时无法获取。
                        </p>
                      ))}
                    {!e.s.queen.enabled && (
                      <p>主 Agent 已停用，请在项目设置里启用后派活。</p>
                    )}
                    {e.s.signals
                      .filter((s) =>
                        `${s.title} #${s.number}`
                          .toLowerCase()
                          .includes(search.toLowerCase()),
                      )
                      .map((s) => (
                        <div className="periphery-row" key={s.messageId}>
                          <button
                            onClick={() =>
                              ui.selectObject({
                                hiveId: e.id,
                                kind: "signal",
                                id: s.messageId,
                                number: s.number,
                              })
                            }
                          >
                            {s.kind === "issue"
                              ? "Issue"
                              : s.kind === "pull_request"
                                ? "PR"
                                : "检查结果"}{" "}
                            #{s.number} {s.title} / 详情
                          </button>
                          {s.kind === "issue" &&
                            !s.claimed &&
                            e.s.queen.enabled && (
                              <button
                                disabled={!!busy}
                                onClick={() =>
                                  void act(s.messageId, () =>
                                    api.assignIssue(e.id, s.number),
                                  )
                                }
                              >
                                交给主 Agent
                              </button>
                            )}
                        </div>
                      ))}
                  </section>
                ))}
              {!loaded && <p role="status">正在读取项目列表…</p>}
            </>
          )}
          {state.overlay === "legend" && (
            <>
              <h2>图例</h2>
              <dl>
                {[
                  ["☉", "主 Agent"],
                  ["☄", "工作中的 Agent"],
                  ["○", "空闲 Agent"],
                  ["×", "失败 Agent"],
                  ["♄", "PR；虚线环表示等你决定"],
                  ["⊙", "你；有事等你时亮起"],
                  ["＋", "待处理 Issue"],
                  ["◌", "排队中"],
                ].map(([symbol, label]) => (
                  <div key={label}>
                    <dt aria-hidden="true">{symbol}</dt>
                    <dd>{label}</dd>
                  </div>
                ))}
              </dl>
            </>
          )}
          {state.overlay === "display" && (
            <>
              <h2>显示设置</h2>
              <label>
                风格
                <select
                  aria-label="风格"
                  value={theme.style}
                  onChange={(e) =>
                    themeStore.setStyle(e.target.value as "classic" | "simple")
                  }
                >
                  <option value="classic">经典</option>
                  <option value="simple">简洁</option>
                </select>
              </label>
              <label>
                主题
                <select
                  aria-label="主题"
                  value={theme.choice}
                  onChange={(e) =>
                    themeStore.setTheme(e.target.value as ThemeChoice)
                  }
                >
                  {Object.entries(THEMES).map(([id, t]) => (
                    <option key={id} value={id}>
                      {t.label}
                    </option>
                  ))}
                  <option value="system">跟随系统</option>
                </select>
              </label>
              <label>
                <input
                  type="checkbox"
                  checked={theme.render.grid}
                  onChange={(e) => themeStore.setGrid(e.target.checked)}
                />
                显示经纬线
              </label>
              <p>
                / 对话，A 项目，, 平台，T 时间，C 列表，B 简报，L 图例，S
                风格，G 经纬线，D 深浅色，Esc 退出一层。
              </p>
            </>
          )}
          {error && <p role="alert">{error}</p>}
          {notice && <p role="status">{notice}</p>}
        </section>
      )}
      <form
        className="stars-say"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!text.trim()) return;
          if (route) {
            route.run();
            setText("");
            return;
          }
          if (!current) {
            setError("先选择一个项目，再对主 Agent 说话。");
            return;
          }
          const submitted = text;
          if (await act("say", () => api.talkToQueen(current.id, submitted)))
            setText((draft) => draft === submitted ? "" : draft);
        }}
      >
        <input
          ref={input}
          aria-label="对话与命令"
          aria-describedby="periphery-say-hint"
          placeholder={
            current ? "对主 Agent 说，或输入命令" : "输入项目名、模型或列表"
          }
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <button disabled={!text.trim() || (!!busy && !route)}>
          {route ? "前往" : "发送"}
        </button>
        <small id="periphery-say-hint">
          {text
            ? `回车${route ? route.label : current ? "发给主 Agent" : "前请先选择项目"}`
            : "按 / 开始输入"}
        </small>
      </form>
      {error && !state.overlay && (
        <p className="periphery-toast" role="alert">
          {error}
        </p>
      )}
      {create && (
        <CreateProject
          ui={ui}
          reloadProjects={props.reloadProjects}
          onClose={() => setCreate(false)}
        />
      )}
    </>
  );
}
