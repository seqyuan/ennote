import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillBlankSessionCreate } from "./harness";

const now = "2026-07-28T00:00:00Z";
const project = { id: "inspect-project", name: "Inspect project", description: "", status: "active", createdAt: now, updatedAt: now };
const session = { id: "inspect-session", projectId: project.id, title: "Inspect run", status: "active", mode: "hosted", activeLeafMessageId: "m2", createdAt: now, updatedAt: now };
const provider = { id: "provider", name: "anthropic", providerType: "openai-compatible", baseUrl: "https://example.test", apiKey: "test", status: "active", createdAt: now, updatedAt: now };
const model = { id: "mp-1", providerId: provider.id, modelName: "claude-sonnet-4", displayName: "Claude Sonnet 4", contextWindow: 32000, maxOutputTokens: 2048, supportsVision: false, supportsToolUse: true, supportsThinking: true, thinkingDialect: "openai_reasoning_effort", supportedThinkingEfforts: ["default", "high"], isDefault: true, status: "active", createdAt: now, updatedAt: now };
const RUN = "run-inspector";

// The assistant message carries the Run id, which is how the inspector finds a
// Run to inspect when nothing is currently running.
function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", text: string) {
  return {
    id, sessionId: session.id, parentMessageId, role, status: "complete",
    speakerKind: role === "user" ? "user" : "host",
    speakerSnapshot: role === "user" ? { kind: "user", displayName: "You" } : { kind: "host", displayName: "Host" },
    modelProfileId: role === "assistant" ? model.id : undefined,
    apiModel: role === "assistant" ? model.modelName : undefined,
    visibility: "public",
    ...(role === "assistant" ? { runId: RUN } : {}),
    parts: [{ type: "text", text }], createdAt: now,
  };
}

function promptComposition(recorded: boolean) {
  return {
    runId: RUN, version: 1, platformVersion: "hosted-v1", digest: "base-digest",
    sections: recorded ? [
      { id: "base", kind: "base", source: "agent_profile", bytes: 512, digest: "aaaa1111bbbb2222" },
      { id: "role.definition", kind: "role", source: "role:analyst@3", bytes: 2048, digest: "cccc3333dddd4444" },
      { id: "skill.preload.report", kind: "skill_preload", source: "role:analyst@3", bytes: 1024, digest: "eeee5555ffff6666", skillId: "report" },
    ] : [],
    sectionsDigest: recorded ? "sections-digest" : "",
    composedDigest: recorded ? "composed-digest" : "",
    prompt: recorded ? "You are the analyst.\n\nReport findings precisely." : "",
    skillCatalogState: recorded ? "materialized" : "",
    skillCatalogDigest: recorded ? "catalog-digest" : "",
    recorded,
  };
}

const mcpSurface = {
  runId: RUN,
  servers: [
    {
      id: "srv-1", bindingId: "bio-binding", bindingRevision: 2, profileVersionId: "bio@v000001",
      configDigest: "config-a", negotiatedProtocol: "2026-07-28", serverIdentityDigest: "identity-a",
      catalogDigest: "catalog-a", required: true, unavailableReason: "",
      instructions: "Search before you fetch.", instructionsDigest: "sha256:instructions",
      tools: [{ remoteName: "search", exposedName: "bio__search", description: "Search", riskClass: "external", sourceKind: "managed", schemaDigest: "d1" }],
    },
    {
      id: "srv-2", bindingId: "down-binding", bindingRevision: 1, profileVersionId: "down@v000001",
      configDigest: "config-b", negotiatedProtocol: "", serverIdentityDigest: "", catalogDigest: "",
      required: false, unavailableReason: "connection refused", instructions: "", instructionsDigest: "",
      tools: [],
    },
  ],
};

const transcript = {
  runId: RUN, formatVersion: 2, source: "shadow", digest: "transcript-digest", hasMore: false,
  messages: [
    { id: "t1", runId: RUN, ordinal: 0, role: "assistant", visibility: "private", createdAt: now,
      content: [{ type: "thinking", text: "weighing options" }, { type: "tool_call", toolCall: { id: "c1", name: "read", arguments: { path: "a.csv" } } }] },
    { id: "t2", runId: RUN, ordinal: 1, role: "tool", visibility: "private", createdAt: now,
      content: [{ type: "tool_result", toolResult: { toolCallId: "c1", toolName: "read", content: "a,b", isError: false } }] },
  ],
};

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockInspector(page: Page, options: { recorded?: boolean } = {}) {
  const recorded = options.recorded ?? true;
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
        messages: [message("m1", undefined, "user", "analyze the samples"), message("m2", "m1", "assistant", "the reply")],
        hasMore: false, activeLeafMessageId: "m2",
      };
    } else if (path === `/v1/runs/${RUN}/prompt`) data = promptComposition(recorded);
    else if (path === `/v1/runs/${RUN}/mcp`) data = mcpSurface;
    else if (path === `/v1/runs/${RUN}/messages`) data = transcript;
    else return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { message: path } }) });
    await fulfill(route, data);
  });
}

async function openInspector(page: Page, options: { recorded?: boolean } = {}) {
  await mockInspector(page, options);
  // Restore rather than navigate. The connect hook restores the stored project and
  // session on its first sessions-loaded tick, which is how a returning user lands
  // back in their session. It is also the only deterministic path: with no project
  // selected the hook marks itself done, and afterwards it only ever adopts an
  // existing blank session, so clicking the row races a blank auto-connect.
  await page.addInitScript(([projectId, sessionId]) => {
    try {
      window.localStorage.setItem("ennote-selected-project", projectId);
      window.localStorage.setItem("ennote-selected-session", sessionId);
    } catch {
      /* storage unavailable */
    }
  }, [project.id, session.id]);
  await page.goto("/");
  // The inspector targets the newest Run the timeline knows about, so wait for
  // the conversation to be projected before opening the panel.
  await expect(page.getByText("the reply", { exact: true })).toBeVisible();
  await page.getByTitle("Show panel").click();
  await page.getByRole("tab", { name: "Status" }).click();
}

test("the inspector shows the frozen prompt composition of the run", async ({ page }) => {
  await openInspector(page);

  // Every contribution is attributed to what produced it, with its byte count.
  await expect(page.locator('[data-prompt-section="base"]')).toContainText("agent_profile");
  await expect(page.locator('[data-prompt-section="role"]')).toContainText("role:analyst@3");
  await expect(page.locator('[data-prompt-section="skill_preload"]')).toContainText("report");
  await expect(page.locator('[data-prompt-section="role"]')).toContainText("2.0 KiB");
  // A truncated digest is shown, with the full value available on hover.
  await expect(page.locator('[data-prompt-section="role"]')).toContainText("cccc3333…");
  await expect(page.locator('[data-prompt-section]')).toHaveCount(3);

  // The exact prompt text is available on demand, not dumped by default.
  await expect(page.locator("[data-run-prompt]")).toHaveCount(0);
  await page.getByRole("button", { name: /Frozen prompt text/ }).click();
  await expect(page.locator("[data-run-prompt]")).toContainText("You are the analyst.");
});

test("the inspector explains why a skill catalog section is absent", async ({ page }) => {
  await openInspector(page);

  await expect(page.getByText("materialized")).toBeVisible();
});

test("the inspector shows the frozen MCP surface, including a failed server", async ({ page }) => {
  await openInspector(page);

  const healthy = page.locator('[data-mcp-server="bio-binding"]');
  await expect(healthy).toContainText("rev 2");
  await expect(healthy).toContainText("2026-07-28");
  await expect(healthy.locator("summary")).toContainText("Instructions from the server");
  await healthy.getByText("Instructions from the server").click();
  await expect(healthy.locator("[data-mcp-instructions]")).toContainText("Search before you fetch.");

  // An unreachable server is a recorded fact with a reason, not an absence.
  const down = page.locator('[data-mcp-server="down-binding"]');
  await expect(down).toContainText("connection refused");
  await expect(down).toContainText("unavailable");
});

test("the inspector reads the run's private transcript without touching the timeline", async ({ page }) => {
  await openInspector(page);

  await expect(page.getByText("Private run transcript")).toBeVisible();
  const first = page.locator('[data-transcript-ordinal="0"]');
  await expect(first).toContainText("Assistant");
  await expect(first).toContainText("private");
  await expect(page.locator('[data-transcript-ordinal="1"]')).toContainText("Tool");

  // The transcript stays out of the conversation: its content is never rendered
  // as part of the timeline.
  await expect(page.getByText("weighing options")).toHaveCount(1);
});

test("a run that recorded no composition says so instead of showing an empty prompt", async ({ page }) => {
  await openInspector(page, { recorded: false });

  await expect(page.getByText("This run did not record its prompt composition")).toBeVisible();
  // No contribution is advertised, and no prompt is offered to open.
  await expect(page.locator("[data-prompt-section]")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Frozen prompt text/ })).toHaveCount(0);
  // The base-prompt digest it is still bound to remains visible.
  await expect(page.getByText("Base prompt")).toBeVisible();
});
