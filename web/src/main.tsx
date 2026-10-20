/// <reference types="vite/client" />
import { StrictMode, Suspense, lazy, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
const Legacy = lazy(() => import("./hive/HiveConsoleApp"));
const Stars = lazy(() => import("./stars/StarsApp"));
function App(){const [hash,setHash]=useState(location.hash);useEffect(()=>{const change=()=>setHash(location.hash);addEventListener('hashchange',change);return()=>removeEventListener('hashchange',change);},[]);return hash.startsWith('#/legacy')?<Legacy/>:<Stars/>;}
createRoot(document.getElementById("root")!).render(<StrictMode><Suspense fallback={<p>正在打开项目…</p>}><App/></Suspense></StrictMode>);
