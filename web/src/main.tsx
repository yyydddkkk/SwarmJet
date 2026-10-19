/// <reference types="vite/client" />
import { StrictMode, Suspense, lazy } from "react";
import { createRoot } from "react-dom/client";
import "./hive/hive-console.css";

// The Hive console is the whole app after round 3: one administrator signs in,
// then lands on #/hives. Old workbench routes redirect there.
const HiveConsole = lazy(() => import("./hive/HiveConsoleApp"));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Suspense fallback={<div className="hg-boot-screen">正在打开蜂巢控制台…</div>}>
      <HiveConsole />
    </Suspense>
  </StrictMode>,
);
