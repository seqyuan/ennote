import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "rail-project", name: "Rail", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const provider = { id: "rail-provider", name: "test", providerType: "openai-compatible", baseUrl: "https://example.test", apiKey: "test", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockWorker(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    // A configured provider suppresses the first-run Models settings auto-open.
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path.endsWith("/active-run")) return fulfill(route, null);
    if (path.endsWith("/messages")) return fulfill(route, { messages: [], hasMore: false });
    if (path.endsWith("/compactions")) return fulfill(route, []);
    return fulfill(route, null);
  });
}

test("desktop sidebar fully collapses (no rail) and reopens from the top-left button", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await mockWorker(page);
  await page.goto("/");

  const navigation = page.locator("#workspace-navigation");
  await expect(navigation).toHaveCSS("width", "280px");

  // Collapse via the logo-row toggle → fully hidden, not a 56px rail.
  await page.getByRole("button", { name: "Collapse navigation" }).click();
  await expect(navigation).toHaveCSS("width", "0px");
  await expect(navigation).toHaveCSS("visibility", "hidden");
  // The collapsed sidebar must not be focusable/clickable.
  await expect(page.getByRole("button", { name: "Collapse navigation" })).toBeHidden();

  // A floating expand button appears at the top-left corner.
  const expand = page.getByRole("button", { name: "Open navigation" });
  await expect(expand).toBeVisible();
  const box = await expand.boundingBox();
  expect(box).not.toBeNull();
  if (box) {
    expect(box.x).toBeLessThan(20);
    expect(box.y).toBeLessThan(20);
  }

  await expand.click();
  await expect(navigation).toHaveCSS("width", "280px");
  await expect(page.getByRole("button", { name: "Collapse navigation" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open navigation" })).toHaveCount(0);
});
