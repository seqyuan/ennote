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
 * Routes every spec has to answer for the App to behave as it does against a
 * real Worker. Call it first in an api/worker route handler and return when it
 * reports handled; anything it declines is the subject under test.
 *
 * Two things live here:
 *
 * - The liveness probe. The App asks `/v1/health/live` whether the Worker is
 *   there and reads the answer from X-Ennote-Worker-Instance. A handler that
 *   ends in `route.abort()` would turn that probe into a network failure and put
 *   the App into read-only for the rest of the test, so the harness answers it
 *   the way a real Worker does. offline-read-only.spec.ts is the one spec that
 *   intercepts this path itself, on purpose.
 * - Startup auto-connect, which POSTs a blank session when the project has no
 *   unused blank. Specs that return a session *list* for both GET and POST make
 *   that call throw "Failed to create session". Handle POST (and later fetches
 *   of the created row) here; leave GET list to the spec.
 */
export async function tryFulfillHarnessRoutes(route: Route): Promise<boolean> {
  const method = route.request().method();
  const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
  if (path === "/v1/health/live") {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { "X-Ennote-Worker-Instance": "e2e-worker" },
      body: JSON.stringify({ data: { status: "live" } }),
    });
    return true;
  }
  const create = path.match(/^\/v1\/projects\/([^/]+)\/sessions$/);
  if (method === "POST" && create) {
    await fulfill(route, blankSession(create[1]), 201);
    return true;
  }
  // The sidebar asks once per Project which Sessions own an active Run. Nothing is
  // running in a mocked spec unless the spec says so, and the selected Session's
  // marker does not depend on this answer.
  if (/^\/v1\/projects\/[^/]+\/active-runs$/.test(path)) {
    await fulfill(route, { sessionIds: [] });
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
 * Open a Session through the app's own restore path, and wait for it to load.
 *
 * Selecting a project starts the connect hook on an async reuse-or-create-blank
 * flow, and that flow's selection can land after a spec's own sidebar click and
 * overwrite it. The spec then asserts against a blank session — which shows as
 * "No session" and times out, intermittently and worse under parallel load.
 * Seeding the stored project and session makes the hook restore them on its first
 * ready tick instead, so there is nothing to race. It is also the path a
 * returning user takes, so the coverage stays honest.
 *
 * Use this where opening a session is setup. A spec that tests opening a session
 * itself (session-history) must keep clicking the row.
 */
export async function openSession(
  page: Page,
  input: { projectId: string; sessionId: string },
): Promise<void> {
  await page.addInitScript(([projectId, sessionId]) => {
    try {
      window.localStorage.setItem("ennote-selected-project", projectId);
      window.localStorage.setItem("ennote-selected-session", sessionId);
    } catch {
      /* storage unavailable */
    }
  }, [input.projectId, input.sessionId]);
  await page.goto("/");
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
