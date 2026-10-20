// Entry point and shell for the "honey garden" console (ADR section 18).
// After sign-in every route lives here: Hive list/pages, 接入 (#/setup) and
// 模型 (#/models). Unknown and empty routes fall back to #/hives. The #/icons
// reference page stays reachable directly but is not part of the navigation.
import { useEffect, useState } from "react";
import HivesPage from "./HivesPage";
import HivePage from "./HivePage";
import ForagerPage from "./ForagerPage";
import SetupPage from "./SetupPage";
import ModelsPage from "./ModelsPage";
import LoginScreen from "./LoginScreen";
import IconLab from "./IconLab";
import { BeeLoader, ErrorNotice } from "./components";
import { hiveAPI } from "./api";
import type { Principal } from "./types";
import { Icon } from "./icons";

import "./hive-console.css";

type Route =
  | { name: "hives" }
  | { name: "hive"; hiveId: string; section: string }
  | { name: "forager"; hiveId: string; foragerId: string; section: string }
  | { name: "setup"; query: URLSearchParams }
  | { name: "models" }
  | { name: "icons" }
  | { name: "redirect" };

function parseRoute(hash: string): Route {
  const raw = hash.replace(/^#/, "");
  const [rawPath, rawQuery = ""] = raw.split("?");
  const path = rawPath || "/";
  const parts = path.split("/").filter(Boolean);
  const query = new URLSearchParams(rawQuery);
  if (path === "/") return { name: "redirect" };
  if (parts[0] === "setup") return { name: "setup", query };
  if (parts[0] === "models") return { name: "models" };
  if (parts[0] === "icons") return { name: "icons" };
  if (parts[0] !== "hives") return { name: "redirect" };
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

type SessionState =
  | { state: "loading" }
  | { state: "anonymous" }
  | { state: "authenticated"; principal: Principal }
  | { state: "error"; message: string };

export default function HiveConsoleApp() {
  const route = useRoute();
  const [session, setSession] = useState<SessionState>({ state: "loading" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let active = true;
    setSession({ state: "loading" });
    hiveAPI
      .getSession()
      .then(principal => {
        if (active) setSession({ state: "authenticated", principal });
      })
      .catch(cause => {
        if (!active) return;
        if (cause instanceof Error && "status" in cause && (cause as { status: number }).status === 401) {
          setSession({ state: "anonymous" });
        } else {
          setSession({
            state: "error",
            message: cause instanceof Error ? cause.message : String(cause),
          });
        }
      });
    return () => {
      active = false;
    };
  }, [attempt]);

  // "#/" and every retired route land on the Hive list.
  useEffect(() => {
    if (route.name === "redirect") location.replace("#/hives");
  }, [route]);

  if (session.state === "loading") {
    return (
      <div className="hg-app hg-boot-screen">
        <BeeLoader label="正在连接蜂巢控制台…" />
      </div>
    );
  }

  if (session.state === "error") {
    return (
      <div className="hg-app hg-boot-screen">
        <ErrorNotice message={session.message} onRetry={() => setAttempt(value => value + 1)} />
      </div>
    );
  }

  if (session.state === "anonymous") {
    return (
      <LoginScreen
        onLogin={() => {
          const raw = location.hash.replace(/^#/, "");
          if (!raw || raw === "/") location.hash = "/hives";
          setAttempt(value => value + 1);
        }}
      />
    );
  }

  const { principal } = session;
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
            Hive
          </a>
          <a
            href="#/setup"
            className={route.name === "setup" ? "is-active" : undefined}
            aria-current={route.name === "setup" ? "page" : undefined}
          >
            接入
          </a>
          <a
            href="#/models"
            className={route.name === "models" ? "is-active" : undefined}
            aria-current={route.name === "models" ? "page" : undefined}
          >
            模型
          </a>
        </nav>
        <div className="hg-session">
          <span className="hg-session-user">
            <Icon name="user" size={16} />
            {principal.displayName || principal.username || "管理员"}
          </span>
          <button
            className="hg-btn hg-btn-small"
            type="button"
            onClick={async () => {
              try {
                await hiveAPI.logout();
              } catch {
                // The local session is dropped either way.
              }
              setSession({ state: "anonymous" });
            }}
          >
            <Icon name="back" size={16} />
            退出
          </button>
        </div>
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
        ) : route.name === "setup" ? (
          <SetupPage key={route.query.toString()} query={route.query} />
        ) : route.name === "models" ? (
          <ModelsPage />
        ) : route.name === "icons" ? (
          <IconLab />
        ) : (
          <BeeLoader label="正在返回 Hive 列表…" />
        )}
      </main>

      <footer className="hg-footer">
        SwarmJet <span aria-hidden="true">·</span> 与 Queen 对话管理 Hive、选择方法论，也可以连接本地 Agent。
      </footer>
    </div>
  );
}
