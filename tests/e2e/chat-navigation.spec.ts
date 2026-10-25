import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillBlankSessionCreate } from "./harness";
import { subscribedFrame } from "./session-feed";

const project = { id: "rail-project", name: "Rail workspace", description: "", status: "active",
  createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" };
const session = { id: "rail-session", projectId: project.id, title: "Rail session", status: "active",
  activeLeafMessageId: "a8", activeBranchId: "main", createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:08Z" };
const branch = { id: "main", sessionId: session.id, label: "Main", leafMessageId: "a8", messageCount: 16,
  active: true, createdAt: session.createdAt, updatedAt: session.updatedAt };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool",
  version: 1, config: { mode }, status: "active", createdAt: session.createdAt, updatedAt: session.updatedAt }));

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", text: string, n: number) {
  return {
    id, sessionId: session.id, parentMessageId, role, status: "complete",
    parts: [{ type: "text", text }],
    createdAt: `2026-08-08T00:00:0${n}Z`,
  };
}

const messages = Array.from({ length: 8 }, (_, index) => {
  const n = index + 1;
  const user = message(`u${n}`, n === 1 ? undefined : `a${n - 1}`, "user", `Question ${n}: inspect this result and summarize the finding.`, n);
  const assistant = message(`a${n}`, `u${n}`, "assistant",
    `Answer ${n}: the matrix is valid.\n\n${"This paragraph keeps the transcript tall enough to scroll. ".repeat(4)}`, n);
  return [user, assistant];
}).flat();

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockRail(page: Page) {
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "a8" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/branches`) return fulfill(route, [branch]);
    if (path === `/v1/sessions/${session.id}/events`) return route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: null, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }),
    });
    if (path === "/v1/attention") return fulfill(route, []);
    return route.abort();
  });
}

test("turn rail jumps between turns and the latest pill returns to the floor", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 700 });
  await mockRail(page);
  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.getByTestId("chat-messages")).toBeVisible();

  const rail = page.getByRole("navigation", { name: "Conversation turns" });
  await expect(rail).toBeVisible();
  await expect(rail.getByRole("button", { name: /Go to turn/ })).toHaveCount(8);

  // Scroll away from the floor: the latest pill appears.
  await page.getByTestId("chat-messages").evaluate((element) => { element.scrollTop = 0; });
  const latest = page.getByRole("button", { name: "Jump to latest" });
  await expect(latest).toBeVisible();
  await latest.click();
  await expect(latest).toHaveCount(0);

  // Clicking a marker lands on that turn and marks it active.
  const third = rail.getByRole("button", { name: /Go to turn 3/ });
  await third.click();
  await expect(third).toHaveAttribute("aria-current", "true");
  await third.hover();
  await expect(page.getByRole("tooltip")).toContainText("Question 3");
});

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("the rail is hidden and the latest pill still works", async ({ page }) => {
    await mockRail(page);
    await openSession(page, { projectId: project.id, sessionId: session.id });
    await expect(page.getByRole("navigation", { name: "Conversation turns" })).toBeHidden();
    await page.getByTestId("chat-messages").evaluate((element) => { element.scrollTop = 0; });
    await expect(page.getByRole("button", { name: "Jump to latest" })).toBeVisible();
  });
});
