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
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.locator(".file-code")).toContainText("ready");

  // The other Project must not inherit the first one's file. The panel hydrates
  // per Project, so these assertions wait for the switch to settle rather than
  // for a fixed slice of time.
  await selectProject(page, second.name);
  await expect(page.getByRole("tab", { name: "main.ts" })).toHaveCount(0, { timeout: 15000 });
  await expect(page.locator(".file-code")).toHaveCount(0, { timeout: 15000 });

  // Coming back returns the reader to where they were.
  await selectProject(page, project.name);
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible({ timeout: 15000 });
  await expect(page.locator(".file-code")).toContainText("ready", { timeout: 15000 });
});

test("the lane divider cannot starve the preview", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
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

  // The settled width survives a reload, and so does the row it was measured with.
  const settled = inspector.width;
  await page.reload();
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "wide");
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();
  const afterReload = await page.getByTestId("right-panel-primary").boundingBox();
  expect(Math.abs((afterReload?.width ?? 0) - settled)).toBeLessThanOrEqual(4);
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
  await expect(page.locator('[data-path="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible();

  // Closing the file empties the inspector lane; the tree is untouched.
  await page.getByRole("button", { name: "Close main.ts" }).click();
  await expect(page.locator(".file-code")).toHaveCount(0);
  await expect(page.locator('[data-path="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");
  await expect(lanes).toHaveAttribute("data-mode", "single");
});

test("a medium panel slides the navigator over the preview", async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();

  const lanes = page.getByTestId("right-panel-lanes");
  await expect(lanes).toHaveAttribute("data-mode", "medium");
  await expect(page.locator(".file-code")).toContainText("ready");

  // Not drawn beside the preview at this width: the sheet starts closed, because
  // the file the reader just opened is what they asked to see.
  const navigator = page.getByTestId("right-panel-primary");
  await expect(navigator).toHaveAttribute("data-parked", "true");

  await page.getByRole("button", { name: "Show the file navigator" }).click();
  await expect(navigator).not.toHaveAttribute("data-parked", "true");
  await expect(navigator).toHaveAttribute("data-open", "true");

  // The tree kept the directory it had expanded before the file opened.
  await expect(navigator.locator('[data-path="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");

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

  for (const name of ["Files", "Graphs", "Status", "Canvas"]) {
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

test("the tree scrolls inside its lane", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 600 });
  // Enough rows that the list cannot fit: the point is that it scrolls at all.
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
      return fulfill(route, Array.from({ length: 60 }, (_, index) => ({
        name: `f${index}.txt`, path: `/workspace/f${index}.txt`, isDir: false, size: 1,
        modifiedAt: "2026-10-01T00:00:00Z",
      })));
    }
    return route.abort();
  });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  const list = page.locator(".file-tree-list");
  const metrics = await list.evaluate((el) => ({
    client: el.clientHeight,
    scroll: el.scrollHeight,
    parent: (el.parentElement as HTMLElement | null)?.clientHeight ?? 0,
  }));

  // A list taller than its box must scroll inside it, not spill out of the panel.
  expect(metrics.scroll).toBeGreaterThan(metrics.client);
  await list.evaluate((el) => { el.scrollTop = el.scrollHeight; });
  const lastVisible = await page.locator('.file-tree-main[title="/workspace/f59.txt"]').isVisible();
  expect(lastVisible).toBe(true);
});

test("the files a reader had open come back after a reload", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.locator(".file-code")).toContainText("ready");

  await page.reload();

  // The row is back and it is still the selected one, so the lane draws its body.
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();
  await expect(page.locator(".file-code")).toContainText("ready");
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "wide");
});

test("two open files both come back, with the same one selected", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
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
      return fulfill(route, [
        { name: "main.ts", path: "/workspace/main.ts", isDir: false, size: 30, modifiedAt: "2026-10-01T00:00:00Z" },
        { name: "util.ts", path: "/workspace/util.ts", isDir: false, size: 20, modifiedAt: "2026-10-01T00:00:00Z" },
      ]);
    }
    if (path === `/v1/projects/${project.id}/files/content`) {
      return route.fulfill({ status: 200, contentType: "text/typescript", body: "export const ready = true;\n" });
    }
    return route.abort();
  });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/main.ts"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/main.ts"]').click();
  await page.locator('.file-tree-main[title="/workspace/util.ts"]').click();
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();

  await page.reload();

  // Both rows are back, in the order they were opened, and util.ts is selected.
  const rows = page.getByRole("tablist").last().getByRole("tab");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toHaveText(/main\.ts/);
  await expect(rows.nth(1)).toHaveText(/util\.ts/);
  await expect(rows.nth(1)).toHaveAttribute("aria-selected", "true");
});

test("returning to a project after a reload gives its files back", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page, { withSecondProject: true });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.locator(".file-code")).toContainText("ready");

  await page.reload();
  await selectProject(page, second.name);
  await expect(page.getByRole("tab", { name: "main.ts" })).toHaveCount(0);

  // A's rows came from disk, not from the session, so they are here again.
  await selectProject(page, project.name);
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();
});

test("a stored row does not leak into another project", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page, { withSecondProject: true });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.locator(".file-code")).toContainText("ready");

  await page.reload();
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();

  await selectProject(page, second.name);
  await expect(page.getByRole("tab", { name: "main.ts" })).toHaveCount(0);
});

test("the navigator can be hidden and stays hidden", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "wide");

  // Hiding it hands the whole panel to the preview, and the lane stays mounted.
  await page.getByRole("button", { name: "Hide the file navigator" }).click();
  const lanes = page.getByTestId("right-panel-lanes");
  await expect(lanes).toHaveAttribute("data-mode", "fill");
  await expect(page.locator(".file-code")).toContainText("ready");
  await expect(page.getByRole("tab", { name: "Files" })).toBeHidden();
  await expect(page.getByRole("separator", { name: "Resize the file navigator" })).toHaveCount(0);

  // Hiding does not cost the tree its place: showing it again finds the same
  // directory still expanded.
  await page.getByRole("button", { name: "Show the file navigator" }).click();
  await expect(lanes).toHaveAttribute("data-mode", "wide");
  await expect(page.locator('[data-path="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");

  // The choice belongs to the Project, so a reload keeps the preview in front.
  await page.getByRole("button", { name: "Hide the file navigator" }).click();
  await page.reload();
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "fill");
});

test("the tree is walkable from the keyboard", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  // Tab order is one stop: the filter field, then whichever row holds focus.
  await page.getByPlaceholder("Filter files...").click();
  await page.keyboard.press("ArrowDown");
  await expect(page.locator('[data-path="/workspace/src"]')).toBeFocused();

  // Right expands, Right again steps into it, Enter opens the file.
  await page.keyboard.press("ArrowRight");
  await expect(page.locator('[data-path="/workspace/src"]')).toHaveAttribute("aria-expanded", "true");
  // The listing arrives after the expansion, so the child is not walkable yet.
  await expect(page.locator('[data-path="/workspace/src/main.ts"]')).toBeVisible();
  await page.keyboard.press("ArrowRight");
  await expect(page.locator('[data-path="/workspace/src/main.ts"]')).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();

  // Left on a child goes to its directory, Left again collapses it.
  await page.locator('[data-path="/workspace/src/main.ts"]').focus();
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator('[data-path="/workspace/src"]')).toBeFocused();
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator('[data-path="/workspace/src"]')).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator('[data-path="/workspace/src/main.ts"]')).toHaveCount(0);
});

test("the filter reaches into loaded directories", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  // Without the filter the nested file is hidden behind a collapsed directory.
  await expect(page.locator('[data-path="/workspace/src/main.ts"]')).toHaveCount(0);

  await page.getByPlaceholder("Filter files...").fill("main");
  // The filter opens the way to the hit by itself.
  await expect(page.locator('[data-path="/workspace/src/main.ts"]')).toBeVisible();
  await page.getByPlaceholder("Filter files...").fill("nothing-matches");
  await expect(page.getByText("No files match.")).toBeVisible();
});

test("a row offers its actions in a menu", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('[data-path="/workspace/src"]').click();
  await page.locator('[data-path="/workspace/src/main.ts"]').click({ button: "right" });

  const menu = page.getByRole("menu", { name: /Actions for main\.ts/ });
  await expect(menu).toBeVisible();
  await expect(menu.getByRole("menuitem", { name: "Open", exact: true })).toBeVisible();
  await expect(menu.getByRole("menuitem", { name: "Copy path" })).toBeVisible();
  await menu.getByRole("menuitem", { name: "Open in floating window", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Preview main.ts" })).toBeVisible();
});

test("a reader can create, rename and delete from the tree", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  // The write endpoints are stateful here: the mock keeps one directory listing.
  let listing = [{ name: "notes.txt", path: "/workspace/notes.txt", isDir: false, size: 3, modifiedAt: "2026-10-01T00:00:00Z" }];
  await page.route("**/api/worker/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files` && request.method() === "GET") return fulfill(route, listing);
    if (path === `/v1/projects/${project.id}/files/create`) {
      const body = JSON.parse(request.postData() ?? "{}");
      const name = String(body.path).split("/").filter(Boolean).pop() ?? String(body.path);
      listing = [...listing, { name, path: body.path, isDir: body.kind === "directory", size: 0, modifiedAt: "2026-10-01T00:00:00Z" }];
      return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ data: listing.at(-1) }) });
    }
    if (path === `/v1/projects/${project.id}/files/move`) {
      const body = JSON.parse(request.postData() ?? "{}");
      listing = listing.map((entry) => entry.path === body.from
        ? { ...entry, name: body.to.split("/").pop(), path: body.to }
        : entry);
      return fulfill(route, listing.find((entry) => entry.path === body.to));
    }
    if (path === `/v1/projects/${project.id}/files` && request.method() === "DELETE") {
      const target = url.searchParams.get("path");
      listing = listing.filter((entry) => entry.path !== target);
      return route.fulfill({ status: 204, body: "" });
    }
    return route.abort();
  });

  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('[data-path="/workspace/notes.txt"]')).toBeVisible();

  // New file, typed in the tree.
  await page.getByRole("button", { name: "New file" }).click();
  await page.getByLabel("New file name").fill("analysis.py");
  await page.getByLabel("New file name").press("Enter");
  await expect(page.locator('[data-path="/workspace/analysis.py"]')).toBeVisible();

  // Rename it from its row.
  await page.locator('[data-path="/workspace/analysis.py"]').click({ button: "right" });
  await page.getByRole("menuitem", { name: "Rename" }).click();
  await page.getByLabel("Rename analysis.py").fill("run.py");
  await page.getByLabel("Rename analysis.py").press("Enter");
  await expect(page.locator('[data-path="/workspace/run.py"]')).toBeVisible();
  await expect(page.locator('[data-path="/workspace/analysis.py"]')).toHaveCount(0);

  // Delete asks first, then actually removes it.
  await page.locator('[data-path="/workspace/run.py"]').click({ button: "right" });
  await page.getByRole("menuitem", { name: "Delete" }).click();
  const confirm = page.getByRole("alertdialog", { name: /Delete run\.py/ });
  await expect(confirm).toBeVisible();
  await confirm.getByRole("button", { name: "Delete" }).click();
  await expect(page.locator('[data-path="/workspace/run.py"]')).toHaveCount(0);
  await expect(page.locator('[data-path="/workspace/notes.txt"]')).toBeVisible();
});

test("a refused create says why", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  // A name is a segment: the panel refuses a slash before the Worker has to.
  await page.getByRole("button", { name: "New file" }).click();
  await page.getByLabel("New file name").fill("a/b.txt");
  await page.getByLabel("New file name").press("Enter");
  await expect(page.getByText("A name cannot contain a slash")).toBeVisible();
});

test("each channel keeps its own lane width", async ({ page }) => {
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
  await page.mouse.move(box.x + 90, box.y + box.height / 2, { steps: 6 });
  await page.mouse.up();

  const filesWidth = (await page.getByTestId("right-panel-primary").boundingBox())?.width ?? 0;
  expect(filesWidth).toBeGreaterThan(300);

  // Graphs gets its own width: switching channels must not inherit the tree's.
  await page.getByRole("tab", { name: "Graphs" }).click();
  await page.getByRole("tab", { name: "Files" }).click();
  await expect.poll(async () => (await page.getByTestId("right-panel-primary").boundingBox())?.width ?? 0)
    .toBeGreaterThan(300);
});

test("a channel's tab carries what it has to say", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/worker/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files`) return fulfill(route, []);
    if (path.endsWith("/graph-runs")) {
      return fulfill(route, [
        { runId: "run-1", sessionId: "s", projectId: project.id, flowVersionId: "v1", manifestDigest: "a".repeat(64), state: "completed", totalTokensUsed: 1, inputs: {}, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" },
        { runId: "run-2", sessionId: "s", projectId: project.id, flowVersionId: "v1", manifestDigest: "b".repeat(64), state: "running", totalTokensUsed: 1, inputs: {}, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z" },
      ]);
    }
    return route.fulfill({ status: 501, contentType: "application/json", body: JSON.stringify({ error: { code: "unmocked_route", message: path } }) });
  });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();

  // The count comes from the channel itself, on the tab, in the strip.
  await page.getByRole("tab", { name: "Graphs" }).click();
  await expect(page.getByRole("tab", { name: /Graphs 2 runs/ })).toBeVisible();
});

test("a file opens to the side and the two are compared", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files`) return fulfill(route, [
      { name: "a.ts", path: "/workspace/a.ts", isDir: false, size: 4, modifiedAt: "2026-10-01T00:00:00Z" },
      { name: "b.ts", path: "/workspace/b.ts", isDir: false, size: 4, modifiedAt: "2026-10-01T00:00:00Z" },
    ]);
    if (path === `/v1/projects/${project.id}/files/content`) {
      const wanted = new URL(route.request().url()).searchParams.get("path") ?? "";
      return route.fulfill({ status: 200, contentType: "text/typescript", body: wanted.endsWith("a.ts") ? "const A = 1;\n" : "const B = 2;\n" });
    }
    return route.fulfill({ status: 501, contentType: "application/json", body: JSON.stringify({ error: { code: "unmocked_route", message: path } }) });
  });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('.file-tree-main[title="/workspace/a.ts"]').click();
  // One file is one pane: the split appears only when there is something to
  // compare it with.
  await expect(page.locator(".file-code")).toContainText("const A = 1");
  await expect(page.getByTestId("inspector-pane-side")).toHaveCount(0);

  // The side pane is asked for explicitly, from the file's own menu.
  await page.locator('.file-tree-main[title="/workspace/b.ts"]').click({ button: "right" });
  await page.getByRole("menuitem", { name: "Open to the side" }).click();

  await expect(page.getByTestId("inspector-pane-main")).toContainText("const A = 1");
  await expect(page.getByTestId("inspector-pane-side")).toContainText("const B = 2");

  // Closing the side row returns the panel to one pane without touching the other.
  await page.getByRole("button", { name: "Close b.ts" }).click();
  await expect(page.getByTestId("inspector-pane-side")).toHaveCount(0);
  await expect(page.locator(".file-code")).toContainText("const A = 1");
});

test("the side pane survives a reload, and the same file is never opened twice", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await expect(page.locator('.file-tree-main[title="/workspace/src"]')).toBeVisible();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.locator(".file-code")).toContainText("ready");

  // Asking for the same file to the side focuses it where it already is.
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click({ button: "right" });
  await page.getByRole("menuitem", { name: "Open to the side" }).click();
  await expect(page.getByTestId("inspector-pane-side")).toHaveCount(0);
  await expect(page.getByRole("tab", { name: "main.ts" })).toHaveCount(1);

  await page.reload();
  await expect(page.getByRole("tab", { name: "main.ts" })).toBeVisible();
});

test("two panes need room, and the reading position survives the split", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files`) return fulfill(route, [
      { name: "a.ts", path: "/workspace/a.ts", isDir: false, size: 4, modifiedAt: "2026-10-01T00:00:00Z" },
      { name: "b.ts", path: "/workspace/b.ts", isDir: false, size: 4, modifiedAt: "2026-10-01T00:00:00Z" },
    ]);
    if (path === `/v1/projects/${project.id}/files/content`) {
      // Enough lines that the viewer has something to scroll.
      const body = Array.from({ length: 200 }, (_, index) => `line ${index}`).join("\n");
      return route.fulfill({ status: 200, contentType: "text/typescript", body });
    }
    return route.fulfill({ status: 501, contentType: "application/json", body: JSON.stringify({ error: { code: "unmocked_route", message: path } }) });
  });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('.file-tree-main[title="/workspace/a.ts"]').click();
  await expect(page.locator(".file-code")).toContainText("line 0");

  // Scroll the main file, then split: the split must not reset where the reader was.
  const mainScroller = page.locator(".file-viewer-body").first();
  await mainScroller.evaluate((el) => { el.scrollTop = 400; });
  await page.locator('.file-tree-main[title="/workspace/b.ts"]').click({ button: "right" });
  await page.getByRole("menuitem", { name: "Open to the side" }).click();

  const panes = page.locator(".right-panel-pane");
  await expect(panes).toHaveCount(2);

  // Each pane scrolls on its own: a shared scroller would move both together.
  const scrolls = await page.evaluate(() => {
    const bodies = Array.from(document.querySelectorAll(".file-viewer-body")) as HTMLElement[];
    return bodies.map((el) => ({ top: el.scrollTop, scrollable: el.scrollHeight > el.clientHeight }));
  });
  expect(scrolls).toHaveLength(2);
  expect(scrolls.every((entry) => entry.scrollable)).toBe(true);
  expect(scrolls[0].top).toBeGreaterThan(0);
});

test("a narrow panel offers no side pane", async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 900 });
  await mockLanes(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('.file-tree-main[title="/workspace/src"]').click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click();
  await expect(page.getByTestId("right-panel-lanes")).toHaveAttribute("data-mode", "medium");

  // No room for two readable files, so the action is not offered at all. The tree
  // lives in the overlay at this width, so it is opened first.
  await page.getByRole("button", { name: "Show the file navigator" }).click();
  await page.locator('.file-tree-main[title="/workspace/src/main.ts"]').click({ button: "right" });
  await expect(page.getByRole("menuitem", { name: "Open to the side" })).toHaveCount(0);
});

test("the side pane comes back after a reload", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/workspace`) return fulfill(route, workspace);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/files`) return fulfill(route, [
      { name: "a.ts", path: "/workspace/a.ts", isDir: false, size: 4, modifiedAt: "2026-10-01T00:00:00Z" },
      { name: "b.ts", path: "/workspace/b.ts", isDir: false, size: 4, modifiedAt: "2026-10-01T00:00:00Z" },
    ]);
    if (path === `/v1/projects/${project.id}/files/content`) {
      const wanted = new URL(route.request().url()).searchParams.get("path") ?? "";
      return route.fulfill({ status: 200, contentType: "text/typescript", body: wanted.endsWith("a.ts") ? "const A = 1;" : "const B = 2;" });
    }
    return route.fulfill({ status: 501, contentType: "application/json", body: JSON.stringify({ error: { code: "unmocked_route", message: path } }) });
  });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByTitle("Show panel").click();
  await page.locator('.file-tree-main[title="/workspace/a.ts"]').click();
  await page.locator('.file-tree-main[title="/workspace/b.ts"]').click({ button: "right" });
  await page.getByRole("menuitem", { name: "Open to the side" }).click();
  await expect(page.getByTestId("inspector-pane-side")).toContainText("const B = 2");

  await page.reload();
  await expect(page.getByTestId("inspector-pane-main")).toContainText("const A = 1");
  await expect(page.getByTestId("inspector-pane-side")).toContainText("const B = 2");

  // Turning the split off leaves one pane that keeps the file it was showing.
  await page.getByRole("button", { name: "Close b.ts" }).click();
  await expect(page.getByTestId("inspector-pane-side")).toHaveCount(0);
  await expect(page.getByTestId("inspector-pane-focused")).toContainText("const A = 1");
});
