import { expect, test, type Page, type Route } from "@playwright/test";
import { selectProject, tryFulfillHarnessRoutes } from "./harness";

const project = { id: "project-submit-failure", name: "Failure project", description: "", status: "active",
  createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" };
const session = { id: "session-submit-failure", projectId: project.id, title: "Failure session", status: "active",
  createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" };
const policies = [
  { id: "builtin-tool-discuss-v3", name: "Discuss", kind: "tool", version: 3, config: { mode: "discuss" },
    status: "active", createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" },
];

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

/** Delay the rejection so the optimistic bubble and the sending status are observable. */
const REJECT_AFTER_MS = 500;

async function mockApp(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false });
    if (path === `/v1/sessions/${session.id}/compactions` || path === `/v1/sessions/${session.id}/branches`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/invocations`) {
      await new Promise(resolve => setTimeout(resolve, REJECT_AFTER_MS));
      return route.fulfill({ status: 500, contentType: "application/json",
        body: JSON.stringify({ error: { message: "Worker rejected the turn" } }) });
    }
    return route.abort();
  });
}

test("a rejected submit restores the draft and leaves no ghost turn", async ({ page }) => {
  const draft = "Draft that has to survive the failure";
  await mockApp(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByText(session.title, { exact: true }).click();

  const composer = page.getByRole("textbox", { name: "Message the agent" });
  await expect(composer).toBeVisible();
  await composer.fill(draft);
  await page.getByRole("button", { name: "Send", exact: true }).click();

  // While the submit is in flight the composer is empty and the optimistic
  // bubble plus the "sending" status carry the turn.
  const messages = page.getByTestId("chat-messages");
  await expect(messages.getByText(draft, { exact: true })).toBeVisible();
  await expect(page.getByText("Sending…")).toBeVisible();

  // On failure the three residues must be gone: text back in the composer, no
  // ghost bubble in the timeline, and the status released back to idle.
  await expect(page.getByRole("main").getByRole("alert")).toContainText("Worker rejected the turn");
  await expect(composer).toHaveValue(draft);
  await expect(messages.getByText(draft, { exact: true })).toHaveCount(0);
  await expect(page.getByText("Sending…")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Send", exact: true })).toBeVisible();
});
