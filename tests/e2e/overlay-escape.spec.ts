import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillBlankSessionCreate } from "./harness";

const project = { id: "overlay-project", name: "Overlay project", description: "", status: "active", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:00Z" };
const workspace = { id: "workspace", projectId: project.id, kind: "local", hostPath: "/tmp/overlay-project", virtualPath: "/workspace", status: "active", pathFingerprint: "fingerprint", createdAt: "2026-07-30T00:00:00Z" };
const provider = { id: "overlay-provider", name: "Overlay provider", providerType: "openai-compatible", baseUrl: "https://example.test", credentialConfigured: true, status: "active", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:00Z" };

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockBase(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files`) return fulfill(route, []);
    return route.abort();
  });
}

test("nested provider delete dialog consumes one Escape without closing Settings", async ({ page }) => {
  await mockBase(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("tab", { name: "Models" }).click();
  await page.getByRole("button", { name: "Delete Overlay provider" }).click();

  const nested = page.getByRole("dialog", { name: "Delete Overlay provider?" });
  await expect(nested).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(nested).toHaveCount(0);
  await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();
});
