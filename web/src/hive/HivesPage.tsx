// Hive list: the home of the honey-garden console (ADR section 18). Cards are
// built from GET /api/hives; the counts come from the store, not fixtures.
import { useMemo, useState } from "react";
import { hiveAPI } from "./api";
import { BeeLoader, EmptyState, ErrorNotice, StatusBadge } from "./components";
import { Icon } from "./icons";
import { useResource } from "./use-resource";
import CreateHiveDialog from "./CreateHiveDialog";

export default function HivesPage() {
  const [query, setQuery] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const resource = useResource(signal => hiveAPI.listHives(signal), []);
  const overviews = resource.data?.hives ?? [];

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return overviews;
    return overviews.filter(({ hive }) =>
      `${hive.name} ${hive.repository}`.toLowerCase().includes(needle),
    );
  }, [overviews, query]);

  return (
    <div className="hg-page">
      <header className="hg-page-head">
        <div>
          <h1>Hive（蜂巢）</h1>
          <p className="hg-page-sub">
            每个 Hive 对应一个仓库，里面有 Queen（蜂王）和一群 Forager。
          </p>
        </div>
        <div className="hg-page-actions">
          <button className="hg-btn hg-btn-primary" type="button" onClick={() => setCreateOpen(true)}>
            <Icon name="plus" size={16} />
            新建 Hive
          </button>
          <div className="hg-search">
            <Icon name="search" size={16} />
            <input
              type="search"
              value={query}
              onChange={event => setQuery(event.target.value)}
              placeholder="搜索名称或仓库"
              aria-label="搜索 Hive"
            />
          </div>
        </div>
      </header>

      {resource.loading && !resource.data ? (
        <BeeLoader label="正在读取 Hive…" />
      ) : resource.error && !resource.data ? (
        <ErrorNotice
          message={resource.error}
          onRetry={resource.reload}
          auth={resource.errorStatus === 401}
          admin={resource.errorStatus === 403}
        />
      ) : (
        <>
          {resource.error ? (
            <ErrorNotice
              message={`${resource.error}（显示的是上一次读取结果）`}
              onRetry={resource.reload}
            />
          ) : null}
          {filtered.length === 0 ? (
            query.trim() ? (
              <EmptyState
                title="没有匹配的 Hive。"
                hint="换一个关键词，或者清空搜索看看全部 Hive。"
              />
            ) : (
              <EmptyState
                title="还没有 Hive。"
                hint="为你的仓库建一个 Hive，再把本地 Agent 接进来。"
                action={
                  <button className="hg-btn hg-btn-primary" type="button" onClick={() => setCreateOpen(true)}>
                    <Icon name="plus" size={16} />
                    新建 Hive
                  </button>
                }
              />
            )
          ) : (
            <ul className="hg-hive-grid">
              {filtered.map((overview, index) => {
                const { hive } = overview;
                return (
                  <li key={hive.id}>
                    <a
                      className="hg-hive-card"
                      href={`#/hives/${hive.id}`}
                      aria-label={`${hive.name}，${hive.repository}`}
                    >
                      <header className="hg-hive-card-head">
                        <span className={`hg-hive-mark hg-hive-mark-${index % 3}`} aria-hidden="true">
                          <Icon name="bee" size={32} />
                        </span>
                        <div>
                          <h2>{hive.name}</h2>
                          <p className="hg-repo">{hive.repository}</p>
                        </div>
                      </header>
                      <dl className="hg-hive-stats">
                        <div>
                          <dt>Queen</dt>
                          <dd>
                            <StatusBadge
                              state={hive.queenEnabled ? "active" : "disabled"}
                              label={hive.queenEnabled ? "已开启" : "未开启"}
                            />
                          </dd>
                        </div>
                        <div>
                          <dt>活跃 Forager</dt>
                          <dd className="hg-stat-value">{overview.activeForagers}</dd>
                        </div>
                        <div>
                          <dt>待审批</dt>
                          <dd
                            className={
                              overview.pendingApprovals > 0
                                ? "hg-stat-value hg-tone-amber"
                                : "hg-stat-value"
                            }
                          >
                            {overview.pendingApprovals}
                          </dd>
                        </div>
                        <div>
                          <dt>Inbox 积压</dt>
                          <dd
                            className={
                              overview.inboxBacklog > 0
                                ? "hg-stat-value hg-tone-amber"
                                : "hg-stat-value"
                            }
                          >
                            {overview.inboxBacklog}
                          </dd>
                        </div>
                      </dl>
                    </a>
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}

      {createOpen ? (
        <CreateHiveDialog
          onClose={() => setCreateOpen(false)}
          onCreated={hiveId => {
            setCreateOpen(false);
            location.hash = `/hives/${hiveId}`;
          }}
        />
      ) : null}
    </div>
  );
}
