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

async function mockLanes(page: Page, options: { withSecondProject?: boolean } = {}) {
  const projects = options.withSecondProject ? [project, second] : [project];
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, projects);
    if (path === `/v1/projects/${second.id}/workspace`) return fulfill(route, secondWorkspace);
    if (path === `/v1/projects/${second.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${second.id}/files`) return fulfill(route, []);
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

const second = { id: "other-project", name: "Other project", description: "", status: "active", createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" };
const secondWorkspace = { id: "workspace-2", projectId: second.id, kind: "local", hostPath: "/tmp/other-project", virtualPath: "/workspace", status: "active", pathFingerprint: "fingerprint-2", createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" };

test("switching projects and back returns to the file that was open", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page, { withSecondProject: true });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.locator(".file-code")).toContainText("ready");

  // The other Project must not inherit the first one's file.
  await selectProject(page, second.name);
  await expect(page.locator(".file-code")).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "main.ts" })).toHaveCount(0);

  // Coming back returns the reader to where they were.
  await selectProject(page, project.name);
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();
  await expect(page.locator(".file-code")).toContainText("ready");
});

test("the lane divider cannot starve the preview", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "wide");

  const divider = page.getByRole("separator", { name: "Resize the file navigator" });
  const box = await divider.boundingBox();
  if (!box) throw new Error("divider has no box");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + 600, box.y + box.height / 2, { steps: 8 });
  await page.mouse.up();

  // 264px is the inspector's floor; the drag stops there rather than at the edge.
  const inspector = await page.getByTestId("right-panel-primary").boundingBox();
  const panel = await page.locator(".right-panel-container").boundingBox();
  if (!inspector || !panel) throw new Error("lanes have no box");
  expect(panel.width - inspector.width).toBeGreaterThanOrEqual(264);

  // The settled width survives a reload. The open file does not — inspector tabs
  // are deliberately not persisted — so the file is reopened to get a split back.
  const settled = inspector.width;
  await page.reload();
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "single");
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "wide");
  const afterReload = await page.getByTestId("right-panel-primary").boundingBox();
  expect(Math.abs((afterReload?.width ?? 0) - settled)).toBeLessThanOrEqual(2);
});

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

test("closing a channel narrows the strip, pinning decides what returns", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  await expect(page.getByRole("tab", { name: "Status" })).toBeVisible();

  // Closing a channel removes it from this visit and leaves the others alone.
  await page.getByRole("button", { name: "Close Status" }).click();
  await expect(page.getByRole("tab", { name: "Status" })).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();

  // The add control brings back exactly the channel that is missing.
  await page.getByRole("button", { name: "Add a channel" }).click();
  await expect(page.getByRole("tab", { name: "Status" })).toBeVisible();

  // Pin acts on the selected channel, which the add control just selected.
  await expect(page.getByRole("button", { name: "Pin this channel" })).toHaveAttribute("aria-pressed", "false");
  await page.getByRole("button", { name: "Pin this channel" }).click();
  await expect(page.getByRole("button", { name: "Unpin this channel" })).toHaveAttribute("aria-pressed", "true");

  // A pin is the restore set: after a reload the strip is exactly what was pinned,
  // and the channels that were never pinned are not brought back.
  await page.reload();
  await expect(page.getByRole("tab", { name: "Status" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Files" })).toHaveCount(0);
});

test("closing a pinned channel hides it now and restores it later", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  await page.getByRole("button", { name: "Pin this channel" }).click();
  await expect(page.getByRole("button", { name: "Unpin this channel" })).toBeVisible();

  // Closing does not unpin: the tab leaves, the pin stays.
  await page.getByRole("button", { name: "Close Files" }).click();
  await expect(page.getByRole("tab", { name: "Files" })).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();
});

test("closing every channel leaves a launcher instead of an empty lane", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  for (const name of ["Files", "Graphs", "Status"]) {
    await page.getByRole("button", { name: `Close ${name}` }).click();
  }

  // The strip has nothing left, so the lane offers the way back.
  const launcher = page.getByRole("group", { name: /Choose a channel/ });
  await expect(launcher).toBeVisible();
  await launcher.getByRole("button", { name: "Graphs" }).click();
  await expect(page.getByRole("tab", { name: "Graphs" })).toBeVisible();
});

test("unpinning leaves the channel open but stops restoring it", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  await page.getByRole("button", { name: "Pin this channel" }).click();
  await expect(page.getByRole("button", { name: "Unpin this channel" })).toHaveAttribute("aria-pressed", "true");
  await page.reload();
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();

  // Unpin keeps the strip exactly as it is; only the next visit changes.
  await page.getByRole("button", { name: "Unpin this channel" }).click();
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Add a channel" })).toBeVisible();

  await page.reload();
  // No pin is left, so the next visit falls back to the default set of all three.
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Graphs" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Status" })).toBeVisible();
});

test("a pinned channel does not reopen a shell the reader collapsed", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.getByRole("button", { name: "Pin this channel" }).click();

  // Collapse from the top bar, the way a reader would.
  await page.getByTitle("Hide panel").click();
  await expect(page.locator(".right-panel-container")).toHaveClass(/right-panel-closed/);

  await page.reload();
  await expect(page.locator(".right-panel-container")).toHaveClass(/right-panel-closed/);

  // The pin still decides what appears once the shell is opened again.
  await page.getByTitle("Show panel").click();
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();
});
