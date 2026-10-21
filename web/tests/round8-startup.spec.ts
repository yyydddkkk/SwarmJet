import { expect, test } from "@playwright/test";
import { mockStarsHTTP } from "./stars-http-mock";

test("session and resource loading start before nonessential panel code resolves", async ({ page }) => {
  await mockStarsHTTP(page);
  let panelReleasedAt = 0;
  let sessionAt = 0;
  let panelRequests = 0;
  const startedAt = Date.now();
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/session" && !sessionAt) sessionAt = Date.now();
  });
  await page.route("**/src/stars/PanelsSlot.tsx*", async (route) => {
    panelRequests++;
    await new Promise((resolve) => setTimeout(resolve, 700));
    panelReleasedAt = Date.now();
    await route.continue();
  });
  await page.goto("/");
  await expect.poll(() => sessionAt).toBeGreaterThan(0);
  await page.getByRole("button", { name: "进入星图", exact: true }).click();
  await page.getByRole("button", { name: /^(SwarmJet )?平台设置$/ }).click();
  await expect.poll(() => panelReleasedAt).toBeGreaterThan(0);
  expect(panelRequests).toBeGreaterThan(0);
  console.log(`STARTUP_BOUNDARY session=${sessionAt - startedAt}ms panelsReleased=${panelReleasedAt - startedAt}ms`);
  expect(sessionAt).toBeLessThan(panelReleasedAt);
});
