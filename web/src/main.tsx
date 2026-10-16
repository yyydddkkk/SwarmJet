/// <reference types="vite/client" />
import React, { lazy, Suspense, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { ToastProvider } from "./ToastProvider";
import "./styles.css";
import "./project-setup.css";
import "./workbench.css";
import "./console-shell.css";
// Development-only, disposable design study. Never replaces the production console.
const Prototype =
  import.meta.env.DEV &&
  new URLSearchParams(location.search).get("prototype") === "workbench"
    ? lazy(() => import("./WorkbenchPrototype"))
    : null;
// New Hive console ("honey garden"). Serves #/hives routes (plus the #/icons
// reference page) from fake fixtures only; the existing console keeps its own
// routes and pages.
const HiveConsole = lazy(() => import("./hive/HiveConsoleApp"));

function Root() {
  const [hash, setHash] = useState(location.hash);
  useEffect(() => {
    const onHash = () => setHash(location.hash);
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  if (hash.startsWith("#/hives") || hash.startsWith("#/icons"))
    return (
      <Suspense fallback={<div className="loading">正在打开 Hive 控制台…</div>}>
        <HiveConsole />
      </Suspense>
    );
  return <App />;
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ToastProvider>
    {Prototype ? (
      <Suspense fallback={null}>
        <Prototype />
      </Suspense>
    ) : (
      <Root />
    )}
    </ToastProvider>
  </React.StrictMode>,
);
