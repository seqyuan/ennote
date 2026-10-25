import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillBlankSessionCreate } from "./harness";

const now = "2026-07-28T00:00:00Z";
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
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/context-usage`) return fulfill(route, contextUsage);
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
