export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method,
    credentials: "same-origin",
    signal,
    headers: {
      ...(method !== "GET" ? { "X-Requested-With": "swarmjet" } : {}),
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    let message = `请求失败 (${response.status})`;
    try {
      message = (await response.json()).error || message;
    } catch {}
    if (response.status === 401 && path !== "/login" && path !== "/session")
      window.dispatchEvent(new Event("session-expired"));
    throw new APIError(message, response.status);
  }
  if (response.status === 204) return undefined as T;
  return response.json();
}
export async function getLog(taskID: string, executionID: string, signal?: AbortSignal, artifactName?: string) {
  const artifactQuery = artifactName
    ? `path=${encodeURIComponent(artifactName)}&kind=log`
    : `path=${encodeURIComponent(`executions/${executionID}/output.jsonl`)}`;
  const response = await fetch(
    `/api/tasks/${encodeURIComponent(taskID)}/artifacts?${artifactQuery}`,
    { credentials: "same-origin", signal },
  );
  if (!response.ok) {
    let message = `日志读取失败 (${response.status})`;
    try {
      message = (await response.json()).error || message;
    } catch {}
    if (response.status === 401)
      window.dispatchEvent(new Event("session-expired"));
    throw new APIError(message, response.status);
  }
  return response.text();
}
