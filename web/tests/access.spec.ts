import { expect, test } from "@playwright/test";

test("email owner sets credentials before approval", async ({ page }) => {
  const requests: { path: string; body: Record<string, unknown> }[] = [];
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/session") return route.fulfill({ status: 401, json: { error: "authentication required" } });
    if (path === "/api/register" || path === "/api/register/verify") {
      requests.push({ path, body: request.postDataJSON() });
      return path.endsWith("verify") ? route.fulfill({ status: 204, body: "" }) : route.fulfill({ status: 202, json: { status: "pending_email" } });
    }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/register");
  await page.getByLabel("邮箱", { exact: true }).fill("member@example.test");
  await page.getByRole("button", { name: "发送验证邮件" }).click();
  await expect(page.getByRole("heading", { name: "验证邮箱" })).toBeVisible();
  await page.getByLabel("邮件中的验证令牌").fill("a".repeat(64));
  await page.getByLabel("姓名", { exact: true }).fill("Member");
  await page.getByLabel("设置密码").fill("a suitably long password");
  await page.getByRole("button", { name: "验证并提交申请" }).click();
  await expect(page.getByRole("heading", { name: "等待审批" })).toBeVisible();
  expect(requests).toEqual([
    { path: "/api/register", body: { email: "member@example.test" } },
    { path: "/api/register/verify", body: { token: "a".repeat(64), displayName: "Member", password: "a suitably long password" } },
  ]);
});

test("project viewer sees private project without administrator controls", async ({ page }) => {
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { id: "viewer", username: "viewer@example.test", displayName: "Viewer", platformAdmin: false } });
    if (path === "/api/projects") return route.fulfill({ json: [{ id: "alpha", name: "Alpha", myRole: "viewer", repositories: [] }] });
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/projects");
  await expect(page.getByRole("link", { name: "Alpha" })).toBeVisible();
  await expect(page.getByRole("link", { name: "成员与团队" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "执行资源" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "新建项目" })).toHaveCount(0);
  await page.getByRole("link", { name: "Alpha" }).click();
  await expect(page.getByText("你在此项目的角色：观察者")).toBeVisible();
});

test("project owner can allow all autonomous-labeled GitLab issues", async ({ page }) => {
  let mode = "off";
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/session") return route.fulfill({ json: { id: "owner", username: "owner@example.test", platformAdmin: false } });
    if (path === "/api/projects") return route.fulfill({ json: [{ id: "alpha", name: "Alpha", provider: "gitlab", myRole: "owner", repositories: [] }] });
    if (path === "/api/projects/alpha/members") return route.fulfill({ json: { members: [] } });
    if (path === "/api/users") return route.fulfill({ json: { users: [] } });
    if (path === "/api/audit") return route.fulfill({ json: { events: [] } });
    if (path === "/api/projects/alpha/trigger-policy") {
      if (request.method() === "PUT") {
        mode = request.postDataJSON().gitlabTriggerMode;
      }
      return route.fulfill({ json: { gitlabTriggerMode: mode, githubAutoIntake: false } });
    }
    return route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/projects/alpha/access");
  const policy = page.getByLabel("GitLab 自动接单策略");
  await expect(policy).toHaveValue("off");
  await policy.selectOption("all");
  await page.getByRole("button", { name: "保存策略" }).click();
  await expect(page.getByRole("status")).toContainText("自动接单策略已保存");
  expect(mode).toBe("all");
  await expect(policy).toHaveValue("all");
});
