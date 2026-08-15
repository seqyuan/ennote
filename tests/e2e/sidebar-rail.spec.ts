import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "rail-project", name: "Rail", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockWorker(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path.endsWith("/active-run")) return fulfill(route, null);
    if (path.endsWith("/messages")) return fulfill(route, { messages: [], hasMore: false });
    if (path.endsWith("/compactions")) return fulfill(route, []);
    return fulfill(route, null);
  });
}

test("desktop sidebar collapses to a 56px rail and expands back", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await mockWorker(page);
  await page.goto("/");

  const navigation = page.locator("#workspace-navigation");
  const collapse = page.getByRole("button", { name: "Collapse navigation" });
  await collapse.click();

  await expect(navigation).toHaveCSS("width", "56px");
  // The rail keeps an expand affordance in the logo row.
  const expand = page.getByRole("button", { name: "Open navigation" });
  await expand.click();

  await expect(navigation).not.toHaveCSS("width", "56px");
  await expect(page.getByRole("button", { name: "Collapse navigation" })).toBeVisible();
});
