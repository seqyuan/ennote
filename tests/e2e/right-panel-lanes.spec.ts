import { expect, test, type Page, type Route } from "@playwright/test";
import { selectProject, tryFulfillHarnessRoutes } from "./harness";

/**
 * The panel's two lanes.
 *
 * The point of the split is that a file opening on the right does not unmount
 * the tree on the left, so the tree keeps its expanded directories and its
 * scroll position. These tests pin that: the same rows are still there, still
 * expanded, with the file drawn beside them.
 */

const project = { id: "lane-project", name: "Lane project", description: "", status: "active", createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" };
const workspace = { id: "workspace", projectId: project.id, kind: "local", hostPath: "/tmp/lane-project", virtualPath: "/workspace", status: "active", pathFingerprint: "fingerprint", createdAt: "2026-10-01T00:00:00Z" };
const policies = ["discuss", "ask", "auto"].map((mode) => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" }));
const provider = { id: "lane-provider", name: "Lane provider", providerType: "openai-compatible", baseUrl: "https://example.test", credentialConfigured: true, status: "active", createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" };

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockLanes(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files`) {
      const requested = url.searchParams.get("path");
      return fulfill(route, requested === "/workspace/src"
        ? [{ name: "main.ts", path: "/workspace/src/main.ts", isDir: false, size: 30, modifiedAt: "2026-10-01T00:00:00Z" }]
        : [{ name: "src", path: "/workspace/src", isDir: true, size: 0, modifiedAt: "2026-10-01T00:00:00Z" }]);
    }
    if (path === `/v1/projects/${project.id}/files/content`) {
      return route.fulfill({ status: 200, contentType: "text/typescript", body: "export const ready = true;\n" });
    }
    return route.abort();
  });
}

test("opening a file keeps the tree mounted beside it", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  const lanes = page.getByTestId("right-panel-lanes");
  // Nothing is open on the right yet, so the panel is one channel, not a split.
  await expect(lanes).toHaveAttribute("data-mode", "single");

  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();

  // The file is on the right...
  await expect(page.locator(".file-code")).toContainText("ready");
  await expect(lanes).toHaveAttribute("data-mode", "wide");
  // ...and the tree it came from is still on the left, still expanded.
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();

  // Closing the file empties the inspector lane; the tree is untouched.
  await page.getByRole("button", { name: "Close main.ts" }).click();
  await expect(page.locator(".file-code")).toHaveCount(0);
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");
  await expect(lanes).toHaveAttribute("data-mode", "single");
});

test("a medium panel slides the navigator over the preview", async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();

  const lanes = page.getByTestId("right-panel-lanes");
  await expect(lanes).toHaveAttribute("data-mode", "medium");
  await expect(page.locator(".file-code")).toContainText("ready");

  // Not drawn beside the preview at this width: the navigator is parked off the edge.
  const navigator = page.getByTestId("right-panel-primary");
  await expect(navigator).toHaveAttribute("data-parked", "true");

  await page.getByRole("button", { name: "Show the file navigator" }).click();
  await expect(navigator).not.toHaveAttribute("data-parked", "true");
  await expect(navigator).toHaveAttribute("data-open", "true");

  // The tree kept the directory it had expanded before the file opened.
  await expect(navigator.locator('.file-tree-main[title="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");

  // Picking a channel answers what the navigator was opened for, so it parks again
  // and the preview's lane is left as it was.
  await navigator.getByRole("tab", { name: "Graphs" }).click();
  await expect(navigator).toHaveAttribute("data-parked", "true");
  await expect(page.locator(".file-code")).toContainText("ready");
});

test("a narrow panel keeps the single tab strip", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  // Below 960 the panel is a drawer, so it is the single strip whatever its width.
  await expect(page.getByRole("tab", { name: "Graphs" })).toBeVisible();
  await expect(page.getByTestId("right-panel-lanes")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Back to conversation" })).toBeVisible();
});
