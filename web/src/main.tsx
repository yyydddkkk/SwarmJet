/// <reference types="vite/client" />
import { StrictMode, Suspense, lazy } from "react";
import { createRoot } from "react-dom/client";
import "./data/transition.css";
const Stars = lazy(() => import("./stars/StarsApp"));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Suspense fallback={<main className="stars-transition"><div className="stars-transition-inner"><h1>SwarmJet</h1><p role="status">正在打开项目…</p></div></main>}>
      <Stars />
    </Suspense>
  </StrictMode>,
);
