import { expect, test, type Route } from "@playwright/test";
import { openSession, tryFulfillBlankSessionCreate } from "./harness";
import { subscribedFrame } from "./session-feed";

const project = { id: "queue-project", name: "Queue workspace", description: "", status: "active",
  createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" };
const session = { id: "queue-session", projectId: project.id, title: "Queue session", status: "active",
  activeLeafMessageId: "m1", createdAt: "2026-08-08T00:00:00Z", updatedAt: "2026-08-08T00:00:00Z" };
const run = { id: "queue-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1,
  status: "running", requestedConfig: { toolPolicyProfileId: "builtin-tool-discuss-v1" }, effectiveConfig: {},
  createdAt: "2026-08-08T00:00:00Z" };

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("queued follow-ups can be edited, removed, or steered immediately", async ({ page }) => {
  let queueSeq = 0;
  const cancelled: string[] = [];
  const steered: Array<Record<string, unknown>> = [];
  let releaseStream: () => void = () => {};
  const streamHeld = new Promise<void>((resolve) => { releaseStream = () => resolve(); });

  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    const method = route.request().method();
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, { run });
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: "m1" });
    if (path === `/v1/sessions/${session.id}/compactions` || path === `/v1/sessions/${session.id}/branches`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/events`) return route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: run, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }),
    });
    if (path === `/v1/runs/${run.id}/inputs` && method === "POST") {
      const body = route.request().postDataJSON() as Record<string, unknown>;
      if (body.kind === "steer") {
        steered.push(body);
        return fulfill(route, { id: "steer-1", runId: run.id, kind: "steer", text: body.text, status: "queued" }, 202);
      }
      queueSeq += 1;
      return fulfill(route, { id: `q${queueSeq}`, runId: run.id, kind: "follow_up", text: String(body.text), status: "queued" }, 202);
    }
    if (method === "DELETE" && path.startsWith(`/v1/runs/${run.id}/inputs/`)) {
      const id = path.slice(`/v1/runs/${run.id}/inputs/`.length);
      cancelled.push(id);
      return fulfill(route, { id, runId: run.id, kind: "follow_up", text: "cancelled", status: "cancelled" });
    }
    if (path === `/v1/runs/${run.id}/events`) {
      await streamHeld;
      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `data: {"type":"run_succeeded","payload":{}}\n\n`,
      });
    }
    return route.abort();
  });

  await openSession(page, { projectId: project.id, sessionId: session.id });
  await expect(page.getByRole("button", { name: "Queue follow-up", exact: true })).toBeVisible();

  const textbox = page.getByRole("textbox", { name: /agent/ });
  const queue = page.locator(".queue-dock");
  const queueFollowUp = async (text: string) => {
    await textbox.fill(text);
    await page.getByRole("button", { name: "Queue follow-up", exact: true }).click();
    await expect(queue).toContainText(text);
  };

  // Edit: cancel the queued row, then restore its text to the composer.
  await queueFollowUp("edit me");
  await queue.getByRole("button", { name: "Edit queued message" }).click();
  await expect.poll(() => cancelled).toContain("q1");
  await expect(queue).toHaveCount(0);
  await expect(textbox).toHaveValue("edit me");

  // Remove: cancel the queued row without touching the composer.
  await textbox.fill("");
  await queueFollowUp("remove me");
  await queue.getByRole("button", { name: "Remove queued message" }).click();
  await expect.poll(() => cancelled).toContain("q2");
  await expect(queue).toHaveCount(0);

  // Steer now: cancel, then submit the same text as an immediate steer.
  await textbox.fill("");
  await queueFollowUp("steer me");
  await queue.getByRole("button", { name: "Steer now" }).click();
  await expect.poll(() => cancelled).toContain("q3");
  await expect.poll(() => steered.length).toBe(1);
  expect(steered[0]).toMatchObject({ kind: "steer", text: "steer me" });
  await expect(queue).toHaveCount(0);

  releaseStream();
});
