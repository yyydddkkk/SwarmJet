/// <reference types="vite/client" />
import { StrictMode, Suspense, lazy } from "react";
import { createRoot } from "react-dom/client";
const Stars = lazy(() => import("./stars/StarsApp"));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Suspense fallback={<p>正在打开项目…</p>}>
      <Stars />
    </Suspense>
  </StrictMode>,
);
