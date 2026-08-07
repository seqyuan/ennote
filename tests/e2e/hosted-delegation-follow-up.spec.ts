import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "delegation-project", name: "Delegation workspace", description: "", status: "active",
  createdAt: "2026-08-04T00:00:00Z", updatedAt: "2026-08-04T00:00:00Z" };
const session = { id: "delegation-session", projectId: project.id, title: "Hosted delegation", status: "active",
  activeLeafMessageId: "m4", createdAt: "2026-08-04T00:00:00Z", updatedAt: "2026-08-04T00:00:04Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode,
  kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-08-04T00:00:00Z", updatedAt: "2026-08-04T00:00:00Z" }));
const parentRun = { id: "parent-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1,
  status: "waiting_children", commitFormatVersion: 1, executionDepth: 0, publishMode: "public_final",
  speakerSnapshot: { kind: "host", displayName: "Host" }, contextSnapshot: {}, requestedConfig: {}, effectiveConfig: {},
  createdAt: "2026-08-04T00:00:00Z" };

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant" | "tool", parts: unknown[], runId?: string) {
  return { id, sessionId: session.id, parentMessageId, role, status: "complete", runId, speakerKind: role === "assistant" ? "host" : "user",
    speakerSnapshot: role === "assistant" ? { kind: "host", displayName: "Host" } : { kind: "user", displayName: "User" },
    visibility: "public", parts, createdAt: `2026-08-04T00:00:0${id.slice(-1)}Z` };
}

async function fulfill(route: Route, data: unknown) {
  await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function selectSession(page: Page) {
  const mobile = (page.viewportSize()?.width ?? 1280) <= 640;
  const navigation = page.locator("#workspace-navigation");
  const waitNavigationClosed = async () => {
    if (!mobile) return;
    await expect(navigation).not.toHaveClass(/sidebar-open/);
    await expect.poll(() => navigation.evaluate(element => element.getBoundingClientRect().right)).toBeLessThanOrEqual(1);
  };
  const openNavigation = async () => {
    if (!mobile) return;
    if (!await navigation.evaluate(element => element.classList.contains("sidebar-open"))) {
      await page.getByRole("button", { name: "Open navigation" }).click();
    }
    await expect(navigation).toHaveClass(/sidebar-open/);
  };
  await page.goto("/");
  await waitNavigationClosed();
  await openNavigation();
  await page.getByTitle("Select project").click();
  await page.getByLabel("Projects", { exact: true }).getByRole("button", { name: project.name }).click();
  await waitNavigationClosed();
  await openNavigation();
  await page.getByRole("button", { name: session.title, exact: true }).click();
  await waitNavigationClosed();
}

function commonRoute(path: string, route: Route) {
  if (path === "/v1/projects") return fulfill(route, [project]);
  if (path === "/v1/policy-profiles") return fulfill(route, policies);
  if (path === "/v1/provider-profiles" || path === "/v1/model-profiles") return fulfill(route, []);
  if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
  if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
  if (path === `/v1/sessions/${session.id}/compactions` || path === `/v1/sessions/${session.id}/branches`) return fulfill(route, []);
  if (path === `/v1/sessions/${session.id}/recovery`) return fulfill(route, null);
  if (path.startsWith("/v1/roles")) return fulfill(route, { items: [], nextCursor: "" });
  return null;
}

test("replies to a needs_input delegated task via the private dialog", async ({ page }) => {
  const groupID = "group-ni";
  const inspection = { group: { id: groupID, parentRunId: parentRun.id, parentToolCallId: "delegate-call",
    strategy: "single", status: "settled", createdAt: "2026-08-04T00:00:02Z" },
    currentGeneration: 0,
    items: [{ itemId: "item-ni", name: "inspect", status: "succeeded", attempts: [
      { attemptId: "att-ni", generation: 0, childRunId: "child-ni", status: "needs_input",
        usage: { modelCalls: 2, toolCalls: 3, tokens: 1000, outputTokens: 500, costMicros: 50 },
        result: { status: "needs_input", summary: "Which files should I inspect?" } }] }],
    generations: [{ id: "gen-0", groupId: groupID, generation: 0, kind: "initial", status: "settled",
      retrySelection: [], reusedAttempts: [], authorizationSnapshot: {}, budgetSnapshot: {},
      clientRequestId: "gen-0", createdAt: "2026-08-04T00:00:02Z" }],
    validActions: ["retry"] };
  const messages = [
    message("m1", undefined, "user", [{ type: "text", text: "Delegate an inspection." }]),
    message("m2", "m1", "assistant", [{ type: "tool_call", toolCall: { id: "delegate-call", name: "delegate_roles",
      arguments: { delegations: [{ name: "inspect", roleHandle: "workspace-explorer", assignment: "Inspect",
        budget: { maxModelCalls: 4, maxToolCalls: 8 } }] } } }], parentRun.id),
    message("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "delegate-call", toolName: "delegate_roles",
      content: "{\"status\":\"settled\"}", isError: false } }], parentRun.id),
    message("m4", "m3", "assistant", [{ type: "text", text: "Delegation settled." }], parentRun.id),
  ];
  let continuationSubmitted = false;
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const common = commonRoute(path, route);
    if (common) return common;
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m4" });
    if (path === `/v1/runs/${parentRun.id}/children`) {
      return fulfill(route, { parentRunId: parentRun.id, groups: [{ id: groupID, parentToolCallId: "delegate-call",
        strategy: "single", status: "settled", createdAt: "2026-08-04T00:00:02Z",
        children: [{ itemId: "item-ni", childRunId: "child-ni", name: "inspect", roleHandle: "workspace-explorer",
          roleDisplayName: "Workspace Explorer", itemStatus: "succeeded", runStatus: "succeeded", createdAt: "2026-08-04T00:00:02Z",
          result: { status: "needs_input", summary: "Which files should I inspect?" } }] }] });
    }
    if (path === `/v1/delegations/${groupID}`) return fulfill(route, inspection);
    if (path === `/v1/delegation-items/item-ni/input`) {
      const body = route.request().postDataJSON();
      expect(body).toMatchObject({ expectedGeneration: 0 });
      expect(body.text).toContain("src/");
      continuationSubmitted = true;
      return fulfill(route, { generation: { id: "gen-1", groupId: groupID, generation: 1, kind: "input",
        status: "running", retrySelection: ["item-ni"], reusedAttempts: [],
        authorizationSnapshot: {}, budgetSnapshot: {}, clientRequestId: "c", createdAt: "2026-08-04T00:00:03Z" },
        childRunId: "child-ni-2" });
    }
    return route.abort();
  });

  await selectSession(page);
  await expect(page.locator('[data-child-run-id="child-ni"] .child-run-retry')).toBeVisible();
  await page.locator('[data-child-run-id="child-ni"] .child-run-retry').click();
  await expect(page.getByRole("dialog", { name: /Reply/ })).toBeVisible();
  await page.locator(".follow-up-input").fill("Inspect src/ only.");
  await page.locator(".follow-up-submit").click();
  expect(continuationSubmitted).toBe(true);
  await expect(page.getByRole("dialog", { name: /Reply/ })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});
