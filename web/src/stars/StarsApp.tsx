import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import {
  DataProvider,
  useDataSource,
  createProjectStore,
  type HiveOverview,
  type Principal,
  type DataSource,
} from "../data";
import CanvasSlot from "./CanvasSlot";
import TimelineSlot from "./TimelineSlot";
import PeripherySlot from "./PeripherySlot";
import LoginSlot from "./LoginSlot";
import { createStarsUI, useStarsUI } from "./ui";
import type { StarsSlotProps } from "./slots";
import "./stars.css";
const PanelsSlot = lazy(() => import("./PanelsSlot"));
function DeferredPanels(props: StarsSlotProps) {
  const state = useStarsUI(props.ui);
  const active = !!(state.selection || state.projectPanel || state.platformPanel);
  const [opened, setOpened] = useState(false);
  useEffect(() => { if (active) setOpened(true); }, [active]);
  if (!active && !opened) return null;
  return <div className="stars-panels-host" aria-hidden={!active} inert={!active}>
    <Suspense fallback={null}><PanelsSlot {...props} /></Suspense>
  </div>;
}
/** Records whether the last interaction was a pointer, so scripted focus after a click shows no ring. */
function useInputModality() {
  useEffect(() => {
    const root = document.documentElement;
    const pointer = () => { root.dataset.input = "pointer"; };
    const keyboard = (e: KeyboardEvent) => { if (!e.ctrlKey && !e.metaKey && !e.altKey) delete root.dataset.input; };
    window.addEventListener("pointerdown", pointer, true);
    window.addEventListener("keydown", keyboard, true);
    return () => {
      window.removeEventListener("pointerdown", pointer, true);
      window.removeEventListener("keydown", keyboard, true);
      delete root.dataset.input;
    };
  }, []);
}
export default function StarsApp({ source }: { source?: DataSource } = {}) {
  useInputModality();
  return (
    <DataProvider source={source}>
      <StarsSession />
    </DataProvider>
  );
}
function StarsSession() {
  const source = useDataSource();
  const [principal, setPrincipal] = useState<Principal>();
  const [anonymous, setAnonymous] = useState(false);
  const [sessionNotice, setSessionNotice] = useState("");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const expired = () => {
      setPrincipal(undefined);
      setAnonymous(true);
      setSessionNotice("会话已过期，请重新登录后继续。");
    };
    window.addEventListener("swarmjet:session-expired", expired);
    return () =>
      window.removeEventListener("swarmjet:session-expired", expired);
  }, []);
  useEffect(() => {
    let active = true;
    source.api
      .getSession()
      .then((p) => {
        if (active) {
          setPrincipal(p);
          setAnonymous(false);
          setSessionNotice("");
          setError("");
        }
      })
      .catch((e) => {
        if (active) {
          if (e.status === 401) setAnonymous(true);
          else setError(e.message);
        }
      });
    return () => {
      active = false;
    };
  }, [source, attempt]);
  if (anonymous)
    return <LoginSlot notice={sessionNotice} onLogin={() => setAttempt((v) => v + 1)} />;
  if (!principal)
    return (
      <main className="stars-transition"><div className="stars-transition-inner">
        <h1>SwarmJet</h1>
        <p role={error ? "alert" : "status"}>{error || "正在连接项目…"}</p>
        {error && <button onClick={() => setAttempt((v) => v + 1)}>重试</button>}
      </div></main>
    );
  return (
    <StarsWorkspace
      principal={principal}
      onLogout={() => {
        setPrincipal(undefined);
        setAnonymous(true);
      }}
    />
  );
}
export function StarsWorkspace({
  principal,
  onLogout,
}: {
  principal: Principal;
  onLogout(): void;
}) {
  const source = useDataSource();
  const ui = useMemo(() => createStarsUI(), []);
  const [projects, setProjects] = useState<HiveOverview[]>([]);
  const [projectsLoading, setProjectsLoading] = useState(true);
  const [projectsLoaded, setProjectsLoaded] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [, render] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setProjectsLoading(true);
    source.api
      .listHives(controller.signal)
      .then((r) => {
        setProjects(r.hives);
        setProjectsLoaded(true);
        setError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setProjectsLoading(false);
      });
    return () => controller.abort();
  }, [source, attempt]);
  const ids = projects.map((p) => p.hive.id).join("\0");
  const stores = useMemo(
    () =>
      new Map(
        projects.map((p) => [p.hive.id, createProjectStore(p.hive.id, source)]),
      ),
    [ids, source],
  );
  useEffect(() => {
    const unsubscribe = [...stores.values()].map((s) => {
      s.start();
      return s.subscribe(() => {
        if (s.getSnapshot().errorStatus === 401) onLogout();
        render((v) => v + 1);
      });
    });
    return () => {
      unsubscribe.forEach((fn) => fn());
      stores.forEach((s) => s.stop());
    };
  }, [stores]);
  useEffect(() => {
    if (location.hash.startsWith("#/setup"))
      ui.openPlatform("setup", location.hash.split("?")[1]);
  }, [ui]);
  const props = {
    ui,
    projects: projects.map((p) => ({
      ...p,
      hive: stores.get(p.hive.id)?.getSnapshot().snapshot?.hive ?? p.hive,
    })),
    stores,
    principal,
    onLogout: async () => {
      await source.api.logout();
      onLogout();
    },
    reloadProjects: () => setAttempt((v) => v + 1),
  };
  const status = [...stores.values()]
    .map((s) => s.getSnapshot())
    .find((s) => s.error || s.retryDelay !== undefined);
  const statusMessage = error || status?.error ||
    (status?.retryDelay !== undefined ? "连接已断开，正在重连" : "");
  const [shownStatus, setShownStatus] = useState("");
  useEffect(() => {
    if (statusMessage) {
      setShownStatus(statusMessage);
      return;
    }
    const timer = setTimeout(() => setShownStatus(""), 300);
    return () => clearTimeout(timer);
  }, [statusMessage]);
  if (!projectsLoaded && (projectsLoading || error)) {
    return (
      <main className="stars-transition">
        <div className="stars-transition-inner">
          <h1>SwarmJet</h1>
          <p role={error ? "alert" : "status"}>{error || "正在读取项目…"}</p>
          {error && <button onClick={() => setAttempt((v) => v + 1)}>重试</button>}
        </div>
      </main>
    );
  }
  return (
    <main className="stars-app">
      <CanvasSlot {...props} />
      <PeripherySlot {...props} />
      <DeferredPanels {...props} />
      <TimelineSlot {...props} />
      <div className={`stars-status${statusMessage ? " open" : ""}`} role={statusMessage ? "alert" : undefined} aria-hidden={!statusMessage} inert={!statusMessage}>
          {shownStatus}
          <button
            onClick={() => {
              setAttempt((v) => v + 1);
              stores.forEach((s) => void s.refresh());
            }}
          >
            重试
          </button>
        </div>
    </main>
  );
}
