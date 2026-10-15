import { expect, test, type Page } from "@playwright/test";
import { formatCommand, parseCommand } from "../src/commands";
import type { Project, Repository } from "../src/types";
const publishedRunner = "registry.test/swarmjet/agent@sha256:" + "a".repeat(64);

const repositories: Repository[] = [
  {
    id: "core",
    projectId: 101,
    url: "https://gitlab.example/team/core.git",
    path: "libs/core",
    targetBranch: "dev",
    createFrom: "",
    dependsOn: [],
    sensitivePaths: [],
  },
  {
    id: "api",
    projectId: 102,
    url: "https://gitlab.example/team/api.git",
    path: "services/api",
    targetBranch: "dev",
    createFrom: "",
    dependsOn: ["core"],
    sensitivePaths: [],
  },
];
const discovery = {
  name: "工程工作区",
  suggestedId: "workspace",
  gitlabUrl: "https://gitlab.example",
  metaUrl: "https://gitlab.example/team/meta.git",
  metaRef: "trunk",
  repositories,
  branches: { core: ["dev", "main"], api: ["main", "stable"] },
  warnings: [
    "仓库 api 缺少目标分支 dev，请手动选择初始化引用；未自动设置 createFrom",
  ],
};
const configuredProject = (): Project => ({
  id: "custom-project",
  name: "已有项目",
  metaUrl: discovery.metaUrl,
  metaRef: "trunk",
  gitlabUrl: discovery.gitlabUrl,
  version: 3,
  enabled: false,
  credentialRef: "GITLAB_TOKEN",
  mergeCredentialRef: "GITLAB_MERGE_TOKEN",
  repositories: [
    {
      ...repositories[0],
      id: "custom-core",
      targetBranch: "release/core",
      createFrom: "stable-core",
      sensitivePaths: ["contracts/**"],
    },
    {
      ...repositories[1],
      id: "custom-api",
      targetBranch: "release/api",
      createFrom: "stable-api",
      dependsOn: ["custom-core"],
      sensitivePaths: ["security/**", "billing"],
    },
  ],
  development: {
    adapter: "opencode",
    model: "example/development",
    image: "swarmjet-agent:configured",
    timeoutSeconds: 5400,
  },
  review: {
    adapter: "opencode",
    model: "example/development",
    image: "swarmjet-agent:configured",
    timeoutSeconds: 5400,
  },
  integration: {
    composeFile: "integration/compose.yaml",
    startupTimeoutSeconds: 180,
    checks: [
      {
        name: "integration",
        service: "verifier",
        command: ["go", "test", "./..."],
        required: true,
      },
    ],
  },
  maxReworks: 3,
  requireCI: true,
});
const blankDraft = (): Project => ({
  ...configuredProject(),
  id: "draft-project",
  name: "",
  metaUrl: "",
  metaRef: "",
  version: 1,
  enabled: false,
  gitlabUrl: "",
  repositories: [],
  development: {
    adapter: "opencode",
    model: "",
    image: "swarmjet-agent:configured",
    timeoutSeconds: 5400,
  },
  review: {
    adapter: "opencode",
    model: "",
    image: "swarmjet-agent:configured",
    timeoutSeconds: 5400,
  },
  integration: { composeFile: "", checks: [], startupTimeoutSeconds: 180 },
});
const blankImported = (): Project => ({
  ...blankDraft(),
  id: "imported-draft",
  metaUrl: discovery.metaUrl,
  gitlabUrl: discovery.gitlabUrl,
});
async function setupAPI(page: Page, initial?: Project) {
  let project = initial ? structuredClone(initial) : undefined;
  const connections = [{ id: "c1", name: "team GitLab", gitlabUrl: discovery.gitlabUrl, username: "admin", version: 1, hasMergeToken: false, fingerprint: "fp", createdAt: "", updatedAt: "" }];
  let failImport = false;
  const saves: Project[] = [];
  const imports: Record<string, unknown>[] = [];
  const additions: Record<string, unknown>[] = [];
  const changes: Record<string, unknown>[] = [];
  let labelsReady = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      pathname = new URL(request.url()).pathname;
    if (request.method() !== "GET")
      expect(request.headers()["x-requested-with"]).toBe("swarmjet");
    if (pathname === "/api/session")
      return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (pathname === "/api/users")
      return route.fulfill({ json: { users: [{ id: "11111111-1111-4111-8111-111111111111", email: "owner@example.test", displayName: "Project Owner", status: "active" }] } });
    if (pathname === "/api/project-defaults")
      return route.fulfill({
        json: { agentImage: "swarmjet-agent:configured" },
      });
    if (pathname === "/api/registry/images")
      return route.fulfill({ json: { repository: "registry.test/swarmjet/agent", images: [
        { digest: "sha256:" + "a".repeat(64), reference: publishedRunner, tags: ["stable"] },
      ] } });
    if (pathname === "/api/connections" && request.method() === "GET")
      return route.fulfill({ json: connections });
    if (pathname === "/api/connections/verify" && request.method() === "POST")
      return route.fulfill({ json: { username: "admin", platformAdmin: true } });
    if (pathname === "/api/connections" && request.method() === "POST") {
      const input = request.postDataJSON();
      const connection = { ...connections[0], id: "c2", name: input.name, gitlabUrl: input.gitlabUrl };
      connections.push(connection);
      return route.fulfill({ json: connection });
    }
    if (pathname === "/api/runtime/agents") return route.fulfill({json:{agents:[{id:"opencode",name:"OpenCode"},{id:"codex",name:"Codex"},{id:"claude",name:"Claude Code"}],enabled:["opencode","codex"]}});
    if (pathname === "/api/runtime/models")
      return route.fulfill({
        json: {
          models: ["example/development", "example/review"],
          providers: ["example"],
          warnings: [],
        },
      });
    if (pathname === "/api/projects/discover") {
      imports.push(request.postDataJSON());
      return failImport
        ? route.fulfill({
            status: 403,
            json: { error: "没有读取权限，请确认仓库访问权限" },
          })
        : route.fulfill({ json: discovery });
    }
    if (pathname === "/api/projects" && request.method() === "GET")
      return route.fulfill({ json: project ? [project] : [] });
    if (pathname === `/api/projects/${project?.id}/repository-changes`) {
      if (request.method() === "GET" && new URL(request.url()).searchParams.get("available") === "true")
        return route.fulfill({ json: { projects: [{ id: 103, name: "worker", pathWithNamespace: "team/worker", defaultBranch: "main", httpUrlToRepo: "https://gitlab.example/team/worker.git", archived: false }], hasNext: false } });
      if (request.method() === "GET") return route.fulfill({ json: changes });
      const input = request.postDataJSON();
      additions.push(input);
      const change = { id: "change-1", projectId: project?.id, repository: { id: input.alias, path: input.path }, status: "waiting", mergeRequestUrl: "https://gitlab.example/team/meta/-/merge_requests/8" };
      changes.push(change);
      return route.fulfill({ status: 202, json: change });
    }
    if (pathname === `/api/projects/${project?.id}/labels`) {
      if (request.method() === "POST") labelsReady = true;
      return route.fulfill({ json: { repositories: (project?.repositories || []).map((repo) => ({
        repositoryId: repo.id, projectId: repo.projectId,
        labels: [{ name: "autonomous", color: "#2563EB", exists: true, existingColor: "#123456" },
          { name: "agent:codex", color: "#7C3AED", exists: labelsReady, existingColor: labelsReady ? "#7C3AED" : undefined }],
      })) } });
    }
    if (
      (pathname === "/api/projects" && request.method() === "POST") ||
      (pathname.startsWith("/api/projects/") && request.method() === "PUT")
    ) {
      const payload = request.postDataJSON() as Project;
      saves.push(structuredClone(payload));
      project = { ...payload, version: (project?.version || 0) + 1 };
      return route.fulfill({ json: project });
    }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  return {
    saves,
    imports,
    additions,
    failImport: () => {
      failImport = true;
    },
  };
}

test("existing project can request a new repository without editing Meta YAML", async ({ page }) => {
  const state = await setupAPI(page, configuredProject());
  await page.goto("/#/projects/custom-project");
  await page.getByText("＋ 选择要加入的仓库").click();
  await page.getByRole("button", { name: "team/worker" }).click();
  await expect(page.getByLabel("仓库标识")).toHaveValue("worker");
  await page.getByRole("button", { name: "提交新增请求" }).click();
  await expect.poll(() => state.additions.length).toBe(1);
  expect(state.additions[0]).toMatchObject({ gitlabProjectId: 103, alias: "worker", path: "worker", targetBranch: "main" });
  await expect(page.getByRole("link", { name: "查看 MR" })).toHaveAttribute("href", "https://gitlab.example/team/meta/-/merge_requests/8");
});

test("enabled project can inspect and repair GitLab selection labels", async ({ page }) => {
  await setupAPI(page, { ...configuredProject(), enabled: true });
  await page.goto("/#/projects/custom-project");
  const labels = page.getByRole("region", { name: "GitLab 标签管理" });
  await expect(labels.getByText("agent:codex · 待创建")).toHaveCount(2);
  await labels.getByRole("button", { name: "检查并补齐" }).click();
  await expect(labels.getByText("标签已就绪")).toHaveCount(2);
  await expect(labels.getByText("agent:codex · 待创建")).toHaveCount(0);
});

test("new project can import an existing Meta-Repo with a saved GitLab connection", async ({ page }) => {
  const state = await setupAPI(page);
  await page.goto("/#/projects/new");
  await page.getByRole("link", { name: "导入已有 Meta-Repo" }).click();
  await expect(page).toHaveURL(/#\/projects\/new\/import$/);
  await page.getByLabel("GitLab 连接", { exact: true }).selectOption("c1");
  await page.getByLabel("Meta-Repo 地址", { exact: true }).fill(discovery.metaUrl);
  await page.getByRole("button", { name: "读取仓库清单" }).click();
  await expect(page.getByLabel("项目名称", { exact: true })).toHaveValue(discovery.name);
  await page.getByLabel("初始负责人（可选）", { exact: true }).selectOption("11111111-1111-4111-8111-111111111111");
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.imports[0]).toMatchObject({ connectionId: "c1", metaUrl: discovery.metaUrl });
  expect(state.saves[0]).toMatchObject({ metaMode: "imported", connectionId: "c1", mergeCredentialRef: "", enabled: false, metaRef: "trunk" });
  await expect(page).toHaveURL(/#\/projects\/workspace$/);
});

test("Meta-Repo import renders when randomUUID is unavailable on internal HTTP", async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(Crypto.prototype, "randomUUID", { configurable: true, value: undefined });
  });
  await setupAPI(page);
  await page.goto("/#/projects/new");
  await page.getByRole("link", { name: "导入已有 Meta-Repo" }).click();
  await expect(page.getByLabel("Meta-Repo 地址", { exact: true })).toBeVisible();
  await expect(page.getByLabel("项目标识", { exact: true })).toHaveValue(/^project-[a-f0-9]{32}$/);
});

test("new import defaults to the current administrator and uses shared notifications", async ({ page }) => {
  const state = await setupAPI(page);
  await page.goto("/#/projects/new/import");
  const owner = page.getByLabel("初始负责人（可选）", { exact: true });
  await expect(owner).toHaveValue("");
  await expect(owner).not.toHaveAttribute("required");

  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect(page.locator('[aria-label="通知消息"] [role="alert"]')).toContainText("请填写项目名称和 Meta-Repo 地址");

  await page.getByLabel("项目名称", { exact: true }).fill("测试工作区");
  await page.getByLabel("Meta-Repo 地址", { exact: true }).fill(discovery.metaUrl);
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0]).toMatchObject({ initialOwnerUserId: "" });
});

test("new import can verify and save a GitLab connection before reading the manifest", async ({ page }) => {
  const state = await setupAPI(page);
  await page.goto("/#/projects/new/import");
  await page.getByLabel("Meta-Repo 地址", { exact: true }).fill(discovery.metaUrl);
  await page.getByRole("button", { name: "新增 GitLab 连接" }).click();
  await expect(page.getByLabel("新连接的 GitLab 地址")).toHaveValue(discovery.gitlabUrl);
  await page.getByLabel("Access Token", { exact: true }).fill("test-token");
  await page.getByRole("button", { name: "验证并保存连接" }).click();
  await expect(page.getByLabel("GitLab 连接", { exact: true })).toHaveValue("c2");
  await page.getByRole("button", { name: "读取仓库清单" }).click();
  await expect.poll(() => state.imports.length).toBe(1);
  expect(state.imports[0]).toMatchObject({ connectionId: "c2", metaUrl: discovery.metaUrl });
});
async function completeExecutionAndChecks(page: Page) {
  await page.getByLabel("项目默认 Agent",{exact:true}).selectOption("opencode");
  await page
    .getByLabel("联调配置文件", { exact: true })
    .fill("integration/compose.yaml");
  await page.getByRole("button", { name: "添加验收检查" }).click();
  await page.getByLabel("检查 1 名称", { exact: true }).fill("integration");
  await page.getByLabel("检查 1 服务", { exact: true }).fill("verifier");
  await page
    .getByLabel("检查 1 命令", { exact: true })
    .fill("python verify.py --label 'API integration' --empty ''");
}

test("draft only needs name and Meta URL and never enables incomplete execution", async ({
  page,
}) => {
  const state = await setupAPI(page, blankDraft());
  await page.goto("/#/projects/draft-project");
  await page.getByLabel("项目名称", { exact: true }).fill("先记录工作区");
  await page
    .getByLabel("Meta-Repo 地址", { exact: true })
    .fill(discovery.metaUrl);
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeDisabled();
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0]).toMatchObject({
    name: "先记录工作区",
    metaUrl: discovery.metaUrl,
    enabled: false,
    repositories: [],
    development: { model: "", image: "swarmjet-agent:configured" },
    review: { model: "" },
    integration: { checks: [] },
  });
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeDisabled();
  expect(state.imports).toHaveLength(0);
});

test("import fills identity, paths and dependencies but missing dev needs explicit creation basis", async ({
  page,
}) => {
  const state = await setupAPI(page, blankImported());
  await page.goto("/#/projects/imported-draft");
  await page
    .getByLabel("Meta-Repo 地址", { exact: true })
    .fill(discovery.metaUrl);
  await page.getByRole("button", { name: "读取仓库清单", exact: true }).click();
  await expect(page.getByLabel("项目名称", { exact: true })).toHaveValue(
    discovery.name,
  );
  await expect(page.getByLabel("Meta 分支", { exact: true })).toHaveValue(
    "trunk",
  );
  await expect(page.getByLabel("api 的目标分支", { exact: true })).toHaveValue(
    "dev",
  );
  await expect(page.getByLabel("api 的创建起点", { exact: true })).toHaveValue(
    "",
  );
  await expect(page.getByText("services/api", { exact: true })).toBeVisible();
  await expect(page.getByText("依赖 core", { exact: true })).toBeVisible();
  await expect(
    page.getByText(discovery.warnings[0], { exact: true }),
  ).toBeVisible();
  await completeExecutionAndChecks(page);
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeDisabled();
  await page.getByLabel("api 的创建起点", { exact: true }).fill("main");
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeEnabled();
  await page.getByRole("button", { name: "保存并启用" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.imports[0]).toMatchObject({
    metaUrl: discovery.metaUrl,
    metaRef: "",
    defaultTargetBranch: "dev",
    credentialRef: "GITLAB_TOKEN",
  });
  expect(state.saves[0]).toMatchObject({
    id: "imported-draft",
    enabled: true,
    metaRef: "trunk",
    development: { model: "", adapter: "opencode" },
    review: { model: "", adapter: "opencode" },
    integration: {
      checks: [
        {
          name: "integration",
          service: "verifier",
          required: true,
          command: [
            "python",
            "verify.py",
            "--label",
            "API integration",
            "--empty",
            "",
          ],
        },
      ],
    },
  });
  expect(
    state.saves[0].repositories?.find((repo) => repo.id === "api"),
  ).toMatchObject({
    projectId: 102,
    path: "services/api",
    targetBranch: "dev",
    createFrom: "main",
    dependsOn: ["core"],
  });
});

test("reimport preserves configured branches, bases and sensitive paths and remaps custom dependency IDs", async ({
  page,
}) => {
  const initial = configuredProject();
  const state = await setupAPI(page, initial);
  await page.goto("/#/projects/custom-project");
  await page.getByRole("button", { name: "重新读取清单" }).click();
  await expect(
    page.getByLabel("custom-api 的目标分支", { exact: true }),
  ).toHaveValue("release/api");
  await expect(
    page.getByLabel("custom-api 的创建起点", { exact: true }),
  ).toHaveValue("stable-api");
  await expect(
    page.getByText("依赖 custom-core", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0].id).toBe("custom-project");
  expect(state.saves[0].repositories).toEqual(initial.repositories);
});

for (const source of ["Meta-Repo 地址", "Meta 分支"] as const) {
  test(`editing ${source} invalidates old import, failed refresh stays blocked and draft clears stale repositories`, async ({
    page,
  }) => {
    const state = await setupAPI(page, configuredProject());
    await page.goto("/#/projects/custom-project");
    await expect(
      page.getByRole("button", { name: "保存并启用" }),
    ).toBeEnabled();
    state.failImport();
    const value =
      source === "Meta-Repo 地址"
        ? "https://gitlab.example/team/other-meta.git"
        : "new-meta-ref";
    await page.getByLabel(source, { exact: true }).fill(value);
    await expect(page.getByText("来源已更改", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: "重新读取清单" }).click();
    await expect(page.getByRole("alert")).toContainText("没有读取权限");
    await expect(page.getByLabel(source, { exact: true })).toHaveValue(value);
    await expect(page.getByLabel("项目名称", { exact: true })).toHaveValue(
      "已有项目",
    );
    await expect(
      page.getByLabel("custom-api 的目标分支", { exact: true }),
    ).toHaveValue("release/api");
    await expect(
      page.getByRole("button", { name: "保存并启用" }),
    ).toBeDisabled();
    await page.getByRole("button", { name: "保存草稿" }).click();
    await expect.poll(() => state.saves.length).toBe(1);
    expect(state.saves[0].enabled).toBe(false);
    expect(state.saves[0].repositories).toEqual([]);
    expect(
      source === "Meta-Repo 地址"
        ? state.saves[0].metaUrl
        : state.saves[0].metaRef,
    ).toBe(value);
  });
}

test("project default only lists enabled agents and does not select models", async ({page}) => {
  await setupAPI(page,blankImported());
  await page.goto("/#/projects/imported-draft");
  await expect(page.getByRole("link",{name:"启用 Agent 与终端配置"})).toHaveAttribute("href","#/runtime");
  const selector=page.getByLabel("项目默认 Agent",{exact:true});
  await selector.selectOption("codex");
  await expect(selector).toHaveValue("codex");
  await expect(selector.locator('option[value="claude"]')).toHaveCount(0);
  await expect(page.getByLabel("开发模型",{exact:true})).toHaveCount(0);
});

test("project Runner image selector supports Node default and published versions for both roles", async ({page}) => {
  const state = await setupAPI(page, configuredProject());
  await page.goto("/#/projects/custom-project");
  await page.getByText("执行环境设置", {exact:false}).click();
  const development = page.getByLabel("开发 Runner 镜像", {exact:true});
  await expect(development).toHaveValue("swarmjet-agent:configured");
  await expect(development.locator(`option[value="${publishedRunner}"]`)).toHaveCount(1);
  await development.selectOption("");
  await expect(page.getByRole("button", {name:"保存并启用"})).toBeEnabled();
  await page.getByRole("checkbox", {name:"审查沿用开发执行环境"}).uncheck();
  const review = page.getByLabel("审查 Runner 镜像", {exact:true});
  await review.selectOption("__manual__");
  await page.getByLabel("审查镜像摘要", {exact:true}).fill(publishedRunner);
  await review.selectOption(publishedRunner);
  await page.getByRole("button", {name:"保存草稿"}).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0].nodes?.mode).toBe("auto");
  expect(state.saves[0].development.image).toBe("");
  expect(state.saves[0].review.image).toBe(publishedRunner);
});

test("legacy local image cannot be enabled for Node scheduling until replaced", async ({page}) => {
  const state = await setupAPI(page, {...configuredProject(), nodes:{mode:"auto"}});
  await page.goto("/#/projects/custom-project");
  await expect(page.getByRole("button", {name:"保存并启用"})).toBeDisabled();
  await page.getByText("执行环境设置", {exact:false}).click();
  await page.getByLabel("开发 Runner 镜像", {exact:true}).selectOption("");
  await expect(page.getByRole("button", {name:"保存并启用"})).toBeEnabled();
  await page.getByRole("button", {name:"保存草稿"}).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0].development.image).toBe("");
  expect(state.saves[0].review.image).toBe("");
});

test("argv command formatting preserves spaces, quotes, empty arguments and multiline content", () => {
  const examples = [
    ["go", "test", "./..."],
    [
      "python",
      "verify.py",
      "API integration",
      "",
      "it's quoted",
      'a "double" quote',
    ],
    [
      "printf",
      "first\nsecond",
      "tab\tvalue",
      "literal|operator",
      "back\\slash",
      "中文参数",
    ],
    ["tool", "${HOME}", "$(not-a-shell)", "--flag=a b", ""],
  ];
  for (const argv of examples)
    expect(parseCommand(formatCommand(argv))).toEqual(argv);
  for (const raw of [
    "npm test | cat",
    "a && b",
    "a; b",
    "cat < input",
    "echo x > output",
    "echo `whoami`",
  ])
    expect(() => parseCommand(raw)).toThrow();
  for (const raw of [
    "echo 'unterminated",
    'echo "unterminated',
    "echo trailing\\",
  ])
    expect(() => parseCommand(raw)).toThrow();
});

for (const viewport of [
  { width: 1440, height: 1100 },
  { width: 390, height: 844 },
]) {
  test(`project setup remains within viewport ${viewport.width} and captures review screenshot`, async ({
    page,
  }, testInfo) => {
    await setupAPI(page, blankImported());
    await page.setViewportSize(viewport);
    await page.goto("/#/projects/imported-draft");
    await page
      .getByLabel("Meta-Repo 地址", { exact: true })
      .fill(discovery.metaUrl);
    await page
      .getByRole("button", { name: "读取仓库清单", exact: true })
      .click();
    await expect(
      page.getByLabel("api 的创建起点", { exact: true }),
    ).toHaveValue("");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    const screenshot = testInfo.outputPath(
      `project-setup-${viewport.width}.png`,
    );
    await page.screenshot({ path: screenshot, fullPage: true });
    await testInfo.attach(`project-setup-${viewport.width}`, {
      path: screenshot,
      contentType: "image/png",
    });
  });
}

test("extra acceptance defaults off and repository or CI checks can be enabled", async ({ page }) => {
  const project = configuredProject();
  project.integration = { composeFile: "", checks: [], startupTimeoutSeconds: 180 };
  const state = await setupAPI(page, project);
  await page.goto("/#/projects/custom-project");
  await page.getByLabel("验收策略", { exact: true }).selectOption("none");
  await expect(page.getByLabel("联调配置文件", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeEnabled();
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0]).toMatchObject({ acceptanceStrategy: "none", requireCI: false });
  await page.getByLabel("验收策略", { exact: true }).selectOption("repository");
  await expect(page.getByLabel("联调配置文件", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeEnabled();
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(2);
  expect(state.saves[1]).toMatchObject({ acceptanceStrategy: "repository", requireCI: false });
  await page.getByLabel("验收策略", { exact: true }).selectOption("gitlab_ci");
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(3);
  expect(state.saves[2]).toMatchObject({ acceptanceStrategy: "gitlab_ci", requireCI: true });
});

test("independent review can be disabled without a review Agent", async ({ page }) => {
  const project = configuredProject();
  project.reviewMode = "none";
  project.review = { adapter: "", model: "", image: "", timeoutSeconds: 0 };
  const state = await setupAPI(page, project);
  await page.goto("/#/projects/custom-project");
  await expect(page.getByLabel("独立审查", { exact: true })).toHaveValue("none");
  await expect(page.getByLabel("审查 Runner 镜像", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeEnabled();
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0]).toMatchObject({ reviewMode: "none" });
});

test("custom command acceptance preserves argv without requiring Compose", async ({ page }) => {
  const project = configuredProject();
  project.integration = { composeFile: "", checks: [], startupTimeoutSeconds: 180 };
  const state = await setupAPI(page, project);
  await page.goto("/#/projects/custom-project");
  await page.getByLabel("自定义验收方式", { exact: true }).selectOption("commands");
  await page.getByLabel("验收镜像", { exact: true }).fill("toolchain:local");
  await page.getByRole("button", { name: "添加验收命令" }).click();
  const fieldset = page.locator("fieldset.setup-check").first();
  await fieldset.getByLabel("名称", { exact: true }).fill("unit");
  await fieldset.getByLabel("工作目录", { exact: true }).fill("libs/core");
  await fieldset.getByLabel("执行命令", { exact: true }).fill("node -e 'console.log(123)'");
  await expect(page.getByRole("button", { name: "保存并启用" })).toBeEnabled();
  await page.getByRole("button", { name: "保存草稿" }).click();
  await expect.poll(() => state.saves.length).toBe(1);
  expect(state.saves[0].integration.commands?.[0]).toMatchObject({ path: "libs/core", argv: ["node", "-e", "console.log(123)"] });
});

test("GitHub editor uses installation connection and GitHub label management", async ({ page }) => {
  await setupAPI(page, {...configuredProject(),provider:"github",metaMode:"managed",connectionId:"gh1",gitlabUrl:"",enabled:true,acceptanceStrategy:"platform_ci",mergeMethod:"squash"});
  await page.goto("/#/projects/custom-project");
  const labels = page.getByRole("region",{name:"GitHub 标签管理"});
  await expect(labels.getByRole("heading",{name:"GitHub 标签",exact:true})).toBeVisible();
  await labels.getByRole("button",{name:"检查并补齐"}).click();
  await expect(labels.getByText("标签已就绪")).toHaveCount(2);
  await page.getByText("连接与凭据设置",{exact:false}).click();
  await expect(page.getByLabel("GitHub 连接",{exact:true})).toHaveValue("gh1");
  await expect(page.getByLabel("GitLab 地址",{exact:true})).toHaveCount(0);
  await expect(page.getByLabel("验收策略",{exact:true})).toHaveValue("platform_ci");
  await expect(page.getByLabel("GitHub 合并方式")).toHaveValue("squash");
});
