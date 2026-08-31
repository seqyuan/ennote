import { expect, test, type Page, type Route } from "@playwright/test";
import { selectProject, tryFulfillBlankSessionCreate } from "./harness";

const project = { id: "project-permission", name: "Permission project", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const session = { id: "session-permission", projectId: project.id, title: "Permission session", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const provider = { id: "provider", name: "Test provider", providerType: "openai-compatible", baseUrl: "https://provider.test/v1", credentialRef: "env:TEST_KEY", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const policies = [
  { id: "builtin-tool-discuss-v1", name: "Discuss", kind: "tool", version: 1, config: { mode: "discuss" }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" },
  { id: "builtin-tool-ask-v1", name: "Ask", kind: "tool", version: 1, config: { mode: "ask" }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" },
  { id: "builtin-tool-auto-v1", name: "Auto", kind: "tool", version: 1, config: { mode: "auto" }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" },
];

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockApp(page: Page, onTurn?: (body: Record<string, unknown>) => void) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/invocations`) {
      onTurn?.(route.request().postDataJSON());
      return fulfill(route, { turnId: "turn", userMessageId: "message", existing: false, run: { id: "run", sessionId: session.id, runKind: "agent", attempt: 1, status: "queued" } }, 202);
    }
    if (path === "/v1/runs/run/events") {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: 'data: {"type":"run_succeeded","payload":{}}\n\n' });
    }
    return route.abort();
  });
}

test("each turn freezes the selected permission profile", async ({ page }) => {
  let turnBody: Record<string, unknown> | undefined;
  await mockApp(page, body => { turnBody = body; });
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByText(session.title, { exact: true }).click();

  await page.getByRole("button", { name: /Permission mode/ }).click();
  await expect(page.getByRole("menuitemradio", { name: "Discuss", exact: true })).toHaveAttribute("aria-checked", "true");
  await page.getByRole("menuitemradio", { name: "Ask", exact: true }).click();
  await page.getByRole("textbox", { name: "Message the agent" }).fill("run this turn");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect.poll(() => turnBody).toBeTruthy();
  expect(turnBody).toMatchObject({ text: "run this turn", target: { kind: "host" },
    config: { toolPolicyProfileId: "builtin-tool-ask-v1" } });
});

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("permission controls and Send remain inside the viewport", async ({ page }) => {
    await mockApp(page);
    await page.goto("/");
    await page.getByRole("button", { name: "Open navigation" }).click();
    await selectProject(page, project.name);
    await page.getByRole("button", { name: "Open navigation" }).click();
    await page.getByText(session.title, { exact: true }).click();
    await expect(page.getByRole("button", { name: /Permission mode/ })).toBeVisible();
    await expect(page.getByRole("button", { name: "Send", exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  });
});
