import { expect, test, type Page } from "@playwright/test";
import { formatCommand, parseCommand } from "../src/commands";
import type { Project, Repository } from "../src/types";

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
  let failImport = false;
  const saves: Project[] = [];
  const imports: Record<string, unknown>[] = [];
  await page.route("**/api/**", async (route) => {
    const request = route.request(),
      pathname = new URL(request.url()).pathname;
    if (request.method() !== "GET")
      expect(request.headers()["x-requested-with"]).toBe("swarmjet");
    if (pathname === "/api/session")
      return route.fulfill({ json: { username: "admin" } });
    if (pathname === "/api/project-defaults")
      return route.fulfill({
        json: { agentImage: "swarmjet-agent:configured" },
      });
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
    failImport: () => {
      failImport = true;
    },
  };
}
async function completeExecutionAndChecks(page: Page) {
  await page
    .getByLabel("开发模型", { exact: true })
    .fill("example/development");
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
    development: { model: "example/development", adapter: "opencode" },
    review: { model: "example/development", adapter: "opencode" },
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

test("configured model catalog populates choices and links to executor setup", async ({
  page,
}) => {
  await setupAPI(page, blankImported());
  await page.goto("/#/projects/imported-draft");
  await expect(
    page.getByRole("link", { name: "登录与 provider 配置" }),
  ).toHaveAttribute("href", "#/runtime");
  await page.getByRole("button", { name: "读取模型列表" }).click();
  await expect(page.locator("#configured-models option")).toHaveCount(2);
  await expect(
    page.locator("#configured-models option").first(),
  ).toHaveAttribute("value", "example/development");
  await expect(page.getByLabel("开发模型", { exact: true })).toHaveAttribute(
    "list",
    "configured-models",
  );
  await expect(
    page.getByText("已读取 2 个模型", { exact: false }),
  ).toBeVisible();
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
