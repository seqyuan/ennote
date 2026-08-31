import { expect, test, type Page, type Route } from "@playwright/test";
import { selectProject, tryFulfillBlankSessionCreate } from "./harness";
import { subscribedFrame } from "./session-feed";

const now = "2026-07-28T00:00:00Z";
const project = { id: "model-project", name: "Model project", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "model-session", projectId: project.id, title: "Model attribution", status: "active", mode: "hosted", activeLeafMessageId: "m2", createdAt: now, updatedAt: now };
const provider = { id: "provider", name: "anthropic", providerType: "openai-compatible", baseUrl: "https://example.test", apiKey: "test", status: "active", createdAt: now, updatedAt: now };
const model = { id: "mp-1", providerId: provider.id, modelName: "claude-sonnet-4", displayName: "Claude Sonnet 4", contextWindow: 32000, maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true, supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default", "high"], isDefault: true, status: "active", createdAt: now, updatedAt: now };

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", text: string) {
  return {
    id, sessionId: session.id, parentMessageId, role, status: "complete",
    speakerKind: role === "user" ? "user" : "host",
    speakerSnapshot: role === "user" ? { kind: "user", displayName: "You" } : { kind: "host", displayName: "Host" },
    modelProfileId: role === "assistant" ? model.id : undefined,
    apiModel: role === "assistant" ? model.modelName : undefined,
    addresseeKind: role === "user" ? "host" : undefined,
    visibility: "public",
    parts: [{ type: "text", text }], createdAt: now,
  };
}

async function mock(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    let data: unknown;
    if (path === "/v1/projects") data = [project];
    else if (path === "/v1/provider-profiles") data = [provider];
    else if (path === "/v1/model-profiles") data = [model];
    else if (path === "/v1/policy-profiles") data = [];
    else if (path === `/v1/projects/${project.id}/sessions`) data = [session];
    else if (path === `/v1/sessions/${session.id}`) data = session;
    else if (path === `/v1/sessions/${session.id}/active-run`) data = null;
    else if (path === `/v1/sessions/${session.id}/compactions`) data = [];
    else if (path === `/v1/sessions/${session.id}/messages`) {
      data = {
        messages: [message("m1", undefined, "user", "hello"), message("m2", "m1", "assistant", "the reply")],
        hasMore: false, activeLeafMessageId: "m2",
      };
    } else {
      return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { message: path } }) });
    }
    await fulfill(route, data);
  });
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

test("host assistant replies attribute the resolved model name next to the speaker", async ({ page }) => {
  await mock(page);
  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByText(session.title, { exact: true }).click();

  await expect(page.getByText("the reply", { exact: true })).toBeVisible();
  const speaker = page.locator(".assistant-speaker");
  await expect(speaker.getByText("Host", { exact: true })).toBeVisible();
  await expect(speaker.locator(".assistant-model")).toHaveText("Claude Sonnet 4");
});

test("streaming host replies attribute the model name in real time", async ({ page }) => {
  const run = {
    id: "stream-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1,
    status: "running",
    speakerSnapshot: { kind: "host", displayName: "Host" },
    requestedConfig: { modelProfileId: model.id, toolPolicyProfileId: "builtin-tool-discuss-v1" },
    effectiveConfig: {},
    createdAt: now,
  };
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (await tryFulfillBlankSessionCreate(route)) return;
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, { run });
    if (path === `/v1/sessions/${session.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream",
      body: subscribedFrame({ activeRun: run, pendingApproval: null, queuedInputs: [], checkpoints: [], delegationActive: false }) });
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: "m1" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/runs/${run.id}/events`) {
      return route.fulfill({ status: 200, contentType: "text/event-stream",
        body: `event: live\ndata: ${JSON.stringify({ type: "text_delta", payload: { iteration: 1, text: "streaming reply" } })}\n\n` });
    }
    return route.abort();
  });

  await page.goto("/");
  await selectProject(page, project.name);
  await page.getByText(session.title, { exact: true }).click();

  await expect(page.getByText("streaming reply", { exact: true })).toBeVisible();
  const speaker = page.locator(".assistant-speaker");
  await expect(speaker.getByText("Host", { exact: true })).toBeVisible();
  await expect(speaker.locator(".assistant-model")).toHaveText("Claude Sonnet 4");
});
