import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "surface-project", name: "Spatial atlas", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const session = { id: "surface-session", projectId: project.id, title: "Review marker matrix", status: "active", activeLeafMessageId: "m7", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:07Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" }));

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant" | "tool", parts: unknown[]) {
  return { id, sessionId: session.id, parentMessageId, role, status: "complete", parts, createdAt: `2026-07-28T00:00:0${id.slice(-1)}Z` };
}

const messages = [
  message("m1", undefined, "user", [{ type: "text", text: "Inspect `markers.csv` and summarize the result." }]),
  message("m2", "m1", "assistant", [
    { type: "thinking", text: "I should inspect the matrix before changing the report." },
    { type: "text", text: "I will inspect the matrix and update the report.\n\n| Check | Status |\n| --- | --- |\n| Schema | pending |" },
    { type: "tool_call", toolCall: { id: "read-call", name: "read", arguments: { path: "/workspace/atlas/markers.csv", apiKey: "super-secret" } } },
    { type: "tool_call", toolCall: { id: "write-call", name: "write", arguments: { path: "/workspace/atlas/report.md", content: "# Marker report", credentialToken: "super-secret" } } },
  ]),
  message("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "write-call", toolName: "write", content: "Wrote report.md", isError: false } }]),
  message("m4", "m3", "tool", [{ type: "tool_result", toolResult: { toolCallId: "read-call", toolName: "read", content: "gene,cluster\nEPCAM,C1", isError: false } }]),
  message("m5", "m4", "assistant", [{ type: "text", text: "The matrix is valid and the report now contains the initial marker summary." }]),
  message("m6", "m5", "user", [{ type: "text", text: "Keep the final interpretation concise." }]),
  message("m7", "m6", "assistant", [{ type: "text", text: "Understood. Future interpretations will remain concise." }]),
];

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockSurface(page: Page) {
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m7" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, [{
      id: "checkpoint", sessionId: session.id, triggerRunId: "run", status: "completed", reason: "manual",
      baseLeafMessageId: "m7", sourceThroughMessageId: "m5", firstKeptMessageId: "m6", summary: "Marker matrix inspection completed.",
      reclaimedTokens: 1200, createdAt: "2026-07-28T00:00:05.500Z",
    }]);
    return route.abort();
  });
}

async function openSurface(page: Page) {
  await mockSurface(page);
  await page.goto("/");
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByTitle("Select project").click();
  await page.getByRole("button", { name: project.name }).click();
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await expect(page.locator("[data-turn-id]")).toHaveCount(2);
}

test("conversation groups tool activity, applies disclosure defaults, and redacts arguments", async ({ page }) => {
  await openSurface(page);
  await expect(page.getByRole("table")).toBeVisible();
  await expect(page.locator(".thinking-block")).not.toHaveAttribute("open", "");
  await expect(page.locator('[data-tool-call-id="read-call"]')).not.toHaveAttribute("open", "");
  await expect(page.locator('[data-tool-call-id="write-call"]')).toHaveAttribute("open", "");
  await expect(page.locator(".tool-batch")).toHaveCount(1);
  await expect(page.locator('[data-tool-call-id="read-call"] .tool-state')).toContainText("Done");
  await expect(page.locator('[data-tool-call-id="write-call"] .tool-state')).toContainText("Done");
  await page.locator('[data-tool-call-id="write-call"] .tool-detail-disclosure').first().getByText("Arguments", { exact: true }).click();
  await expect(page.locator('[data-tool-call-id="write-call"] pre').first()).toContainText("[redacted]");
  await expect(page.getByText("super-secret", { exact: false })).toHaveCount(0);
  await expect(page.locator(".checkpoint-separator")).toContainText("Context checkpoint");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  await page.screenshot({ path: "/tmp/ennote-conversation-surface-light.png", fullPage: true });
});

test("theme control applies and persists light and dark preferences", async ({ page }) => {
  await openSurface(page);
  await page.getByRole("button", { name: "Choose theme" }).click();
  await page.getByRole("menuitemradio", { name: "Dark theme" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  expect(await page.evaluate(() => localStorage.getItem("ennote-theme"))).toBe("dark");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.getByTitle("Select project").click();
  await page.getByRole("button", { name: project.name }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await page.screenshot({ path: "/tmp/ennote-conversation-surface-dark.png", fullPage: true });
});

test("a nonterminal stream EOF reconnects the same run and refreshes canonical history", async ({ page }) => {
  const run = { id: "reconnect-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1,
    status: "running", requestedConfig: { toolPolicyProfileId: "builtin-tool-discuss-v1" }, effectiveConfig: {}, createdAt: "2026-07-28T00:00:00Z" };
  let streams = 0;
  let completed = false;
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, completed ? null : { run });
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, {
      messages: completed ? [
        message("m1", undefined, "user", [{ type: "text", text: "Resume after disconnect" }]),
        message("m2", "m1", "assistant", [{ type: "text", text: "Recovered response" }]),
      ] : [], hasMore: false,
    });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/runs/${run.id}/events`) {
      streams += 1;
      if (streams === 1) return route.fulfill({ status: 200, contentType: "text/event-stream",
        body: `data: ${JSON.stringify({ type: "text_delta", payload: { iteration: 1, text: "Partial response" } })}\n\n` });
      completed = true;
      return route.fulfill({ status: 200, contentType: "text/event-stream",
        body: `data: ${JSON.stringify({ type: "run_succeeded", payload: {} })}\n\n` });
    }
    return route.abort();
  });
  await page.goto("/");
  await page.getByTitle("Select project").click();
  await page.getByRole("button", { name: project.name }).click();
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await expect.poll(() => streams, { timeout: 5000 }).toBe(2);
  await expect(page.getByText("Recovered response", { exact: true })).toBeVisible();
  await expect(page.getByText("Completed", { exact: true })).toBeVisible();
});

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("tool batches and composer stay inside the viewport", async ({ page }) => {
    await openSurface(page);
    await expect(page.getByRole("button", { name: "Send" })).toBeVisible();
    await expect(page.locator('[data-tool-call-id="write-call"]')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await page.screenshot({ path: "/tmp/ennote-conversation-surface-mobile.png", fullPage: true });
  });
});
