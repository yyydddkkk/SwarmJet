// Entry point for the "honey garden" console. Mounted only for #/hives (and the
// #/icons reference page) — see web/src/main.tsx. The pages read and write the
// Hive REST endpoints; fixtures are now used by Playwright only.
import { useEffect, useState } from "react";
import HivesPage from "./HivesPage";
import HivePage from "./HivePage";
import ForagerPage from "./ForagerPage";
import IconLab from "./IconLab";
import { EmptyState } from "./components";
import { Icon } from "./icons";

import "./hive-console.css";

type Route =
  | { name: "hives" }
  | { name: "hive"; hiveId: string; section: string }
  | { name: "forager"; hiveId: string; foragerId: string; section: string }
  | { name: "approvals" }
  | { name: "system" }
  | { name: "icons" }
  | { name: "unknown" };

function parseRoute(hash: string): Route {
  const path = hash.replace(/^#/, "").split("?")[0] || "/hives";
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "approvals") return { name: "approvals" };
  if (parts[0] === "system") return { name: "system" };
  if (parts[0] === "icons") return { name: "icons" };
  if (parts[0] !== "hives") return { name: "unknown" };
  if (parts.length === 1) return { name: "hives" };
  if (parts[2] === "foragers" && parts[3]) {
    return {
      name: "forager",
      hiveId: parts[1],
      foragerId: decodeURIComponent(parts[3]),
      section: parts[4] ?? "conversation",
    };
  }
  return { name: "hive", hiveId: parts[1], section: parts[2] ?? "conversation" };
}

function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(location.hash));
  useEffect(() => {
    const onHash = () => setRoute(parseRoute(location.hash));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return route;
}

const placeholders: Record<"approvals" | "system", { title: string; hint: string }> = {
  approvals: {
    title: "审批（全局）",
    hint: "所有 Hive 的待审批会汇总到这里，导航栏显示计数。这一轮先放占位。",
  },
  system: {
    title: "系统",
    hint: "Node、MCP Token、网络与代理、成员、CLI 登录配置。这一轮先放占位。",
  },
};

export default function HiveConsoleApp() {
  const route = useRoute();
  const inHive = route.name === "hives" || route.name === "hive" || route.name === "forager";

  return (
    <div className="hg-app">
      <a
        className="hg-skip"
        href="#hg-main"
        onClick={event => {
          event.preventDefault();
          document.getElementById("hg-main")?.focus();
        }}
      >
        跳至主要内容
      </a>
      <header className="hg-topbar">
        <a className="hg-brand" href="#/hives">
          <span className="hg-brand-mark" aria-hidden="true">
            <Icon name="bee" size={32} />
          </span>
          <span className="hg-brand-name">SwarmJet</span>
          <span className="hg-brand-note">蜂巢控制台</span>
        </a>
        <nav className="hg-nav" aria-label="控制台导航">
          <a href="#/hives" className={inHive ? "is-active" : undefined} aria-current={inHive ? "page" : undefined}>
            Hive（蜂巢）
          </a>
          <a
            href="#/approvals"
            className={route.name === "approvals" ? "is-active" : undefined}
            aria-current={route.name === "approvals" ? "page" : undefined}
          >
            审批
          </a>
          <a
            href="#/system"
            className={route.name === "system" ? "is-active" : undefined}
            aria-current={route.name === "system" ? "page" : undefined}
          >
            系统
          </a>
        </nav>
        <p className="hg-topbar-note">
          平台实时数据 · <a href="#/icons">图标规范</a>
        </p>
      </header>

      <main id="hg-main" className="hg-main" tabIndex={-1}>
        {route.name === "hives" ? (
          <HivesPage key="hives" />
        ) : route.name === "hive" ? (
          <HivePage key={`hive-${route.hiveId}-${route.section}`} hiveId={route.hiveId} section={route.section} />
        ) : route.name === "forager" ? (
          <ForagerPage
            key={`forager-${route.hiveId}-${route.foragerId}-${route.section}`}
            hiveId={route.hiveId}
            foragerId={route.foragerId}
            section={route.section}
          />
        ) : route.name === "icons" ? (
          <IconLab />
        ) : route.name === "unknown" ? (
          <EmptyState
            title="页面不存在。"
            hint="检查一下链接，或者回到 Hive 列表。"
            action={
              <a className="hg-btn" href="#/hives">
                返回 Hive 列表
              </a>
            }
          />
        ) : (
          <div className="hg-page">
            <header className="hg-page-head">
              <div>
                <h1>{placeholders[route.name].title}</h1>
              </div>
            </header>
            <section className="hg-panel">
              <div className="hg-panel-body">
                <EmptyState title={`${placeholders[route.name].title}还没实现。`} hint={placeholders[route.name].hint} />
              </div>
            </section>
          </div>
        )}
      </main>
    </div>
  );
}
