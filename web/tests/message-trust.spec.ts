// Round-4 message provenance: every row carries user / agent / external /
// system, and external content is visually marked without shouting.
import { expect, test } from "@playwright/test";
import { mockHiveAPI } from "./hive-api-mock";

test.use({ viewport: { width: 1440, height: 900 } });

test("the conversation shows all four provenance markers", async ({ page }, testInfo) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet");

  const stream = page.locator(".hg-stream");
  await expect(stream.locator(".hg-trust-user").first()).toHaveText("用户");
  await expect(stream.locator(".hg-trust-agent").first()).toHaveText("Agent");
  await expect(stream.locator(".hg-trust-system").first()).toHaveText("系统");

  const external = stream.locator(".hg-msg-external").first();
  await expect(external).toBeVisible();
  await expect(external.locator(".hg-trust-external")).toHaveText("外部");
  await expect(external).toContainText("外部内容，未经核实");

  // The external card uses the honey border and a 4px deep-amber edge; it is
  // visible but not the coral error surface.
  const style = await external.evaluate(node => {
    const computed = getComputedStyle(node);
    return {
      borderLeftWidth: computed.borderLeftWidth,
      background: computed.backgroundColor,
      borderLeftColor: computed.borderLeftColor,
    };
  });
  expect(style.borderLeftWidth).toBe("4px");
  expect(style.borderLeftColor).toBe("rgb(201, 138, 21)");
  expect(style.background).toBe("rgb(251, 235, 200)");

  await page.screenshot({ path: testInfo.outputPath("message-trust-1440.png"), fullPage: true });
});

test("an unknown trust value from a newer server is shown raw", async ({ page }) => {
  const state = await mockHiveAPI(page);
  const message = state.messages.find(item => item.id === "msg-1");
  if (message) message.trust = "partner";
  await page.goto("/#/legacy/hives/swarmjet");

  await expect(page.locator(".hg-stream .hg-trust-unknown").first()).toHaveText("partner");
});

test("the Inbox marks router events as external", async ({ page }) => {
  await mockHiveAPI(page);
  await page.goto("/#/legacy/hives/swarmjet/inbox");

  await expect(page.locator(".hg-msg-external .hg-trust-external")).toHaveText("外部");
  await expect(page.getByText("外部内容，未经核实", { exact: true })).toBeVisible();
});
