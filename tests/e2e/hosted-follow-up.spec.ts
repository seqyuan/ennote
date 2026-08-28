import { expect, test, type Page, type Route } from "@playwright/test";
import { subscribedFrame } from "./session-feed";

const project = { id: "followup-project", name: "Follow-up workspace", description: "", status: "active",
  createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" };
const session = { id: "followup-session", projectId: project.id, title: "Follow-up session", status: "active",
  activeLeafMessageId: "m1", createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" };
const run = { id: "followup-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1,
  status: "running", requestedConfig: { toolPolicyProfileId: "builtin-tool-discuss-v1" }, effectiveConfig: {},
  createdAt: "2026-08-08T00:00:00Z" };

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function selectSession(page: Page) {
  await page.goto("/");
  await page.getByTitle("Select project").first().click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
}

test("queues a follow-up while a run is active and clears it once consumed", async ({ page }) => {
  const queuedBodies: Array<Record<string, unknown>> = [];
  // The first events connection is held open until the test queues a
  // follow-up, so the composer never sees a reconnecting state.
  let releaseFirstStream: () => void = () => {};
  const firstStreamHeld = new Promise<void>(resolve => { releaseFirstStream = () => resolve(); });
  let firstStreamReleased = false;
  let sentConsumed = false;
  let runFinished = false;

  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, { run });
    if (path === `/v1/sessions/${session.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: runFinished ? null : run, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }) });
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: "m1" });
    if (path === `/v1/sessions/${session.id}/compactions` || path === `/v1/sessions/${session.id}/branches`) return fulfill(route, []);
    if (path === `/v1/runs/${run.id}/inputs` && route.request().method() === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      queuedBodies.push(body);
      return fulfill(route, { id: "q1", runId: run.id, kind: "follow_up", text: String(body.text), status: "queued" }, 202);
    }
    if (path === `/v1/runs/${run.id}/events`) {
      if (!firstStreamReleased) {
        firstStreamReleased = true;
        await firstStreamHeld;
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: [
          `event: live\ndata: {"type":"text_delta","payload":{"iteration":1,"text":"working"}}\n\n`,
          `data: {"type":"follow_up_consumed","payload":{"seq":1}}\n\n`,
        ].join("") });
      }
      if (!sentConsumed) {
        sentConsumed = true;
        return route.fulfill({ status: 200, contentType: "text/event-stream",
          body: `data: {"type":"follow_up_consumed","payload":{"seq":1}}\n\n` });
      }
      runFinished = true;
      return route.fulfill({ status: 200, contentType: "text/event-stream",
        body: `data: {"type":"run_succeeded","payload":{}}\n\n` });
    }
    return route.abort();
  });

  await selectSession(page);
  // The run is active; queue a follow-up without interrupting it.
  await page.getByRole("textbox", { name: /agent/ }).fill("Summarize what you found");
  await page.getByRole("button", { name: "Queue follow-up", exact: true }).click();
  const queue = page.locator(".composer-followup-queue");
  await expect(queue).toContainText("Queued: Summarize what you found");
  await expect.poll(() => queuedBodies.length).toBeGreaterThan(0);
  expect(queuedBodies[0]).toMatchObject({ kind: "follow_up", text: "Summarize what you found" });
  expect(queuedBodies[0]).toHaveProperty("clientRequestId");

  // Release the held stream: the run streams output and the worker reports the
  // queued follow-up as consumed — the queue chip clears while the run lives on.
  releaseFirstStream();
  await expect(page.getByText("working", { exact: true })).toBeVisible({ timeout: 8000 });
  await expect(queue).toHaveCount(0, { timeout: 10000 });
  await expect(page.getByRole("button", { name: "Queue follow-up" })).toBeVisible();

  // The run then finishes on the next reconnect and the composer returns to send.
  await expect(page.getByRole("button", { name: "Queue follow-up" })).toHaveCount(0, { timeout: 20000 });
  await expect(page.getByRole("button", { name: "Send", exact: true })).toBeVisible();
  await expect(page.getByRole("textbox", { name: /agent/ })).toHaveValue("");
});
