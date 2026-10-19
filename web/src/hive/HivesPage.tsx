// Hive list: the home of the honey-garden console (ADR section 18).
import { useMemo, useState } from "react";
import { hiveOverviews } from "../fixtures/hive-console";
import { EmptyState, StatusBadge } from "./components";
import { Icon } from "./icons";

export default function HivesPage() {
  const [query, setQuery] = useState("");
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return hiveOverviews;
    return hiveOverviews.filter(({ hive }) =>
      `${hive.name} ${hive.repository}`.toLowerCase().includes(needle),
    );
  }, [query]);

  return (
    <div className="hg-page">
      <header className="hg-page-head">
        <div>
          <h1>Hive（蜂巢）</h1>
          <p className="hg-page-sub">
            每个 Hive 对应一个仓库，里面有 Queen（蜂王）和一群 Forager。
          </p>
        </div>
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
      </header>

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
    </div>
  );
}
