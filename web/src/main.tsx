/// <reference types="vite/client" />
import React, { lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import "./styles.css";
import "./project-setup.css";
import "./workbench.css";
// Development-only, disposable design study. Never replaces the production console.
const Prototype =
  import.meta.env.DEV &&
  new URLSearchParams(location.search).get("prototype") === "workbench"
    ? lazy(() => import("./WorkbenchPrototype"))
    : null;
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    {Prototype ? (
      <Suspense fallback={null}>
        <Prototype />
      </Suspense>
    ) : (
      <App />
    )}
  </React.StrictMode>,
);
