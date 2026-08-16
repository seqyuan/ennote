import { expect, test, type Page, type Route } from "@playwright/test";
import { subscribedFrame } from "./session-feed";

const project = { id: "ms-project", name: "Multi-session workspace", description: "", status: "active",
  createdAt: "2026-08-16T00:00:00Z", updatedAt: "2026-08-16T00:00:00Z" };

function session(id: string, title: string) {
  return { id, projectId: project.id, title, status: "active", mode: "hosted",
    activeLeafMessageId: `${id}-message`, createdAt: "2026-08-16T00:00:00Z", updatedAt: "2026-08-16T00:00:00Z" };
}

function message(id: string, sessionId: string, text: string) {
  return { id, sessionId, role: "user", status: "complete", speakerKind: "user",
    speakerSnapshot: { kind: "user", displayName: "You" }, addresseeKind: "host",
    visibility: "public", parts: [{ type: "text", text }], createdAt: "2026-08-16T00:00:00Z" };
}

function run(id: string, sessionId: string) {
  return { id, turnId: `${id}-turn`, sessionId, runKind: "agent", attempt: 1, status: "running",
    requestedConfig: {}, effectiveConfig: {}, createdAt: "2026-08-16T00:00:00Z" };
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function selectSession(page: Page, title: string) {
  await page.getByRole("button", { name: title, exact: true }).click();
}

test("two sessions keep independent resident stores when switching between them", async ({ page }) => {
  const sessionA = session("session-a", "Session Alpha");
  const sessionB = session("session-b", "Session Beta");
  const runA = run("run-a", sessionA.id);
  const runB = run("run-b", sessionB.id);
  const messageFetches: Record<string, number> = { a: 0, b: 0 };

  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles" || path === "/v1/model-profiles" || path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [sessionA, sessionB]);
    if (path === `/v1/sessions/${sessionA.id}`) return fulfill(route, sessionA);
    if (path === `/v1/sessions/${sessionB.id}`) return fulfill(route, sessionB);
    if (path === `/v1/sessions/${sessionA.id}/messages`) { messageFetches.a += 1; return fulfill(route, { messages: [message("am", sessionA.id, "Alpha message")], hasMore: false, activeLeafMessageId: `${sessionA.id}-message` }); }
    if (path === `/v1/sessions/${sessionB.id}/messages`) { messageFetches.b += 1; return fulfill(route, { messages: [message("bm", sessionB.id, "Beta message")], hasMore: false, activeLeafMessageId: `${sessionB.id}-message` }); }
    if (path === `/v1/sessions/${sessionA.id}/compactions` || path === `/v1/sessions/${sessionB.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${sessionA.id}/branches` || path === `/v1/sessions/${sessionB.id}/branches`) return fulfill(route, []);
    if (path === `/v1/sessions/${sessionA.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: runA, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }) });
    if (path === `/v1/sessions/${sessionB.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: runB, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }) });
    if (path === `/v1/runs/${runA.id}/events` || path === `/v1/runs/${runB.id}/events`) {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: "" });
    }
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    return route.abort();
  });

  await page.goto("/");
  await page.getByTitle("Select project").first().click();
  await page.getByText(project.name, { exact: true }).click();

  // Open A: its message and active run are visible.
  await selectSession(page, sessionA.title);
  await expect(page.getByText("Alpha message", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Queue follow-up", exact: true })).toBeVisible();

  // Switch to B: its message and active run are visible, A's are not.
  await selectSession(page, sessionB.title);
  await expect(page.getByText("Beta message", { exact: true })).toBeVisible();
  await expect(page.getByText("Alpha message", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Queue follow-up", exact: true })).toBeVisible();

  // Switch back to A: the resident store restores its message and run without
  // re-fetching history (the store is not destroyed on switch-away).
  await selectSession(page, sessionA.title);
  await expect(page.getByText("Alpha message", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Queue follow-up", exact: true })).toBeVisible();

  // Each session's history was fetched exactly once (no switch-back re-fetch).
  expect(messageFetches.a).toBe(1);
  expect(messageFetches.b).toBe(1);
});

test("an off-screen session converges to a later snapshot and refreshed history from its own feed", async ({ page }) => {
  const sessionA = session("session-a", "Session Alpha");
  const sessionB = session("session-b", "Session Beta");
  const runA = run("run-a", sessionA.id);
  const runB = run("run-b", sessionB.id);
  let runAFinished = false;

  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles" || path === "/v1/model-profiles" || path === "/v1/provider-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [sessionA, sessionB]);
    if (path === `/v1/sessions/${sessionA.id}`) return fulfill(route, sessionA);
    if (path === `/v1/sessions/${sessionB.id}`) return fulfill(route, sessionB);
    if (path === `/v1/sessions/${sessionA.id}/messages`) return fulfill(route, {
      messages: runAFinished ? [message("am", sessionA.id, "Alpha finished")] : [message("am0", sessionA.id, "Alpha running")],
      hasMore: false, activeLeafMessageId: `${sessionA.id}-message`,
    });
    if (path === `/v1/sessions/${sessionB.id}/messages`) return fulfill(route, { messages: [message("bm", sessionB.id, "Beta message")], hasMore: false, activeLeafMessageId: `${sessionB.id}-message` });
    if (path === `/v1/sessions/${sessionA.id}/compactions` || path === `/v1/sessions/${sessionB.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/sessions/${sessionA.id}/events`) {
      const body = runAFinished
        ? [
            subscribedFrame({ activeRun: null, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }),
            `id: 1\ndata: ${JSON.stringify({ type: "message_committed", runId: runA.id, firstSeq: 1, lastSeq: 1 })}\n\n`,
            `id: 2\ndata: ${JSON.stringify({ type: "run_succeeded", runId: runA.id, payload: {} })}\n\n`,
          ].join("")
        : subscribedFrame({ activeRun: runA, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false });
      return route.fulfill({ status: 200, contentType: "text/event-stream", body });
    }
    if (path === `/v1/sessions/${sessionB.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: runB, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }) });
    if (path === `/v1/runs/${runA.id}/events` || path === `/v1/runs/${runB.id}/events`) {
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: "" });
    }
    if (path === "/v1/roles") return fulfill(route, { items: [], nextCursor: "" });
    return route.abort();
  });

  await page.goto("/");
  await page.getByTitle("Select project").first().click();
  await page.getByText(project.name, { exact: true }).click();

  await selectSession(page, sessionA.title);
  await expect(page.getByText("Alpha running", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Queue follow-up", exact: true })).toBeVisible();

  // Switch to B; A's store keeps its feed open off-screen.
  await selectSession(page, sessionB.title);
  await expect(page.getByText("Beta message", { exact: true })).toBeVisible();

  // A's run finishes while we are on B (its feed converges off-screen).
  runAFinished = true;

  // Switch back to A: it converges to the finished snapshot and refreshed history.
  await selectSession(page, sessionA.title);
  await expect(page.getByRole("button", { name: "Send", exact: true })).toBeVisible({ timeout: 10000 });
  await expect(page.getByText("Alpha finished", { exact: true })).toBeVisible({ timeout: 10000 });
});
