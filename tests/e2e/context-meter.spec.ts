import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillHarnessRoutes } from "./harness";

const now = "2026-07-28T00:00:00Z";
// Session aggregates: they render inside the same panel as the context breakdown,
// which is why the composer has no stats strip of its own any more.
const sessionStats = {
  turns: 3, steps: 12, llmMs: 10900, toolMs: 88000,
  ttftSteps: 4, ttftMs: 1600, decodeMs: 20000, decodeTokens: 2920,
  uncachedInputTokens: 5500, cacheReadTokens: 27300, cacheWriteTokens: 0,
  outputTokens: 1300, reasoningTokens: 0,
};
const project = { id: "meter-project", name: "Meter project", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "meter-session", projectId: project.id, title: "Meter session", status: "active", activeLeafMessageId: "m2", createdAt: now, updatedAt: now };

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", text: string) {
  return {
    id, sessionId: session.id, parentMessageId, role, status: "complete",
    speakerKind: role === "user" ? "user" : "host",
    speakerSnapshot: role === "user" ? { kind: "user", displayName: "You" } : { kind: "host", displayName: "Host" },
    visibility: "public",
    parts: [{ type: "text", text }], createdAt: now,
  };
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mock(page: Page, contextUsage: unknown) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/context-usage`) return fulfill(route, contextUsage);
    if (path === `/v1/sessions/${session.id}/stats`) return fulfill(route, sessionStats);
    if (path === `/v1/sessions/${session.id}/messages`) {
      return fulfill(route, {
        messages: [message("m1", undefined, "user", "hello"), message("m2", "m1", "assistant", "the reply")],
        hasMore: false, activeLeafMessageId: "m2",
      });
    }
    return route.abort();
  });
  await openSession(page, { projectId: project.id, sessionId: session.id });
}

test("expands the seven-category breakdown", async ({ page }) => {
  await mock(page, {
    contextWindow: 100000,
    projectedTokens: 40000,
    systemTokens: 8000,
    toolsTokens: 6000,
    messageTokens: 26000,
    categoriesVersion: 1,
    categories: [
      { key: "system", tokens: 4000 },
      { key: "role", tokens: 3000 },
      { key: "rules", tokens: 2000 },
      { key: "skills", tokens: 1000 },
      { key: "tools", tokens: 6000 },
      { key: "reminders", tokens: 2000 },
      { key: "conversation", tokens: 22000 },
    ],
  });

  await page.getByRole("button", { name: "40% of context used" }).click();
  const panel = page.getByRole("dialog", { name: "of context used" });
  await expect(panel).toBeVisible();
  for (const label of ["System prompt", "Role", "Project rules", "Skills", "Tools", "Reminders", "Conversation"]) {
    await expect(panel.getByText(label, { exact: true })).toBeVisible();
  }
});

test("falls back to the three legacy totals for old sessions", async ({ page }) => {
  await mock(page, {
    contextWindow: 100000,
    projectedTokens: 40000,
    systemTokens: 8000,
    toolsTokens: 6000,
    messageTokens: 26000,
  });

  await page.getByRole("button", { name: "40% of context used" }).click();
  const panel = page.getByRole("dialog", { name: "of context used" });
  await expect(panel.getByText("Messages", { exact: true })).toBeVisible();
  await expect(panel.getByText("Project rules", { exact: true })).toHaveCount(0);
});

test("shows the session figures in the same panel as the breakdown", async ({ page }) => {
  await mock(page, {
    contextWindow: 100000,
    projectedTokens: 40000,
    systemTokens: 8000,
    toolsTokens: 6000,
    messageTokens: 26000,
  });

  // Nothing numeric is printed under the composer: the ring is the only summary.
  await expect(page.locator(".stats-line")).toHaveCount(0);
  await expect(page.getByText("3 turns · 12 steps")).toHaveCount(0);

  await page.getByRole("button", { name: "40% of context used" }).click();
  const panel = page.getByRole("dialog", { name: "of context used" });
  await expect(panel.getByText("Session figures", { exact: true })).toBeVisible();
  await expect(panel.getByText("3 turns · 12 steps", { exact: true })).toBeVisible();
  await expect(panel.getByText("Cache hit 83%", { exact: true })).toBeVisible();
  await expect(panel.getByText("Input 32.8K tok · Output 1.3K tok", { exact: true })).toBeVisible();
});
