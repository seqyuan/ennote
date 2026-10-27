import { expect, test, type Page, type Route } from "@playwright/test";
import { openSession, tryFulfillBlankSessionCreate } from "./harness";
import { subscribedFrame } from "./session-feed";

const project = { id: "resume-project", name: "Resume workspace", description: "", status: "active",
  createdAt: "2026-10-26T00:00:00Z", updatedAt: "2026-10-26T00:00:00Z" };
const session = { id: "resume-session", projectId: project.id, title: "Resume session", status: "active", mode: "hosted",
  activeLeafMessageId: "m1", createdAt: "2026-10-26T00:00:00Z", updatedAt: "2026-10-26T00:00:00Z" };
const run = { id: "resume-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1,
  status: "running", speakerSnapshot: { kind: "host", displayName: "Host" },
  requestedConfig: { toolPolicyProfileId: "builtin-tool-discuss-v1" }, effectiveConfig: {},
  createdAt: "2026-10-26T00:00:00Z" };

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("a reconnecting run stream resumes from Last-Event-ID and appends instead of resetting", async ({ page }) => {
  const cursors: Array<string | undefined> = [];
  let streamCalls = 0;

  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, { run });
    if (path === `/v1/sessions/${session.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: run, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }) });
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: "m1" });
    if (path === `/v1/sessions/${session.id}/compactions` || path === `/v1/sessions/${session.id}/branches`) return fulfill(route, []);
    if (path === `/v1/sessions/${session.id}/context-usage` || path === `/v1/sessions/${session.id}/recovery`) return fulfill(route, null);
    if (path === `/v1/projects/${project.id}/prompt-templates`) return fulfill(route, { templates: [], diagnostics: [] });
    if (path === `/v1/runs/${run.id}/events`) {
      streamCalls += 1;
      cursors.push(route.request().headers()["last-event-id"]);
      if (streamCalls === 1) {
        // Drop the connection after one live frame and no terminal event.
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: [
          `id: 1\nevent: live\ndata: {"type":"text_delta","payload":{"iteration":1,"text":"first half"}}\n\n`,
        ].join("") });
      }
      if (streamCalls === 2) {
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: [
          `id: 2\nevent: live\ndata: {"type":"text_delta","payload":{"iteration":1,"text":" second half"}}\n\n`,
        ].join("") });
      }
      // Keep the run open; the transient transcript must survive reconnects.
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: `id: ${streamCalls + 1}\n: keepalive\n\n` });
    }
    return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { message: path } }) });
  });

  await openSession(page, { projectId: project.id, sessionId: session.id });

  await expect(page.getByText("first half second half", { exact: true })).toBeVisible({ timeout: 15000 });
  expect(streamCalls).toBeGreaterThanOrEqual(2);
  expect(cursors[0]).toBeUndefined();
  expect(cursors[1]).toBe("1");
});
