import { expect, test, type Locator } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

// Desktop only (ADR section 18). Screenshots land in test-results/ at 1440px.
test.use({ viewport: { width: 1440, height: 900 } });

test("Hive list renders cards from the API, filters, and falls back to an empty state", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives");

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

test("Hive conversation shows real messages and sends to the Queen", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet");

  await expect(page.getByRole("heading", { name: "SwarmJet 平台", level: 1 })).toBeVisible();
  await expect(page.getByText("Queen（蜂王）已开启", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "对话", level: 2 })).toBeVisible();

  await expect(
    page.getByText("收到。我先把 #142 交给一只使用 implementation 预设的 Forager", { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByText("把 #142 派出去，顺便让一只独立的 Forager 审一下 #88 的改动。", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("heading", { name: "Swarm（蜂群）", level: 2 })).toBeVisible();
  await expect(page.getByRole("link", { name: /forager-7f3a/ })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("hive-conversation-1440.png"), fullPage: true });

  await page.getByLabel("对 Queen（蜂王）说话").fill("再派一只 Forager 审一下 #88");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect(page.locator(".hg-form-status")).toContainText("已发送给 Queen");
  await expect(page.getByText("再派一只 Forager 审一下 #88", { exact: true })).toBeVisible();

  const sent = state.calls.filter(
    call => call.method === "POST" && call.path === "/api/hives/swarmjet/messages",
  );
  expect(sent).toHaveLength(1);
  expect(sent[0].body).toMatchObject({ to: "queen", body: "再派一只 Forager 审一下 #88" });
});

test("Hive with Queen disabled dispatches a Forager through the API", async ({ page }, testInfo) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/orders");

  await expect(page.getByText("Queen（蜂王）未开启", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "直接派 Forager", level: 2 })).toBeVisible();

  await page.getByRole("button", { name: "派出 Forager" }).click();
  await expect(page.getByRole("alert")).toContainText("派活说明不能为空");
  await page.screenshot({ path: testInfo.outputPath("hive-dispatch-1440.png"), fullPage: true });

  await page.getByLabel("派活说明").fill("把订单导出改成流式响应，避免大报表超时。");
  await page.getByRole("button", { name: "派出 Forager" }).click();
  await expect(page.locator(".hg-form-status")).toContainText("已派出 forager-new-");
  await expect(page.getByRole("link", { name: /forager-new-/ })).toBeVisible();

  const spawn = state.calls.filter(
    call => call.method === "POST" && call.path === "/api/hives/orders/foragers",
  );
  expect(spawn).toHaveLength(1);
  expect(spawn[0].body).toMatchObject({
    task: "把订单导出改成流式响应，避免大报表超时。",
    preset: "implementation",
  });
});

test("Forager detail uses the real conversation and handoff", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet/foragers/forager-7f3a");

  await expect(page.getByRole("heading", { name: /forager-7f3a/ })).toBeVisible();
  await expect(page.getByText("为 Hive 页的 Forager 列表增加按状态筛选", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "对话", level: 2 })).toBeVisible();
  await expect(page.locator(".hg-msg-queen .hg-msg-body").first()).toContainText(
    "处理 #142：在 Swarm 面板增加按状态筛选。",
  );

  await page.screenshot({ path: testInfo.outputPath("forager-conversation-1440.png"), fullPage: true });

  await page.getByRole("link", { name: "交接摘要" }).click();
  await expect(page).toHaveURL(/#\/legacy\/hives\/swarmjet\/foragers\/forager-7f3a\/handoff$/);
  await expect(page.getByRole("heading", { name: "交接摘要", level: 2 })).toBeVisible();
  await expect(page.getByText("在 Swarm 面板增加按状态筛选 Forager 的入口", { exact: true })).toBeVisible();
  await expect(page.getByText("前端筛选交互与空结果提示", { exact: true })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("forager-handoff-1440.png"), fullPage: true });
});

test("Hive Inbox shows unhandled external events", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet/inbox");

  await expect(page.getByRole("heading", { name: "Inbox（蜂巢收件箱）", level: 2 })).toBeVisible();
  await expect(page.getByText(/GitHub issues\.opened：#142/)).toBeVisible();
  await expect(page.locator(".hg-msg-external .hg-trust-external")).toHaveText("外部");
});

test("Cancel writes a control message and the Swarm badge follows the API", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet");

  const item = page.locator(".hg-swarm-item").filter({ hasText: "forager-7f3a" });
  await item.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.locator(".hg-form-status")).toContainText("已向 forager-7f3a 发送取消指令");
  await expect(
    page.locator(".hg-swarm-item").filter({ hasText: "forager-7f3a" }).getByText("已取消", { exact: true }),
  ).toBeVisible();
});

test("Conversation and Swarm panel refresh on the five-second poll", async ({ page }) => {
  const state = await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet");
  await expect(page.getByRole("link", { name: /forager-7f3a/ })).toBeVisible();

  state.messages.push({
    id: "poll-message",
    hiveId: "swarmjet",
    from: "queen",
    to: "local:web-admin",
    kind: "reply",
    chainId: "chain-poll",
    hop: 1,
    body: "轮询到达的新消息。",
    state: "pending",
    createdAt: new Date().toISOString(),
  });
  state.agents.swarmjet.push({
    id: "forager-poll",
    hiveId: "swarmjet",
    role: "forager",
    state: "parked",
    preset: "implementation",
    cli: "opencode",
    task: "轮询到达的新 Forager",
    branch: "swarm/swarmjet/forager-poll",
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  });

  await expect(page.getByText("轮询到达的新消息。", { exact: true })).toBeVisible({ timeout: 7000 });
  await expect(page.getByRole("link", { name: /forager-poll/ })).toBeVisible({ timeout: 7000 });
});

test("API failures show a readable error instead of pretending to be empty", async ({ page }) => {
  await mockHiveAPI(page, { failures: { hives: 500 }, errorMessage: "数据库暂时不可用" });
  await page.goto("/#/legacy/hives");
  await expect(page.getByRole("alert")).toContainText("数据库暂时不可用");
});

test("A 401 tells the user to sign in again", async ({ page }) => {
  await mockHiveAPI(page, { failures: { hives: 401 }, errorMessage: "invalid session" });
  await page.goto("/#/legacy/hives");
  await expect(page.getByRole("alert")).toContainText("会话已过期或未登录");
  await expect(page.getByRole("link", { name: "去登录" })).toHaveAttribute("href", "/");
});

test("A 403 explains that a platform administrator is required", async ({ page }) => {
  await mockHiveAPI(page, { failures: { hives: 403 }, errorMessage: "platform administrator required" });
  await page.goto("/#/legacy/hives");
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("需要平台管理员权限");
  await expect(alert).toContainText("需要管理员会话才能继续");
  await expect(page.getByRole("link", { name: "返回主控制台" })).toHaveAttribute("href", "/");
});

// Pixel reference page: the whole design spec — eight Forager states, the Queen
// on and off, six hashed body colours, the 29 icons at 16 and 32px, the loader
// and the empty state — on a light and on a dark surface.
test("Pixel reference shows the whole design spec on light and dark surfaces", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/icons");

  await expect(page.getByRole("heading", { name: "像素规范", level: 1 })).toBeVisible();
  const surfaces = page.locator(".hg-icon-lab-panel");
  await expect(surfaces).toHaveCount(4);

  const avatarSurfaces = [surfaces.nth(0), surfaces.nth(1)];
  const iconSurfaces = [surfaces.nth(2), surfaces.nth(3)];

  for (const surface of avatarSurfaces) {
    const states = surface.locator('[aria-label="Forager 状态"] .hg-px-cell');
    await expect(states).toHaveCount(8);
    for (const name of ["creating", "active", "parked", "needs_you", "hibernated", "completed", "failed", "cancelled"]) {
      await expect(
        states.locator(".hg-px-cell-label").filter({ hasText: new RegExp(`^${name}$`) }),
      ).toHaveCount(1);
    }
    // Queen on, Queen off, and the component that applies the off palette.
    await expect(surface.locator('[aria-label="Queen"] .hg-px-cell')).toHaveCount(3);
    await expect(surface.locator('[aria-label="身体颜色"] .hg-px-cell')).toHaveCount(6);
  }

  for (const surface of iconSurfaces) {
    const icons = surface.locator('[aria-label="图标集"] .hg-icon');
    await expect(icons).toHaveCount(29 * 2); // 16px + 32px
    await expect(surface.locator('[aria-label="图标集"] .hg-px-cell')).toHaveCount(29);
    for (const name of ["hive", "swarm", "crown", "bee"]) {
      await expect(
        surface.locator('[aria-label="图标集"] .hg-px-cell-label').filter({ hasText: new RegExp(`^${name}$`) }),
      ).toHaveCount(1);
    }
    await expect(surface.locator(".hg-loader")).toBeVisible();
    await expect(surface.locator("[aria-label='空状态'] .hg-empty-pixels")).toHaveCount(1);
  }

  // Only the four whole-pixel sizes are ever used.
  const sizes = await page.locator("#pixel-icons svg, #forager-avatars svg").evaluateAll(nodes =>
    nodes.map(node => Number(node.getAttribute("width"))),
  );
  expect(sizes.every(size => [16, 32, 48, 64].includes(size))).toBe(true);

  await page.locator("#forager-avatars").screenshot({ path: testInfo.outputPath("forager-avatars-1440.png") });
  await page.locator("#pixel-icons").screenshot({ path: testInfo.outputPath("pixel-icons-1440.png") });
  await page.screenshot({ path: testInfo.outputPath("icon-lab-1440.png"), fullPage: true });
});

/** Colour + geometry of a drawn sprite: enough to tell two states apart. */
const pixelSignature = (handle: Locator) =>
  handle.evaluate((node: HTMLElement) =>
    [...node.querySelectorAll("svg g[fill]")]
      .map(group => {
        const fill = group.getAttribute("fill") ?? "";
        const cells = [...group.querySelectorAll("rect")]
          .map(rect => `${rect.getAttribute("x")},${rect.getAttribute("y")}`)
          .sort()
          .join(" ");
        return `${fill}:${cells}`;
      })
      .sort()
      .join("|"),
  );

test("A Forager keeps the same bee, and every state draws a different one", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet");

  // Same Forager, same size, in the Swarm rail and in the conversation stream.
  const same = page.locator('.hg-px-avatar[aria-label="Forager forager-7f3a"]');
  await expect(same).toHaveCount(2);
  const [rail, stream] = await same.all();
  const railSignature = await pixelSignature(rail);
  expect(railSignature).toBe(await pixelSignature(stream));

  // The body colour is a literal hex (state overrides are the only other ones),
  // so the same id has to come back with the same body fill.
  const bodyColor = (handle: Locator) =>
    handle.evaluate((node: HTMLElement) => {
      const fills = [...node.querySelectorAll("svg g[fill]")].map(group => group.getAttribute("fill") ?? "");
      return fills.find(fill => /^#[0-9a-f]{6}$/i.test(fill)) ?? "";
    });
  expect(await bodyColor(rail)).toMatch(/^#[0-9a-f]{6}$/i);
  expect(await bodyColor(stream)).toBe(await bodyColor(rail));

  // Different Foragers are allowed to share a colour, but they are not forced to.
  const other = page.locator('.hg-px-avatar[aria-label="Forager forager-2b91"]').first();
  expect(await pixelSignature(other)).not.toBe(railSignature);

  // Eight states, eight distinct drawings on the reference page.
  await page.goto("/#/legacy/icons");
  const states = page
    .locator("#forager-avatars .hg-icon-lab-panel")
    .first()
    .locator('[aria-label="Forager 状态"] .hg-px-cell');
  await expect(states).toHaveCount(8);
  const drawn = await states.locator("svg").evaluateAll(nodes =>
    nodes.map(node => {
      const groups = [...node.querySelectorAll("g[fill]")].map(group => {
        const fill = group.getAttribute("fill") ?? "";
        const cells = [...group.querySelectorAll("rect")]
          .map(rect => `${rect.getAttribute("x")},${rect.getAttribute("y")}`)
          .sort()
          .join(" ");
        return `${fill}:${cells}`;
      });
      return groups.sort().join("|");
    }),
  );
  expect(new Set(drawn).size).toBe(8);
});

test("The bee loader flaps between two wing frames", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/icons");
  const loader = page.locator(".hg-loader").first();
  await expect(loader).toBeVisible();
  await expect(loader.locator(".hg-loader-frame")).toHaveCount(2);
  // Wait for the stylesheet to be applied: a missing rule would read as "" and
  // used to make this assertion pass for the wrong reason.
  await page.waitForFunction(() => {
    const frames = [...document.querySelectorAll(".hg-loader-frame")];
    return (
      frames.length > 0 &&
      frames.every(frame => {
        const name = getComputedStyle(frame).animationName;
        return name !== "" && name !== "none";
      })
    );
  });
  const names = await loader.locator(".hg-loader-frame").evaluateAll(nodes =>
    nodes.map(node => getComputedStyle(node).animationName),
  );
  expect(names[0]).not.toBe("none");
  expect(names[1]).not.toBe("none");
});

test.describe("reduced motion", () => {
  test.use({ reducedMotion: "reduce" });

  test("The loader parks on its first frame when motion is reduced", async ({ page }) => {
    await mockHiveAPI(page);
    await page.goto("/#/legacy/icons");
    const loader = page.locator(".hg-loader").first();
    await expect(loader).toBeVisible();
    // The reduced-motion rule sets animation: none; wait for the stylesheet so a
    // cold dev-server render cannot be mistaken for a parked animation.
    await page.waitForFunction(() => {
      const bee = document.querySelector<HTMLElement>(".hg-loader-bee");
      const up = document.querySelector<HTMLElement>(".hg-loader-frame-up");
      return (
        !!bee &&
        !!up &&
        getComputedStyle(bee).animationName === "none" &&
        getComputedStyle(up).animationName === "none"
      );
    });

    const state = await loader.evaluate(node => {
      const bob = node.querySelector<HTMLElement>(".hg-loader-bee");
      const up = node.querySelector<HTMLElement>(".hg-loader-frame-up");
      const down = node.querySelector<HTMLElement>(".hg-loader-frame-down");
      return {
        bob: bob ? getComputedStyle(bob).animationName : "missing",
        up: up ? getComputedStyle(up).animationName : "missing",
        down: down ? getComputedStyle(down).animationName : "missing",
        upVisibility: up ? getComputedStyle(up).visibility : "missing",
        downVisibility: down ? getComputedStyle(down).visibility : "missing",
      };
    });

    expect(state.bob).toBe("none");
    expect(state.up).toBe("none");
    expect(state.down).toBe("none");
    expect(state.upVisibility).toBe("visible");
    expect(state.downVisibility).toBe("hidden");
  });
});
