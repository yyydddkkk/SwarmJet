// Tiny polling resource used by the Hive console. The conversation stream and
// the Swarm panel refresh every five seconds this round; there is no SSE or
// WebSocket yet (ADR section 18, "实时" is a later round).
import { useCallback, useEffect, useRef, useState } from "react";
import { HiveAPIError } from "./api";

export type Resource<T> = {
  data: T | undefined;
  loading: boolean;
  error: string;
  errorStatus: number | undefined;
  reload: () => void;
};

export function useResource<T>(
  load: (signal: AbortSignal) => Promise<T>,
  deps: readonly unknown[],
  intervalMs = 0,
): Resource<T> {
  const [data, setData] = useState<T | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [errorStatus, setErrorStatus] = useState<number | undefined>(undefined);
  const [attempt, setAttempt] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    let active = true;
    let controller: AbortController | undefined;
    const run = async (initial: boolean) => {
      controller?.abort();
      controller = new AbortController();
      const current = controller;
      if (initial) {
        setLoading(true);
        setError("");
        setErrorStatus(undefined);
      }
      try {
        const next = await loadRef.current(current.signal);
        if (!active || current.signal.aborted) return;
        setData(next);
        setError("");
        setErrorStatus(undefined);
      } catch (cause) {
        if (!active || current.signal.aborted) return;
        if (cause instanceof DOMException && cause.name === "AbortError") return;
        setError(cause instanceof Error ? cause.message : String(cause));
        setErrorStatus(cause instanceof HiveAPIError ? cause.status : undefined);
      } finally {
        if (active && !current.signal.aborted) setLoading(false);
      }
    };
    void run(true);
    const timer =
      intervalMs > 0 ? window.setInterval(() => void run(false), intervalMs) : undefined;
    return () => {
      active = false;
      if (timer !== undefined) window.clearInterval(timer);
      controller?.abort();
    };
    // The callers pass a fixed-shape dependency list; the loader itself is read
    // through a ref so a new closure does not restart the interval.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt, intervalMs, ...deps]);

  const reload = useCallback(() => setAttempt(value => value + 1), []);
  return { data, loading, error, errorStatus, reload };
}
