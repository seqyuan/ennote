import { expect, type Page, type Route } from "@playwright/test";

/** Worker JSON envelope used by the mocked `/api/worker/v1` routes. */
export function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

function blankSession(projectId: string) {
  const now = "2026-07-30T00:00:00Z";
  return {
    id: `e2e-blank-${projectId}`,
    projectId,
    title: "New Chat",
    status: "active",
    createdAt: now,
    updatedAt: now,
  };
}

/**
 * Startup auto-connect POSTs a blank session when the project has no unused
 * blank. Specs that return a session *list* for both GET and POST make that
 * call throw "Failed to create session". Handle POST (and later fetches of
 * the created row) here; leave GET list to the spec.
 */
export async function tryFulfillBlankSessionCreate(route: Route): Promise<boolean> {
  const method = route.request().method();
  const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
  const create = path.match(/^\/v1\/projects\/([^/]+)\/sessions$/);
  if (method === "POST" && create) {
    await fulfill(route, blankSession(create[1]), 201);
    return true;
  }
  const detail = path.match(/^\/v1\/sessions\/(e2e-blank-[^/]+)(\/.*)?$/);
  if (!detail) return false;
  const projectId = detail[1].slice("e2e-blank-".length);
  const rest = detail[2] ?? "";
  if (rest === "") {
    await fulfill(route, blankSession(projectId));
    return true;
  }
  if (rest === "/active-run" || rest === "/recovery") {
    await fulfill(route, null);
    return true;
  }
  if (rest === "/messages") {
    await fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: null });
    return true;
  }
  if (rest === "/compactions" || rest === "/branches") {
    await fulfill(route, []);
    return true;
  }
  return false;
}

export async function openNavigationIfNeeded(page: Page) {
  if ((page.viewportSize()?.width ?? 1280) > 640) return;
  const navigation = page.locator("#workspace-navigation");
  if (await navigation.evaluate((element) => element.classList.contains("sidebar-open"))) return;
  await page.getByRole("button", { name: "Open navigation" }).first().click();
}

/**
 * Pick a project in the sidebar. After blank-session startup the trigger's
 * title is the selected project name, not "Select project". Always click the
 * menu item (even when already selected) so mobile still closes the drawer.
 */
export async function selectProject(page: Page, projectName: string) {
  await openNavigationIfNeeded(page);
  const trigger = page.locator(".sidebar-project-selector > button");
  await expect(trigger).toBeVisible();
  await trigger.click();
  await page.getByRole("menu", { name: "Projects" }).getByRole("button", { name: projectName, exact: true }).click();
}
