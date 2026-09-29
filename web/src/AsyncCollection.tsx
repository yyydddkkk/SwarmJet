import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Empty, ErrorBox } from "./ui";

type CollectionPage<T> = { items: T[]; nextCursor?: string };

export type CollectionState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "empty"; nextCursor: string }
  | { status: "ready"; items: T[]; nextCursor: string };

// The loader changes when filters or the cursor change. Superseded requests
// are aborted so an older result cannot replace the current collection.
export function useAsyncCollection<T>(load: (signal: AbortSignal) => Promise<CollectionPage<T>>) {
  const [state, setState] = useState<CollectionState<T>>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setState({ status: "loading" });
    void load(controller.signal).then(result => {
      if (controller.signal.aborted) return;
      const items = result.items || [];
      const nextCursor = result.nextCursor || "";
      setState(items.length ? { status: "ready", items, nextCursor } : { status: "empty", nextCursor });
    }).catch(cause => {
      if (!controller.signal.aborted)
        setState({ status: "error", message: cause instanceof Error ? cause.message : String(cause) });
    });
    return () => controller.abort();
  }, [load, attempt]);
  const retry = useCallback(() => setAttempt(value => value + 1), []);
  const nextCursor = state.status === "ready" || state.status === "empty" ? state.nextCursor : "";
  return { state, nextCursor, retry };
}

export function AsyncCollectionView<T>({ state, retry, loadingText, empty, children }: {
  state: CollectionState<T>;
  retry: () => void;
  loadingText: string;
  empty: ReactNode;
  children: (items: T[]) => ReactNode;
}) {
  switch (state.status) {
    case "loading": return <p className="async-collection-loading">{loadingText}</p>;
    case "error": return <ErrorBox error={state.message} retry={retry} />;
    case "empty": return <Empty>{empty}</Empty>;
    case "ready": return <>{children(state.items)}</>;
  }
}
