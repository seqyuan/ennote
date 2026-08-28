import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "attention-project", name: "Attention workspace", description: "", status: "active",
  createdAt: "2026-08-04T00:00:00Z", updatedAt: "2026-08-04T00:00:00Z" };
const sourceSession = { id: "source-session", projectId: project.id, title: "Source delegation", status: "active",
  activeLeafMessageId: "m2", createdAt: "2026-08-04T00:00:00Z", updatedAt: "2026-08-04T00:00:04Z" };
const currentSession = { id: "current-session", projectId: project.id, title: "Current session", status: "active",
  activeLeafMessageId: "m1", createdAt: "2026-08-04T00:00:00Z", updatedAt: "2026-08-04T00:00:02Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode,
  kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-08-04T00:00:00Z", updatedAt: "2026-08-04T00:00:00Z" }));

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", parts: unknown[], runId?: string) {
  return { id, sessionId: currentSession.id, parentMessageId, role, status: "complete", runId, speakerKind: role === "assistant" ? "host" : "user",
    speakerSnapshot: role === "assistant" ? { kind: "host", displayName: "Host" } : { kind: "user", displayName: "User" },
    visibility: "public", parts, createdAt: `2026-08-04T00:00:0${id.slice(-1)}Z` };
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function selectProject(page: Page) {
  await page.goto("/");
  const navigation = page.locator("#workspace-navigation");
  if ((page.viewportSize()?.width ?? 1280) <= 640) {
    await page.getByRole("button", { name: "Open navigation" }).click();
    await expect(navigation).toHaveClass(/sidebar-open/);
  }
  await page.getByTitle("Select project").first().click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
}

function commonRoute(path: string, route: Route) {
  if (path === "/v1/projects") return fulfill(route, [project]);
  if (path === "/v1/policy-profiles") return fulfill(route, policies);
  if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
  if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [currentSession, sourceSession]);
  if (path === `/v1/sessions/${currentSession.id}`) return fulfill(route, currentSession);
  if (path === `/v1/sessions/${sourceSession.id}`) return fulfill(route, sourceSession);
  if (path.startsWith("/v1/sessions/") && path.endsWith("/compactions")) return fulfill(route, []);
  if (path.startsWith("/v1/sessions/") && path.endsWith("/branches")) return fulfill(route, []);
  if (path.startsWith("/v1/sessions/") && path.endsWith("/recovery")) return fulfill(route, null);
  return null;
}

test("global attention bell shows pending items and navigates to the source session", async ({ page }) => {
  const approvalItem = { id: "att-1", projectId: project.id, sessionId: sourceSession.id,
    sourceKind: "delegation_approval", sourceId: "approval-1", sourceGeneration: 1,
    kind: "approval_required", requiresAction: true, status: "pending",
    display: { kind: "retry_budget", generation: 1 }, createdAt: "2026-08-04T00:00:03Z" };
  const completionItem = { id: "att-2", projectId: project.id, sessionId: sourceSession.id,
    sourceKind: "delegation_completion", sourceId: "handle-1", sourceGeneration: 0,
    kind: "delegation_completed", requiresAction: false, status: "pending",
    display: { kind: "completed", summary: "Background delegation completed" }, createdAt: "2026-08-04T00:00:04Z" };
  const messages = [
    message("m1", undefined, "user", [{ type: "text", text: "Hello" }]),
  ];
  let dismissedCompletion = false;
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const common = commonRoute(path, route);
    if (common) return common;
    if (path === `/v1/sessions/${currentSession.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${currentSession.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m1" });
    if (path === `/v1/attention`) {
      const url = new URL(route.request().url());
      if (url.searchParams.get("projectId") === project.id && url.searchParams.get("status") === "pending") {
        const items = dismissedCompletion ? [approvalItem] : [approvalItem, completionItem];
        return fulfill(route, { items, hasMore: false });
      }
      return fulfill(route, { items: [], hasMore: false });
    }
    if (path === `/v1/attention/${completionItem.id}/dismiss`) {
      dismissedCompletion = true;
      return fulfill(route, { status: "dismissed" });
    }
    return route.abort();
  });

  await selectProject(page);
  // The bell shows the pending count.
  await expect(page.locator(".attention-bell")).toBeVisible();
  await expect(page.locator(".attention-badge")).toHaveText("2");
  await page.locator(".attention-bell").click();
  // Action-required appears ahead of notifications.
  await expect(page.locator(".attention-row").nth(0)).toContainText("approval");
  await expect(page.locator(".attention-row").nth(1)).toContainText("Background delegation completed");

  // Notification can be dismissed.
  await page.locator(".attention-row").nth(1).locator(".attention-dismiss").click();
  await expect(page.locator(".attention-badge")).toHaveText("1");

  // Selecting the approval item navigates to the source session.
  await page.locator(".attention-row").nth(0).locator(".attention-main").click();
  await expect(page.locator("#workspace-navigation")).toContainText("Source delegation");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});
