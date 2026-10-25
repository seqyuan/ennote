import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillBlankSessionCreate } from "./harness";

const project = { id: "selection-project", name: "Selection project", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const session = { id: "selection-session", projectId: project.id, title: "Selection session", status: "active", activeLeafMessageId: "m2", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:02Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" }));

const messages = [
  {
    id: "m1", sessionId: session.id, parentMessageId: undefined, role: "user", status: "complete",
    parts: [{ type: "text", text: "Summarize the marker matrix." }], createdAt: "2026-07-28T00:00:01Z",
  },
  {
    id: "m2", sessionId: session.id, parentMessageId: "m1", role: "assistant", status: "complete",
    parts: [{ type: "text", text: "The matrix is valid and ready for review." }], createdAt: "2026-07-28T00:00:02Z",
  },
];

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function openSurface(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m2" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    return route.abort();
  });
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.locator("[data-turn-id]")).toHaveCount(1);
}

test("selecting text offers quote, copy, and explain without losing the selection", async ({ page }) => {
  await openSurface(page);
  await page.locator("[data-turn-id] .assistant-step .markdown-body p").first().selectText();

  const toolbar = page.getByRole("toolbar", { name: "Selection actions" });
  await expect(toolbar).toBeVisible();

  await toolbar.getByRole("button", { name: "Quote in composer" }).click();
  const input = page.getByRole("textbox", { name: "Message" });
  await expect(input).toBeFocused();
  await expect(input).toHaveValue(/> The matrix is valid and ready for review\./);
  await expect(toolbar).toHaveCount(0);
});

test("Escape closes only the selection toolbar", async ({ page }) => {
  await openSurface(page);
  await page.locator("[data-turn-id] .assistant-step .markdown-body p").first().selectText();
  await expect(page.getByRole("toolbar", { name: "Selection actions" })).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(page.getByRole("toolbar", { name: "Selection actions" })).toHaveCount(0);
  await expect(page.locator("[data-turn-id]")).toBeVisible();
});
