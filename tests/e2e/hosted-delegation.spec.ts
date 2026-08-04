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
  await page.getByRole("button", { name: project.name }).click();
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

test("nested delegation activity polls child state and renders terminal result", async ({ page }) => {
  let activityRequests = 0;
  const messages = [
    message("m1", undefined, "user", [{ type: "text", text: "Inspect the workspace with a delegated Role." }]),
    message("m2", "m1", "assistant", [{ type: "tool_call", toolCall: { id: "delegate-call", name: "delegate_roles",
      arguments: { delegations: [{ name: "inspect", roleHandle: "workspace-explorer", assignment: "Inspect files",
        budget: { maxModelCalls: 4, maxToolCalls: 8 } }] } } }], parentRun.id),
    message("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "delegate-call", toolName: "delegate_roles",
      content: "{\"status\":\"settled\"}", isError: false } }], parentRun.id),
    message("m4", "m3", "assistant", [{ type: "text", text: "The workspace inspection is complete." }], parentRun.id),
  ];
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const common = commonRoute(path, route);
    if (common) return common;
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m4" });
    if (path === `/v1/runs/${parentRun.id}/children`) {
      activityRequests += 1;
      const completed = activityRequests > 1;
      return fulfill(route, { parentRunId: parentRun.id, groups: [{ id: "group", parentToolCallId: "delegate-call",
        strategy: "single", status: completed ? "settled" : "waiting_children", createdAt: "2026-08-04T00:00:02Z",
        children: [{ itemId: "item", childRunId: "child", name: "inspect", roleHandle: "workspace-explorer",
          roleDisplayName: "Workspace Explorer", itemStatus: completed ? "succeeded" : "running",
          runStatus: completed ? "succeeded" : "running", createdAt: "2026-08-04T00:00:02Z",
          ...(completed ? { result: { status: "completed", summary: "Workspace contains no files." } } : {}) }] }] });
    }
    return route.abort();
  });

  await selectSession(page);
  await expect(page.locator(".nested-activity")).toBeVisible();
  await expect(page.locator('[data-child-run-id="child"] .child-run-status')).toContainText("Completed", { timeout: 5000 });
  await page.locator('[data-child-run-id="child"] .child-run-result').getByText("Result", { exact: true }).click();
  await expect(page.getByText("Workspace contains no files.", { exact: true })).toBeVisible();
  expect(activityRequests).toBeGreaterThan(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  await page.screenshot({ path: "/tmp/ennote-hosted-delegation-activity.png", fullPage: true });
});

test("discovers a later child tool approval while the parent waits", async ({ page }) => {
  let activeRunRequests = 0;
  let approved = false;
  const childApproval = { id: "child-approval", runId: "child-run", sessionId: session.id, iteration: 1,
    batchDigest: "child-digest", status: "pending", requestedAt: "2026-08-04T00:00:03Z",
    attribution: { speakerKind: "role", handle: "workspace-explorer", displayName: "Workspace Explorer",
      permissionCeiling: "ask", authority: "read_only" },
    items: [{ callIndex: 0, toolCallId: "read-call", toolName: "write", riskClass: "local_write",
      argumentsPreview: "{\"path\":\"notes.txt\"}" }] };
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const common = commonRoute(path, route);
    if (common) return common;
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false });
    if (path === `/v1/sessions/${session.id}/active-run`) {
      activeRunRequests += 1;
      return fulfill(route, { run: parentRun,
        ...(activeRunRequests > 1 && !approved ? { pendingApproval: childApproval } : {}) });
    }
    if (path === `/v1/runs/${parentRun.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream", body: "" });
    if (path === `/v1/approval-requests/${childApproval.id}/decision`) {
      approved = true;
      return fulfill(route, { ...childApproval, status: "approved", resolvedAt: "2026-08-04T00:00:04Z" });
    }
    return route.abort();
  });

  await selectSession(page);
  await expect(page.getByText("@workspace-explorer · read_only · ask", { exact: true })).toBeVisible({ timeout: 6000 });
  await page.getByRole("button", { name: "Approve batch" }).click();
  await expect(page.getByText("Approval required", { exact: true })).toHaveCount(0);
  expect(activeRunRequests).toBeGreaterThan(1);
  expect(approved).toBe(true);
});

test.describe("mobile admission approval", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("shows structured delegation admission and submits approval", async ({ page }) => {
    let approved = false;
    const approval = { id: "approval", runId: parentRun.id, sessionId: session.id, iteration: 1, batchDigest: "digest",
      status: "pending", requestedAt: "2026-08-04T00:00:01Z", items: [{ callIndex: 0, toolCallId: "delegate-call",
        toolName: "delegate_roles", riskClass: "delegation", argumentsPreview: "{}", delegations: [{ name: "inspect",
          roleHandle: "workspace-explorer", assignmentPreview: "Inspect files without modifying them.", outputContract: "text-v1",
          budget: { maxModelCalls: 4, maxToolCalls: 8, maxTotalTokens: 20000, maxOutputTokens: 4000,
            maxCostUsdMicros: 100000, maxWallTimeMs: 120000 } }] }] };
    await page.route("**/api/worker/v1/**", async route => {
      const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
      const common = commonRoute(path, route);
      if (common) return common;
      if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false });
      if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, approved ? null : {
        run: { ...parentRun, status: "waiting_delegation_admission" }, pendingApproval: approval,
      });
      if (path === `/v1/runs/${parentRun.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream", body: "" });
      if (path === `/v1/approval-requests/${approval.id}/decision`) {
        expect(route.request().postDataJSON()).toMatchObject({ decision: "approved" });
        approved = true;
        return fulfill(route, { ...approval, status: "approved", resolvedAt: "2026-08-04T00:00:02Z" });
      }
      return route.abort();
    });

    await selectSession(page);
    await expect(page.getByText("Delegation approval required", { exact: true })).toBeVisible();
    await expect(page.getByText("@workspace-explorer", { exact: true })).toBeVisible();
    await expect(page.getByText("4 model · 8 tool", { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await page.screenshot({ path: "/tmp/ennote-hosted-delegation-approval-mobile.png", fullPage: true });
    await page.getByRole("button", { name: "Approve batch" }).click();
    await expect(page.getByText("Delegation approval required", { exact: true })).toHaveCount(0);
    expect(approved).toBe(true);
  });
});

test("retries an eligible child and renders generation history", async ({ page }) => {
  const groupID = "group-retry";
  let retryRequested = false;
  const inspection = { group: { id: groupID, parentRunId: parentRun.id, parentToolCallId: "delegate-call",
    strategy: "single", status: "settled", createdAt: "2026-08-04T00:00:02Z" },
    currentGeneration: 0,
    items: [{ itemId: "item-ok", name: "inspect", status: "succeeded", attempts: [
      { attemptId: "att-ok", generation: 0, childRunId: "child-ok", status: "succeeded", usage: { modelCalls: 2, toolCalls: 3, tokens: 1000, outputTokens: 500, costMicros: 50 },
        result: { status: "completed", summary: "found README" }, resultDigest: "sha256:aa" }] },
      { itemId: "item-fail", name: "review", status: "failed", attempts: [
        { attemptId: "att-fail", generation: 0, childRunId: "child-fail", status: "failed", usage: { modelCalls: 1, toolCalls: 1, tokens: 200, outputTokens: 100, costMicros: 10 },
          errorCode: "provider_unavailable", result: { status: "blocked", summary: "review failed" } }] }],
    generations: [{ id: "gen-0", groupId: groupID, generation: 0, kind: "initial", status: "settled",
      retrySelection: [], reusedAttempts: [], authorizationSnapshot: {}, budgetSnapshot: {},
      clientRequestId: "gen-0", createdAt: "2026-08-04T00:00:02Z" }],
    validActions: ["retry"] };
  const messages = [
    message("m1", undefined, "user", [{ type: "text", text: "Delegate a review." }]),
    message("m2", "m1", "assistant", [{ type: "tool_call", toolCall: { id: "delegate-call", name: "delegate_roles",
      arguments: { delegations: [{ name: "inspect", roleHandle: "workspace-explorer", assignment: "Review",
        budget: { maxModelCalls: 4, maxToolCalls: 8 } }] } } }], parentRun.id),
    message("m3", "m2", "tool", [{ type: "tool_result", toolResult: { toolCallId: "delegate-call", toolName: "delegate_roles",
      content: "{\"status\":\"settled\"}", isError: false } }], parentRun.id),
    message("m4", "m3", "assistant", [{ type: "text", text: "Delegation settled." }], parentRun.id),
  ];
  await page.route("**/api/worker/v1/**", async route => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const common = commonRoute(path, route);
    if (common) return common;
    if (path === `/v1/sessions/${session.id}/active-run`) return fulfill(route, null);
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages, hasMore: false, activeLeafMessageId: "m4" });
    if (path === `/v1/runs/${parentRun.id}/children`) {
      return fulfill(route, { parentRunId: parentRun.id, groups: [{ id: groupID, parentToolCallId: "delegate-call",
        strategy: "parallel", status: "settled", createdAt: "2026-08-04T00:00:02Z",
        children: [{ itemId: "item-ok", childRunId: "child-ok", name: "inspect", roleHandle: "workspace-explorer",
          roleDisplayName: "Workspace Explorer", itemStatus: "succeeded", runStatus: "succeeded", createdAt: "2026-08-04T00:00:02Z" },
          { itemId: "item-fail", childRunId: "child-fail", name: "review", roleHandle: "workspace-explorer",
            roleDisplayName: "Workspace Explorer", itemStatus: "failed", runStatus: "failed", createdAt: "2026-08-04T00:00:02Z",
            errorCode: "provider_unavailable", errorMessage: "review failed" }] }] });
    }
    if (path === `/v1/delegations/${groupID}`) return fulfill(route, inspection);
    if (path === `/v1/delegations/${groupID}/retry`) {
      expect(route.request().postDataJSON()).toMatchObject({
        expectedGeneration: 0, itemIds: ["item-fail"],
      });
      retryRequested = true;
      return fulfill(route, { generation: { id: "gen-1", groupId: groupID, generation: 1, kind: "retry",
        status: "running", retrySelection: ["item-fail"], reusedAttempts: [{ itemId: "item-ok", attemptId: "att-ok",
          generation: 0, childRunId: "child-ok", resultDigest: "sha256:aa" }],
        authorizationSnapshot: {}, budgetSnapshot: {}, clientRequestId: "req", createdAt: "2026-08-04T00:00:03Z" },
        childRunIds: ["child-retry"], approval: null });
    }
    return route.abort();
  });

  await selectSession(page);
  await expect(page.locator(".nested-activity")).toBeVisible();
  // The failed row exposes the icon Retry command.
  await expect(page.locator('[data-child-run-id="child-fail"] .child-run-retry')).toBeVisible();
  await page.locator('[data-child-run-id="child-fail"] .child-run-retry').hover();
  await page.locator('[data-child-run-id="child-fail"] .child-run-retry').click();
  expect(retryRequested).toBe(true);

  // Generation history renders generation 0 with settled status.
  await page.locator(".generation-history summary").first().click();
  await expect(page.getByText("Generation 0 (current)", { exact: true })).toBeVisible();
  await expect(page.getByText("settled", { exact: true }).first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});
