import { expect, test, type Page, type Route } from "@playwright/test";
import { selectProject, tryFulfillBlankSessionCreate } from "./harness";
// A configured provider suppresses the first-run Models settings auto-open.
const settingsProvider = { id: "settings-provider", name: "Provider", providerType: "openai-compatible", baseUrl: "https://example.test", credentialConfigured: true, status: "active", createdAt: "2026-07-30T00:00:00Z", updatedAt: "2026-07-30T00:00:00Z" };

const project = { id: "phase2-project", name: "Cell atlas", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const activeSessions = [
  { id: "marker-session", projectId: project.id, title: "Marker review", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:03Z" },
  { id: "qc-session", projectId: project.id, title: "QC notes", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:02Z" },
];
const archivedSessions = [
  { id: "old-session", projectId: project.id, title: "Prior integration", status: "archived", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:01Z" },
];
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" }));

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockPhase2(page: Page) {
  const sessions = [...activeSessions, ...archivedSessions].map(session => ({ ...session }));
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [settingsProvider]);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) {
      const status = url.searchParams.get("status") ?? "active";
      const query = (url.searchParams.get("q") ?? "").toLocaleLowerCase();
      return fulfill(route, sessions.filter(session => session.status === status && session.title.toLocaleLowerCase().includes(query)));
    }
    const transition = path.match(/^\/v1\/sessions\/([^/]+)\/(archive|restore)$/);
    if (transition && route.request().method() === "POST") {
      const session = sessions.find(item => item.id === transition[1]);
      if (!session) return fulfill(route, { code: "not_found", message: "not found" }, 404);
      session.status = transition[2] === "archive" ? "archived" : "active";
      return fulfill(route, session);
    }
    const detail = path.match(/^\/v1\/sessions\/([^/]+)$/);
    if (detail) return fulfill(route, sessions.find(item => item.id === detail[1]));
    if (path.endsWith("/active-run")) return fulfill(route, null);
    if (path.endsWith("/messages")) return fulfill(route, { messages: [], hasMore: false });
    if (path.endsWith("/compactions")) return fulfill(route, []);
    return route.abort();
  });
}

async function openWorkspace(page: Page) {
  await page.goto("/");
  await selectProject(page, project.name);
}

test("searches active Sessions and supports archive and restore lifecycle actions", async ({ page }) => {
  await mockPhase2(page);
  await openWorkspace(page);
  // The search field is a collapsed affordance: expand it first.
  await page.getByRole("button", { name: "Search sessions", exact: true }).click();
  const search = page.getByRole("textbox", { name: "Search sessions" });
  await search.fill("marker");
  await expect(page.getByRole("button", { name: "Marker review", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "QC notes", exact: true })).toHaveCount(0);
  await search.clear();
  await expect(page.getByRole("button", { name: "QC notes", exact: true })).toBeVisible();

  // Row actions are hover-revealed.
  await page.getByRole("button", { name: "Marker review", exact: true }).hover();
  await page.getByRole("button", { name: "Actions for Marker review" }).click();
  await page.getByRole("menuitem", { name: "Archive session" }).click();
  await expect(page.getByRole("button", { name: "Marker review", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Show archived sessions" }).click();
  await expect(page.getByRole("button", { name: "Marker review", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Prior integration", exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Marker review", exact: true }).hover();
  await page.getByRole("button", { name: "Actions for Marker review" }).click();
  await page.getByRole("menuitem", { name: "Restore session" }).click();
  await expect(page.getByText("Restored Marker review", { exact: true })).toBeAttached();
  await expect(page.getByLabel("Archived sessions in Cell atlas").getByRole("button", { name: "Marker review", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Marker review", exact: true })).toBeVisible();
});

test("Settings tabs use arrow navigation, retain Chat, and restore focus", async ({ page }) => {
  await mockPhase2(page);
  await openWorkspace(page);
  await page.getByRole("button", { name: "Marker review", exact: true }).click();
  await page.evaluate(() => { (window as typeof window & { retainedChat?: Element | null }).retainedChat = document.querySelector(".chat-area"); });

  const settingsButton = page.getByRole("button", { name: "Settings", exact: true });
  await settingsButton.click();
  await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();
  // The trigger opens on the first nav row (General); onboarding targets a
  // section explicitly, so select Models for the focus and arrow checks.
  const models = page.getByRole("tab", { name: "Models" });
  await models.click();
  await expect(models).toHaveAttribute("aria-selected", "true");
  const closeSettings = page.getByRole("button", { name: "Close settings" });
  await closeSettings.focus();
  // Shift+Tab wraps to the previous focusable in DOM order: the last nav
  // cell (the rail precedes the content column, dsh layout).
  await closeSettings.press("Shift+Tab");
  await expect(page.getByRole("tab", { name: "Skills" })).toBeFocused();
  await page.getByRole("tab", { name: "Skills" }).press("Tab");
  await expect(closeSettings).toBeFocused();
  await models.press("ArrowRight");
  await expect(page.getByRole("tab", { name: "Policies" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "Policies" }).press("End");
  await expect(page.getByRole("tab", { name: "Skills" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("tab", { name: "Context & session" }).click();
  await expect(page.getByLabel("Default model")).toBeVisible();
  expect(await page.evaluate(() => (window as typeof window & { retainedChat?: Element | null }).retainedChat === document.querySelector(".chat-area"))).toBe(true);

  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Settings" })).toHaveCount(0);
  await expect(settingsButton).toBeFocused();
  expect(await page.evaluate(() => (window as typeof window & { retainedChat?: Element | null }).retainedChat === document.querySelector(".chat-area"))).toBe(true);
});

test.describe("mobile drawer", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("uses one modal navigation DOM and restores trigger focus on Escape and selection", async ({ page }) => {
    await mockPhase2(page);
    await page.goto("/");
    const trigger = page.getByRole("button", { name: "Open navigation" });
    await trigger.click();
    await expect(page.getByRole("dialog", { name: "Navigation" })).toBeVisible();
    await expect(page.locator(".sidebar")).toHaveCount(1);
    await expect(page.locator(".workspace-content")).toHaveAttribute("inert", "");
    const closeNavigation = page.getByRole("button", { name: "Close navigation" });
    await closeNavigation.focus();
    await closeNavigation.press("Shift+Tab");
    await expect(page.getByRole("button", { name: "Settings", exact: true })).toBeFocused();
    await page.getByRole("button", { name: "Settings", exact: true }).press("Tab");
    await expect(closeNavigation).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Navigation" })).not.toBeVisible();
    await expect(trigger).toBeFocused();

    await trigger.click();
    await selectProject(page, project.name);
    await expect(trigger).toBeFocused();
    await trigger.click();
    await page.getByRole("button", { name: "Marker review", exact: true }).click();
    await expect(trigger).toBeFocused();
    await expect(page.locator(".sidebar")).toHaveCount(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  });
});
