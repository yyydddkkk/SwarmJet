import { expect, test, type Locator } from "@playwright/test";

// Desktop only (ADR section 18). Screenshots land in test-results/ at 1440px.
test.use({ viewport: { width: 1440, height: 900 } });

test("Hive list renders cards, filters, and falls back to an empty state", async ({ page }, testInfo) => {
  await page.goto("/#/hives");

  await expect(page.getByRole("heading", { name: "Hive（蜂巢）" })).toBeVisible();
  await expect(page.getByRole("link", { name: /SwarmJet 平台/ })).toBeVisible();
  await expect(page.getByText("yyydddkkk/orders-api", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: /支付网关/ })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("hive-list-1440.png"), fullPage: true });

  await page.getByLabel("搜索 Hive").fill("不存在的仓库");
  await expect(page.getByText("没有匹配的 Hive。", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: /SwarmJet 平台/ })).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("hive-list-empty-1440.png"), fullPage: true });

  await page.getByLabel("搜索 Hive").fill("");
  await expect(page.getByRole("link", { name: /SwarmJet 平台/ })).toBeVisible();
});

test("Hive conversation with Queen enabled shows prose, action cards, events and the Swarm rail", async ({ page }, testInfo) => {
  await page.goto("/#/hives/swarmjet");

  await expect(page.getByRole("heading", { name: "SwarmJet 平台", level: 1 })).toBeVisible();
  await expect(page.getByText("Queen（蜂王）已开启", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "对话", level: 2 })).toBeVisible();

  // Queen speaks as prose; actions and external events arrive as cards.
  await expect(
    page.getByText("收到。我先把 #142 交给一只使用 implementation 预设的 Forager", { exact: false }),
  ).toBeVisible();
  await expect(page.getByText("派出 Forager", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("外部内容", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("heading", { name: "Swarm（蜂群）", level: 2 })).toBeVisible();
  await expect(page.getByRole("link", { name: /forager-7f3a/ })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("hive-conversation-1440.png"), fullPage: true });

  // The composer writes to the in-memory demo stream; the bee loader shows while Queen reads.
  await page.getByLabel("对 Queen（蜂王）说话").fill("再派一只 Forager 审一下 #88");
  await page.getByRole("button", { name: "发送" }).click();
  await expect(page.locator(".hg-loader")).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("hive-queen-thinking-1440.png"), fullPage: true });
  await expect(page.locator(".hg-form-status")).toContainText("已写入本地演示会话");
  await expect(page.locator(".hg-msg-queen .hg-msg-body").last()).toContainText("现在是本地假数据演示");
});

test("Hive with Queen disabled offers the direct dispatch form", async ({ page }, testInfo) => {
  await page.goto("/#/hives/orders");

  await expect(page.getByText("Queen（蜂王）未开启", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "直接派 Forager", level: 2 })).toBeVisible();

  await page.getByRole("button", { name: "派出 Forager" }).click();
  await expect(page.getByRole("alert")).toContainText("派活说明不能为空");
  await page.screenshot({ path: testInfo.outputPath("hive-dispatch-1440.png"), fullPage: true });

  await page.getByLabel("派活说明").fill("把订单导出改成流式响应，避免大报表超时。");
  await page.getByRole("button", { name: "派出 Forager" }).click();
  await expect(page.locator(".hg-form-status")).toContainText("已在本地演示中创建 Forager");
});

test("Forager detail implements the conversation and handoff sections", async ({ page }, testInfo) => {
  await page.goto("/#/hives/swarmjet/foragers/forager-7f3a");

  await expect(page.getByRole("heading", { name: /forager-7f3a/ })).toBeVisible();
  await expect(page.getByText("CI 2/3", { exact: true })).toBeVisible();
  await expect(page.getByText("Guard（守卫蜂） 3/4", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "对话", level: 2 })).toBeVisible();
  await expect(page.locator(".hg-msg-queen .hg-msg-body").first()).toContainText(
    "处理 #142：在 Swarm 面板增加按状态筛选。",
  );

  await page.screenshot({ path: testInfo.outputPath("forager-conversation-1440.png"), fullPage: true });

  await page.getByRole("link", { name: "交接摘要" }).click();
  await expect(page).toHaveURL(/#\/hives\/swarmjet\/foragers\/forager-7f3a\/handoff$/);
  await expect(page.getByRole("heading", { name: "交接摘要", level: 2 })).toBeVisible();
  await expect(page.getByText("在 Swarm 面板增加按状态筛选 Forager 的入口", { exact: true })).toBeVisible();
  await expect(page.getByText("前端筛选交互与空结果提示", { exact: true })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("forager-handoff-1440.png"), fullPage: true });
});

// Pixel-icon reference page: every icon at 16/32/48, both bee grids, the Queen's
// crown, twelve hashed Forager avatars and the loader — light and dark surface.
test("Pixel reference lists the icon set, both bee grids and twelve Forager avatars", async ({ page }, testInfo) => {
  await page.goto("/#/icons");

  await expect(page.getByRole("heading", { name: "像素规范", level: 1 })).toBeVisible();
  const surfaces = page.locator(".hg-icon-lab-panel");
  await expect(surfaces).toHaveCount(4);

  for (const surface of [surfaces.nth(0), surfaces.nth(1)]) {
    // 29 icons, each drawn at all three whole-pixel steps.
    await expect(surface.locator('[aria-label="图标集"] .hg-px-cell')).toHaveCount(29);
    await expect(surface.locator('[aria-label="图标集"] .hg-icon')).toHaveCount(29 * 3);
    await expect(surface.getByText("hive", { exact: true })).toBeVisible();
    await expect(surface.getByText("swarm", { exact: true })).toBeVisible();
    await expect(surface.getByText("refresh", { exact: true })).toBeVisible();
  }

  const light = surfaces.nth(0);
  await expect(light.locator(".hg-px-cell-label").filter({ hasText: new RegExp(`^bee-16$`) })).toBeVisible();
  await expect(light.locator(".hg-px-cell-label").filter({ hasText: new RegExp(`^queen-16$`) })).toBeVisible();
  await expect(light.locator(".hg-px-cell-label").filter({ hasText: new RegExp(`^bee-32$`) })).toBeVisible();
  await expect(light.locator(".hg-px-cell-label").filter({ hasText: new RegExp(`^queen-32$`) })).toBeVisible();
  await expect(light.locator(".hg-px-cell-label").filter({ hasText: new RegExp(`^comb-32$`) })).toBeVisible();
  await expect(light.locator(".hg-loader")).toBeVisible();
  // bee-16 / queen-16 / bee-32 / queen-32 at three steps each, plus the comb.
  await expect(light.locator(".hg-px")).toHaveCount(13);
  const drawn = await light.locator(".hg-px").evaluateAll(nodes =>
    nodes.map(node => Number(node.getAttribute("width"))),
  );
  expect(drawn.sort((a, b) => a - b)).toEqual([16, 16, 32, 32, 32, 32, 48, 48, 64, 64, 96, 96, 96]);

  await expect(page.locator("#pixel-icons")).toBeVisible();
  await page.locator("#pixel-icons").screenshot({ path: testInfo.outputPath("pixel-icons-1440.png") });
  await page.locator("#forager-avatars").screenshot({ path: testInfo.outputPath("forager-avatars-1440.png") });
  await page.screenshot({ path: testInfo.outputPath("icon-lab-1440.png"), fullPage: true });
});

const avatarSignature = (handle: Locator) =>
  handle.evaluate((node: HTMLElement) => `${node.style.getPropertyValue("--hg-px-body")}|${node.innerHTML}`);

test("Forager avatars are stable per id and different across ids", async ({ page }) => {
  await page.goto("/#/hives/swarmjet");

  // Same Forager, same size, two places: the Swarm rail and the conversation.
  const same = page.locator('.hg-px-avatar[aria-label="Forager forager-7f3a"]');
  await expect(same).toHaveCount(2);
  const [rail, stream] = await same.all();
  const railSignature = await avatarSignature(rail);
  expect(railSignature).toBe(await avatarSignature(stream));

  // A different Forager gets a different bee.
  const other = page.locator('.hg-px-avatar[aria-label="Forager forager-2b91"]').first();
  expect(await avatarSignature(other)).not.toBe(railSignature);

  // Twelve ids, twelve distinct looks: colour, stripes, accessory and facing all
  // come off the hash, and nothing else feeds into the drawing.
  await page.goto("/#/icons");
  const cells = page.locator("#forager-avatars .hg-icon-lab-panel").first().locator(".hg-px-cell");
  await expect(cells).toHaveCount(12);
  const signatures = await cells.evaluateAll(nodes =>
    nodes.map(node => {
      const avatar = node.querySelector<HTMLElement>(".hg-px-avatar");
      return `${avatar?.style.getPropertyValue("--hg-px-body")}|${avatar?.innerHTML}`;
    }),
  );
  expect(new Set(signatures).size).toBe(12);
  expect(signatures.every(Boolean)).toBe(true);
});

test("The bee loader flaps between two wing frames", async ({ page }) => {
  await page.goto("/#/icons");
  const loader = page.locator(".hg-loader").first();
  await expect(loader).toBeVisible();
  await expect(loader.locator(".hg-loader-frame")).toHaveCount(2);
  const names = await loader.locator(".hg-loader-frame").evaluateAll(nodes =>
    nodes.map(node => getComputedStyle(node).animationName),
  );
  expect(names[0]).not.toBe("none");
  expect(names[1]).not.toBe("none");
});

test.describe("reduced motion", () => {
  test.use({ reducedMotion: "reduce" });

  test("The loader parks on its first frame when motion is reduced", async ({ page }) => {
    await page.goto("/#/icons");
    const loader = page.locator(".hg-loader").first();
    await expect(loader).toBeVisible();

    const state = await loader.evaluate(node => {
      const bob = node.querySelector<HTMLElement>(".hg-loader-bee");
      const up = node.querySelector<HTMLElement>(".hg-loader-frame-up");
      const down = node.querySelector<HTMLElement>(".hg-loader-frame-down");
      return {
        bob: bob ? getComputedStyle(bob).animationName : "missing",
        up: up ? getComputedStyle(up).animationName : "missing",
        down: down ? getComputedStyle(down).animationName : "missing",
        downVisibility: down ? getComputedStyle(down).visibility : "missing",
        upVisibility: up ? getComputedStyle(up).visibility : "missing",
      };
    });

    expect(state.bob).toBe("none");
    expect(state.up).toBe("none");
    expect(state.down).toBe("none");
    expect(state.upVisibility).toBe("visible");
    expect(state.downVisibility).toBe("hidden");
  });
});
