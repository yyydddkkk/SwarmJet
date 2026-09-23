import { useEffect, useState } from "react";
import { api } from "./api";

export type AgentSpec = { id: string; name: string; executable: string; setupCommand: string };
export type AgentCatalog = { agents: AgentSpec[]; enabled: string[] };

export function useAgentCatalog() {
  const [catalog, setCatalog] = useState<AgentCatalog>({ agents: [], enabled: [] });
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    api<AgentCatalog>("/runtime/agents", "GET", undefined, controller.signal)
      .then(setCatalog)
      .catch((e) => { if (!controller.signal.aborted) setError(e.message); });
    return () => controller.abort();
  }, []);
  return { catalog, setCatalog, error, setError };
}
