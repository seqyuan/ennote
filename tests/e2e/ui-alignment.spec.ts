import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "ui-project", name: "UI project", description: "", status: "active", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:00Z" };
const workspace = { id: "workspace", projectId: project.id, kind: "local", hostPath: "/tmp/ui-project", virtualPath: "/workspace", status: "active", pathFingerprint: "fingerprint", createdAt: "2026-07-30T00:00:00Z" };
const policies = ["discuss", "ask", "auto"].map((mode) => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:00Z" }));
const files = [
  { name: "src", path: "/workspace/src", isDir: true, size: 0, modifiedAt: "2026-07-30T00:00:00Z" },
  { name: "README.md", path: "/workspace/README.md", isDir: false, size: 28, modifiedAt: "2026-07-30T00:00:00Z" },
];

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockUI(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files`) {
      const requested = url.searchParams.get("path");
      return fulfill(route, requested === "/workspace/src"
        ? [{ name: "main.ts", path: "/workspace/src/main.ts", isDir: false, size: 30, modifiedAt: "2026-07-30T00:00:00Z" }]
        : files);
    }
    if (path === `/v1/projects/${project.id}/files/content`) {
      const requested = url.searchParams.get("path");
      return route.fulfill({
        status: 200,
        contentType: requested?.endsWith(".md") ? "text/markdown" : "text/typescript",
        body: requested?.endsWith(".md") ? "# UI preview\n\nRendered markdown." : "export const ready = true;\n",
      });
    }
    return route.abort();
  });
}

async function selectProject(page: Page) {
  await page.getByTitle("Select project").click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
}

test("file panel renders Markdown, code, and a floating preview", async ({ page }) => {
  await mockUI(page);
  await page.goto("/");
  await selectProject(page);
  await expect(page.locator(".conversation-header")).toHaveCount(0);
  await page.getByTitle("Show panel").click();
  await page.getByTitle("/workspace/README.md").click();
  await expect(page.locator(".file-markdown")).toContainText("Rendered markdown");
  await page.getByRole("tab", { name: "Files" }).click();
  await page.getByLabel("Preview README.md in a floating window").click();
  await expect(page.getByRole("dialog", { name: "Preview README.md" })).toBeVisible();
  await page.getByLabel("Close preview").click();
  await page.getByTitle("/workspace/src").click();
  await page.getByTitle("/workspace/src/main.ts").click();
  await expect(page.locator(".file-code")).toContainText("ready");
});

test("settings is modal and the theme preference stays synchronized", async ({ page }) => {
  await mockUI(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Switch to dark mode" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Settings" })).toHaveAttribute("aria-modal", "true");
});

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the full-screen file panel has a visible return action", async ({ page }) => {
    await mockUI(page);
    await page.goto("/");
    await page.getByTitle("Show panel").click();
    const back = page.getByRole("button", { name: "Back to conversation" });
    await expect(back).toBeVisible();
    await back.click();
    await expect(page.locator(".right-panel-container")).toBeHidden();
    await expect(page.locator(".conversation-header")).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  });
});
