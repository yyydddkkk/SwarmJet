// REST client for the Hive console. All writes go through the session cookie
// and the same-origin CSRF header the rest of the console uses; the server
// fixes the sender address to local:web-<signed-in user>.
import type {
  Agent,
  CreatedToken,
  GithubRepository,
  Hive,
  HiveOverview,
  Message,
  MessageKind,
  MethodologyResponse,
  MethodologyTemplate,
  QueenStatus,
  MethodologyVersionsResponse,
  ModelCredentialResponse,
  ModelsResponse,
  Principal,
  PRStatusResponse,
  SetupStatus,
  TokenScope,
  TokenSummary,
  UsagePeriod,
  UsageResponse,
} from "./types";

export type HiveListResponse = {
  hives: HiveOverview[];
  localAddress: string;
  truncated?: boolean;
};

export type HiveDetailResponse = {
  hive: Hive;
  localAddress: string;
};

export type ForagerListResponse = {
  queen?: Agent;
  foragers: Agent[];
  localAddress: string;
  truncated?: boolean;
};

export type SpawnForagerResponse = {
  forager: Agent;
  message: Message;
  localAddress?: string;
};

export type ForagerDetailResponse = {
  forager: Agent;
  hive: Hive;
  localAddress: string;
};

export type MessagePage = {
  messages: Message[];
  cursor?: string;
  truncated?: boolean;
  to: string;
};

export type MessageResponse = {
  message: Message;
  localAddress?: string;
};

export type CreateHiveInput = {
  name: string;
  repository: string;
  queenEnabled?: boolean;
  config?: Partial<Hive["config"]>;
};

export type GithubManifestResponse = {
  action: string;
  manifest: Record<string, unknown>;
};

export class HiveAPIError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
    this.name = "HiveAPIError";
  }
}

async function request<T>(
  path: string,
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" = "GET",
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
    } catch {
      // The body was not JSON; keep the status-based message.
    }
    // A failed login carries its own message; only other 401s mean the session
    // expired. /session is expected to 401 while signed out.
    if (response.status === 401 && path !== "/login" && path !== "/session") {
      message = "会话已过期或未登录，请重新登录。";
    }
    if (response.status === 403) {
      message = "需要管理员会话才能继续。";
    }
    throw new HiveAPIError(message, response.status);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

const segment = (value: string) => encodeURIComponent(value);

export const hiveAPI = {
  /* ---- session ---- */

  getSession(signal?: AbortSignal) {
    return request<Principal>("/session", "GET", undefined, signal);
  },

  login(username: string, password: string, signal?: AbortSignal) {
    return request<void>("/login", "POST", { username, password }, signal);
  },

  logout(signal?: AbortSignal) {
    return request<void>("/logout", "POST", undefined, signal);
  },

  /* ---- setup / onboarding ---- */

  getSetupStatus(signal?: AbortSignal) {
    return request<SetupStatus>("/setup/status", "GET", undefined, signal);
  },

  createGithubManifest(owner: string, signal?: AbortSignal) {
    return request<GithubManifestResponse>("/setup/github/manifest", "POST", { owner }, signal);
  },

  listGithubRepositories(signal?: AbortSignal) {
    return request<{ repositories: GithubRepository[] }>(
      "/setup/github/repositories",
      "GET",
      undefined,
      signal,
    );
  },

  /* ---- tokens / local agent connections ---- */

  listTokens(signal?: AbortSignal) {
    return request<{ tokens: TokenSummary[] }>("/tokens", "GET", undefined, signal);
  },

  createToken(input: { name: string; scope: TokenScope }, signal?: AbortSignal) {
    return request<CreatedToken>("/tokens", "POST", input, signal);
  },

  revokeToken(id: string, signal?: AbortSignal) {
    return request<void>(`/tokens/${segment(id)}`, "DELETE", undefined, signal);
  },

  /* ---- models and credentials (round 4, contract section 7) ---- */

  listModels(signal?: AbortSignal) {
    return request<ModelsResponse>("/models", "GET", undefined, signal);
  },

  /**
   * Writes a provider key. The key travels only in the request body: never in
   * the URL, localStorage or a log line. The server answers with the masked
   * value, which is all the console keeps.
   */
  setModelCredential(provider: string, apiKey: string, signal?: AbortSignal) {
    return request<ModelCredentialResponse>(
      `/models/credentials/${segment(provider)}`,
      "PUT",
      { apiKey },
      signal,
    );
  },

  deleteModelCredential(provider: string, signal?: AbortSignal) {
    return request<void>(`/models/credentials/${segment(provider)}`, "DELETE", undefined, signal);
  },

  /* ---- hives ---- */

  listHives(signal?: AbortSignal) {
    return request<HiveListResponse>("/hives", "GET", undefined, signal);
  },

  getHive(hiveId: string, signal?: AbortSignal) {
    return request<HiveDetailResponse>(`/hives/${segment(hiveId)}`, "GET", undefined, signal);
  },

  getQueen(hiveId: string, signal?: AbortSignal) {
    return request<QueenStatus>(`/hives/${segment(hiveId)}/queen`, "GET", undefined, signal);
  },

  updateHive(hiveId: string, input: { name?: string; queenEnabled?: boolean; config?: Partial<Hive["config"]> }, signal?: AbortSignal) {
    return request<HiveDetailResponse>(`/hives/${segment(hiveId)}`, "PATCH", input, signal);
  },

  createHive(input: CreateHiveInput, signal?: AbortSignal) {
    return request<HiveDetailResponse>("/hives", "POST", input, signal);
  },

  /* ---- methodology (round 4, contract sections 2, 3 and 7) ---- */

  listMethodologyTemplates(signal?: AbortSignal) {
    return request<MethodologyTemplate[]>("/methodology-templates", "GET", undefined, signal);
  },

  installMethodology(hiveId: string, name: string, note?: string, signal?: AbortSignal) {
    return request<MethodologyResponse>(`/hives/${segment(hiveId)}/methodology/install`, "POST", { name, ...(note ? { note } : {}) }, signal);
  },

  getMethodology(hiveId: string, version?: number, signal?: AbortSignal) {
    const query = version ? `?version=${encodeURIComponent(String(version))}` : "";
    return request<MethodologyResponse>(
      `/hives/${segment(hiveId)}/methodology${query}`,
      "GET",
      undefined,
      signal,
    );
  },

  listMethodologyVersions(hiveId: string, signal?: AbortSignal) {
    return request<MethodologyVersionsResponse>(
      `/hives/${segment(hiveId)}/methodology/versions`,
      "GET",
      undefined,
      signal,
    );
  },

  activateMethodology(hiveId: string, version: number, signal?: AbortSignal) {
    return request<MethodologyResponse>(
      `/hives/${segment(hiveId)}/methodology/activate`,
      "POST",
      { version },
      signal,
    );
  },

  listForagers(hiveId: string, signal?: AbortSignal) {
    return request<ForagerListResponse>(
      `/hives/${segment(hiveId)}/foragers`,
      "GET",
      undefined,
      signal,
    );
  },

  spawnForager(
    hiveId: string,
    input: {
      task: string;
      preset?: string;
      cli?: string;
      branch?: string;
      issueNumber?: number;
      chainId?: string;
    },
    signal?: AbortSignal,
  ) {
    return request<SpawnForagerResponse>(
      `/hives/${segment(hiveId)}/foragers`,
      "POST",
      input,
      signal,
    );
  },

  getForager(foragerId: string, signal?: AbortSignal) {
    return request<ForagerDetailResponse>(
      `/foragers/${segment(foragerId)}`,
      "GET",
      undefined,
      signal,
    );
  },

  /* ---- PR attestations, merge readiness and usage (round 4) ---- */

  getPRStatus(hiveId: string, prNumber: number, signal?: AbortSignal) {
    return request<PRStatusResponse>(
      `/hives/${segment(hiveId)}/prs/${encodeURIComponent(String(prNumber))}`,
      "GET",
      undefined,
      signal,
    );
  },

  getUsage(
    query: { hive?: string; agent?: string; period?: UsagePeriod },
    signal?: AbortSignal,
  ) {
    const params = new URLSearchParams();
    if (query.hive) params.set("hive", query.hive);
    if (query.agent) params.set("agent", query.agent);
    if (query.period) params.set("period", query.period);
    const suffix = params.toString() ? `?${params.toString()}` : "";
    return request<UsageResponse>(`/usage${suffix}`, "GET", undefined, signal);
  },

  cancelForager(foragerId: string, signal?: AbortSignal) {
    return request<MessageResponse>(
      `/foragers/${segment(foragerId)}/cancel`,
      "POST",
      undefined,
      signal,
    );
  },

  listMessages(
    hiveId: string,
    query: { to: string; after?: string; limit?: number },
    signal?: AbortSignal,
  ) {
    const params = new URLSearchParams({ to: query.to });
    if (query.after) params.set("after", query.after);
    if (query.limit) params.set("limit", String(query.limit));
    return request<MessagePage>(
      `/hives/${segment(hiveId)}/messages?${params.toString()}`,
      "GET",
      undefined,
      signal,
    );
  },

  sendMessage(
    hiveId: string,
    input: { to: string; body: string; kind?: MessageKind; chainId?: string },
    signal?: AbortSignal,
  ) {
    return request<MessageResponse>(
      `/hives/${segment(hiveId)}/messages`,
      "POST",
      input,
      signal,
    );
  },
};
