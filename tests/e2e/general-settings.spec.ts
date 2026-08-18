import { expect, test, type Page, type Route } from "@playwright/test";

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockApp(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    return route.abort();
  });
}

test("General tab persists appearance and default permission preferences", async ({ page }) => {
  await mockApp(page);
  await page.goto("/");
  // First-run guidance auto-opens the dialog when no provider is configured
  // yet (a tick after load); wait for it, dismiss it, then exercise the
  // sidebar trigger like a real user.
  const autoDialog = page.locator(".settings-dialog-shell");
  await autoDialog.waitFor({ state: "visible", timeout: 5000 }).catch(() => {});
  if (await autoDialog.isVisible()) {
    await page.getByRole("button", { name: "Close settings" }).click();
    await autoDialog.waitFor({ state: "hidden" });
  }
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("tab", { name: "General" }).click();

  // Appearance: pick System (three-state theme, dsh AppearanceRow).
  const system = page.getByRole("button", { name: "System", exact: true });
  await system.click();
  await expect(system).toHaveAttribute("aria-pressed", "true");

  // Default permission: pick Auto and verify the persisted preference.
  const auto = page.getByRole("button", { name: "Auto", exact: true });
  await auto.click();
  await expect(auto).toHaveAttribute("aria-pressed", "true");
  expect(await page.evaluate(() => localStorage.getItem("ennote-default-permission"))).toBe("auto");

  // Language: switch to 中文, persist, and translate the section title.
  const zh = page.getByRole("button", { name: "中文", exact: true });
  await zh.click();
  await expect(zh).toHaveAttribute("aria-pressed", "true");
  expect(await page.evaluate(() => localStorage.getItem("ennote-locale"))).toBe("zh-CN");
  await expect(page.getByRole("heading", { name: "通用", level: 2 })).toBeVisible();
});
