import { api, type DataAPI } from "./api";
import type { ProjectEvent, ProjectSnapshot } from "./types";
import type { DataSource } from "./store";
// No production window globals or URL switches: tests inject this source through
// DataProvider, or construct a ProjectStore directly with it.
export function createMockDataSource(
  initial: Record<string, ProjectSnapshot>,
  overrides: Partial<DataAPI> = {},
) {
  const snapshots = structuredClone(initial);
  const clients = new Set<{
    id: string;
    ready(): void;
    event(e: ProjectEvent): void;
    error(): void;
  }>();
  const calls: { method: string; args: unknown[] }[] = [];
  const mockAPI = Object.fromEntries(
    Object.keys(api).map((method) => [
      method,
      (...args: unknown[]) => {
        calls.push({ method, args });
        const fn = overrides[method as keyof DataAPI] as
          ((...args: unknown[]) => unknown) | undefined;
        if (fn) return fn(...args);
        if (method === "getPRStatus") {
          const pr = snapshots[String(args[0])].prs.find(
            (pr) => pr.number === args[1],
          );
          if (!pr) return Promise.reject(new Error("Missing mock PR status"));
          return Promise.resolve({
            hiveId: args[0],
            prNumber: pr.number,
            draft: false,
            checks: [],
            attestations: [],
            reasons: [],
            ...structuredClone(pr),
          });
        }
        if (method === "listPRs")
          return Promise.resolve({
            prs: structuredClone(snapshots[String(args[0])].prs),
          });
        if (method === "getMethodology")
          return Promise.resolve(
            structuredClone(snapshots[String(args[0])].methodology),
          );
        if (method === "listSignals")
          return Promise.resolve({
            signals: structuredClone(snapshots[String(args[0])].signals),
          });
        return Promise.reject(
          new Error(`Mock API operation requires an override: ${method}`),
        );
      },
    ]),
  ) as DataAPI;
  const source: DataSource = {
    api: mockAPI,
    async snapshot(id) {
      calls.push({ method: "snapshot", args: [id] });
      if (!snapshots[id]) throw new Error(`Missing mock snapshot: ${id}`);
      return structuredClone(snapshots[id]);
    },
    connect(id, ready, event, error) {
      const client = { id, ready, event, error };
      clients.add(client);
      return {
        close() {
          clients.delete(client);
        },
      };
    },
  };
  return {
    source,
    calls,
    snapshots,
    setSnapshot(id: string, snapshot: ProjectSnapshot) {
      snapshots[id] = structuredClone(snapshot);
    },
    ready(id: string) {
      [...clients].filter((c) => c.id === id).forEach((c) => c.ready());
    },
    push(id: string, event: ProjectEvent) {
      [...clients].filter((c) => c.id === id).forEach((c) => c.event(event));
    },
    disconnect(id: string) {
      [...clients].filter((c) => c.id === id).forEach((c) => c.error());
    },
    get connections() {
      return clients.size;
    },
  };
}
