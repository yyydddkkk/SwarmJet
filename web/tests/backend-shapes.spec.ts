// One check per round-4 endpoint: the payload below is written by hand to
// match the Go response struct exactly (json tags, omitempty fields left out,
// zero values as Go encodes them), not built from the shared fixtures. Each
// test fails if the page throws while rendering it.
import { expect, test, type Page } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

const json = (body: unknown) => ({
  status: 200,
  contentType: "application/json",
  body: JSON.stringify(body),
});

function collectErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  return errors;
}

const manifest = {
  defaultRole: "worker",
  roles: {
    worker: {
      prompt: "roles/worker.md",
      permissions: { publish: true, message: true, attest: false, merge: false, methodology: false },
    },
  },
  merge: { requireAttestation: true, requireNonAuthor: true, requireChecks: true, auto: false },
};

// internal/api/prs.go prStatusResponse
test("PR status: prStatusResponse renders", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  await page.route("**/api/hives/swarmjet/prs/88", route =>
    route.fulfill(
      json({
        hiveId: "swarmjet",
        prNumber: 88,
        state: "open",
        draft: true,
        merged: false,
        headSha: "0123456789abcdef0123456789abcdef01234567",
        url: "https://github.com/yyydddkkk/SwarmJet/pull/88",
        checks: [{ name: "unit", status: "in_progress", conclusion: "" }],
        attestations: [
          {
            id: "att-1",
            hiveId: "swarmjet",
            prNumber: 88,
            headSha: "0123456789abcdef0123456789abcdef01234567",
            attesterId: "forager-2b91",
            authorId: "forager-7f3a",
            isAuthor: false,
            result: "fail",
            summary: "单测失败。",
            superseded: false,
            createdAt: "2026-10-18T09:00:00Z",
          },
        ],
        ready: false,
        reasons: ["required check unit is in_progress"],
      }),
    ),
  );
  await page.goto("/#/hives/swarmjet/foragers/forager-7f3a/merge");

  await expect(page.getByTestId("merge-readiness")).toHaveText("尚不可合并");
  await expect(page.getByText("required check unit is in_progress", { exact: true })).toBeVisible();
  await expect(page.locator(".hg-merge-status")).toContainText("PR #88 · open · draft");
  await expect(page.getByRole("table", { name: "结论历史" }).locator("tbody tr")).toHaveCount(1);
  expect(errors).toEqual([]);
});

// internal/api/methodology.go methodologyResponse with builtin: true
test("methodology GET: builtin methodologyResponse renders", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  await page.route("**/api/hives/swarmjet/methodology", route =>
    route.fulfill(
      json({
        id: "",
        hiveId: "swarmjet",
        version: 0,
        active: false,
        manifest,
        digest: "",
        createdBy: "",
        createdAt: "0001-01-01T00:00:00Z",
        files: [{ path: "methodology.yaml", content: "defaultRole: worker\n" }],
        builtin: true,
      }),
    ),
  );
  await page.route("**/api/hives/swarmjet/methodology/versions", route =>
    route.fulfill(json({ hiveId: "swarmjet", activeVersion: 0, versions: [], changes: [] })),
  );
  await page.goto("/#/hives/swarmjet/methodology");

  await expect(page.getByText("尚未选择方法论，可以在对话里请 Queen 推荐", { exact: true })).toBeVisible();
  await expect(page.getByRole("list", { name: "内置方法论目录" })).toBeVisible();
  await expect(page.getByTestId("methodology-version")).toHaveCount(0);
  expect(errors).toEqual([]);
});

// internal/api/methodology.go versions map + methodology.Change
test("methodology versions: {hiveId, activeVersion, versions, changes} renders", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  const version = {
    id: "m-1",
    hiveId: "swarmjet",
    version: 1,
    active: true,
    manifest,
    digest: "ab".repeat(32),
    createdBy: "local:web-admin",
    createdAt: "2026-10-18T08:00:00Z",
  };
  await page.route("**/api/hives/swarmjet/methodology", route =>
    route.fulfill(json({ ...version, files: [] })),
  );
  await page.route("**/api/hives/swarmjet/methodology?version=1", route =>
    route.fulfill(json({ ...version, files: [] })),
  );
  await page.route("**/api/hives/swarmjet/methodology/versions", route =>
    route.fulfill(
      json({
        hiveId: "swarmjet",
        activeVersion: 1,
        versions: [version],
        changes: [
          {
            id: 7,
            hiveId: "swarmjet",
            action: "publish",
            version: 1,
            actor: "local:web-admin",
            note: "首个版本",
            createdAt: "2026-10-18T08:00:00Z",
          },
        ],
      }),
    ),
  );
  await page.goto("/#/hives/swarmjet/methodology");

  await expect(page.getByTestId("methodology-version")).toContainText("v1");
  await expect(page.locator(".hg-version-table tbody tr")).toHaveCount(1);
  await expect(page.locator(".hg-change-log > li")).toHaveCount(1);
  await expect(page.locator(".hg-change-log")).toContainText("首个版本");
  expect(errors).toEqual([]);
});

// POST /methodology/activate answers with methodologyResponse (no builtin)
test("methodology activate: the flattened response is accepted", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  let activateBody: unknown;
  await page.route("**/api/hives/swarmjet/methodology/activate", async route => {
    activateBody = route.request().postDataJSON();
    await route.fulfill(
      json({
        id: "method-swarmjet-2",
        hiveId: "swarmjet",
        version: 2,
        active: true,
        manifest,
        digest: "cd".repeat(32),
        createdBy: "local:web-admin",
        createdAt: "2026-10-10T08:20:00Z",
        files: [],
      }),
    );
  });
  await page.goto("/#/hives/swarmjet/methodology");
  const row = page.locator(".hg-version-table tbody tr").filter({ hasText: "v2" });
  await row.getByRole("button", { name: "切换到此版本" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "切换到 v2" }).click();

  await expect(page.getByRole("status").filter({ hasText: "已切换到 v2" }).first()).toBeVisible();
  expect(activateBody).toEqual({ version: 2 });
  expect(errors).toEqual([]);
});

// internal/domain/llm.go LLMUsageSummary, Hive-wide
test("usage: Hive LLMUsageSummary renders with role names joined from Agents", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  const queries: URLSearchParams[] = [];
  const totals = {
    requests: 3,
    inputTokens: 1200,
    outputTokens: 300,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    costUsd: 0.5,
    unknownPriceRequests: 1,
  };
  await page.route("**/api/usage?*", route => {
    queries.push(new URL(route.request().url()).searchParams);
    return route.fulfill(
      json({
        hiveId: "swarmjet",
        period: "today",
        totals,
        byAgent: [{ agentId: "forager-2b91", ...totals }],
        byModel: [{ model: "deepseek/deepseek-chat", ...totals }],
      }),
    );
  });
  await page.goto("/#/hives/swarmjet/usage");

  await expect(page.getByTestId("hive-usage-total")).toHaveText("$0.50");
  const rows = page.locator(".hg-usage-ranking .hg-usage-row");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("verifier");
  await expect(page.getByText("1 次调用价格未知，按 0 计", { exact: true })).toBeVisible();
  expect(queries.length).toBeGreaterThan(0);
  expect(queries[0].get("period")).toBe("today");
  expect(errors).toEqual([]);
});

// LLMUsageSummary filtered to one Agent that has no rows: Go returns empty slices
test("usage: an Agent without rows shows its own empty state", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  const queries: URLSearchParams[] = [];
  await page.route("**/api/usage?*", route => {
    queries.push(new URL(route.request().url()).searchParams);
    return route.fulfill(
      json({
        hiveId: "swarmjet",
        agentId: "forager-5c40",
        period: "today",
        totals: {
          requests: 0,
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
          costUsd: 0,
          unknownPriceRequests: 0,
        },
        byAgent: [],
        byModel: [],
      }),
    );
  });
  await page.goto("/#/hives/swarmjet/foragers/forager-5c40/usage");

  await expect(page.getByText("这只 Agent 还没有用量。", { exact: true })).toBeVisible();
  await expect(page.getByTestId("agent-usage-cost")).toHaveCount(0);
  expect(queries.length).toBeGreaterThan(0);
  const query = queries[0];
  expect(query.get("agent")).toBe("forager-5c40");
  expect(query.get("period")).toBe("today");
  expect(errors).toEqual([]);
});

// internal/api/models.go modelsResponse
test("models GET: modelsResponse renders providers and gateway state", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  await page.route("**/api/models", route =>
    route.fulfill(
      json({
        defaultModel: "deepseek/deepseek-chat",
        models: [
          {
            id: "deepseek/deepseek-chat",
            provider: "deepseek",
            displayName: "DeepSeek Chat",
            contextLength: 65536,
            maxOutputTokens: 8192,
            kind: "chat",
          },
          { id: "openai/gpt-5-mini", provider: "openai" },
        ],
        providers: [{ provider: "deepseek", type: "api_key", masked: "sk-…9f2c", configured: true }],
        gatewayAvailable: true,
      }),
    ),
  );
  await page.goto("/#/models");

  await expect(page.getByText("已有可用凭证", { exact: true })).toBeVisible();
  await expect(page.getByTestId("masked-deepseek")).toHaveText("sk-…9f2c");
  await expect(page.getByTestId("gateway-available")).toHaveText("可连接");
  await expect(page.getByRole("button", { name: "移除 DeepSeek 的凭证" })).toBeVisible();
  await expect(page.locator(".hg-model-table tbody tr")).toHaveCount(2);
  expect(errors).toEqual([]);
});

test("models GET: an unreachable gateway is shown", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  await page.route("**/api/models", route =>
    route.fulfill(
      json({ defaultModel: "deepseek/deepseek-chat", models: [], providers: [], gatewayAvailable: false }),
    ),
  );
  await page.goto("/#/models");

  await expect(page.getByTestId("gateway-available")).toHaveText("暂不可用");
  await expect(page.getByText("模型网关暂时没有响应", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

// PUT /api/models/credentials/{provider} answers with modelCredentialView
test("models PUT: modelCredentialView is shown as the masked value", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  await page.route("**/api/models/credentials/deepseek", route =>
    route.request().method() === "PUT"
      ? route.fulfill(json({ provider: "deepseek", type: "api_key", masked: "sk-…7777", configured: true }))
      : route.fallback(),
  );
  await page.goto("/#/models");
  await page.getByLabel("API key").fill("sk-test-not-real-7777");
  await page.getByRole("button", { name: "保存凭证" }).click();

  await expect(
    page.getByRole("status").filter({ hasText: "已保存 DeepSeek 凭证，脱敏值 sk-…7777。" }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

// internal/api/setup.go setupStatusResponse.models (setupModels)
test("setup status: the setupModels block renders on 接入", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page, {
    setup: {
      models: {
        available: true,
        configured: true,
        defaultModel: "deepseek/deepseek-reasoner",
        providers: ["deepseek"],
      },
    },
  });
  await page.goto("/#/setup");

  await expect(page.getByTestId("setup-models-status")).toHaveText("已有可用凭证");
  await expect(page.getByTestId("setup-default-model")).toHaveText("deepseek/deepseek-reasoner");
  expect(errors).toEqual([]);
});

test("setup status: setupModels without providers asks for a credential", async ({ page }) => {
  const errors = collectErrors(page);
  await mockHiveAPI(page);
  await page.goto("/#/setup");

  await expect(page.getByTestId("setup-models-status")).toHaveText("还没有可用凭证");
  await expect(page.getByTestId("setup-default-model")).toHaveText("deepseek/deepseek-chat");
  await expect(page.getByText("先写入一个模型凭证", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});
