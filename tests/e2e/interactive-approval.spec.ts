import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "approval-project", name: "Approval project", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const session = { id: "approval-session", projectId: project.id, title: "Approval session", status: "active", activeLeafMessageId: "message", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const run = { id: "approval-run", turnId: "turn", sessionId: session.id, runKind: "agent", attempt: 1, status: "waiting_for_approval", requestedConfig: { toolPolicyProfileId: "builtin-tool-ask-v1" }, effectiveConfig: {}, createdAt: "2026-07-28T00:00:00Z" };
const approval = { id: "approval", runId: run.id, sessionId: session.id, iteration: 1, batchDigest: "digest", status: "pending", requestedAt: "2026-07-28T00:00:01Z", items: [
  { callIndex: 1, toolCallId: "write-call", toolName: "write", riskClass: "local_write", argumentsPreview: '{"path":"notes.txt","content":"updated"}' },
  { callIndex: 2, toolCallId: "bash-call", toolName: "bash", riskClass: "shell", argumentsPreview: '{"command":"gofmt -w notes.go"}' },
] };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" }));

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockApprovalApp(page: Page, onDecision: (decision: string) => void, delayReplayRefresh = false) {
  let active: unknown = { run, pendingApproval: approval };
  let activeRequests = 0;
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session]);
    if (path === `/v1/sessions/${session.id}`) return fulfill(route, session);
    if (path === `/v1/sessions/${session.id}/active-run`) {
      activeRequests += 1;
      const snapshot = active;
      if (delayReplayRefresh && activeRequests > 1) await new Promise(resolve => setTimeout(resolve, 250));
      return fulfill(route, snapshot);
    }
    if (path === `/v1/sessions/${session.id}/messages`) return fulfill(route, { messages: [], hasMore: false, activeLeafMessageId: "message" });
    if (path === `/v1/sessions/${session.id}/compactions`) return fulfill(route, []);
    if (path === `/v1/runs/${run.id}/events`) return route.fulfill({ status: 200, contentType: "text/event-stream",
      body: `data: ${JSON.stringify({ type: "approval_requested", payload: { approvalId: approval.id } })}\n\n` });
    if (path === `/v1/approval-requests/${approval.id}/decision`) {
      const body = route.request().postDataJSON();
      onDecision(body.decision);
      active = { run: { ...run, status: "queued" } };
      return fulfill(route, { ...approval, status: body.decision, resolvedAt: "2026-07-28T00:00:02Z" });
    }
    return route.abort();
  });
}

async function openApproval(page: Page) {
  await page.goto("/");
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByTitle("Select project").first().click();
  await page.getByText(project.name, { exact: true }).click();
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByText(session.title, { exact: true }).click();
  await expect(page.getByRole("region", { name: "Tool approval required" })).toBeVisible();
  await expect(page.locator(".pending-tool-batch .approval-panel")).toBeVisible();
}

test("pending approval survives reload and resolves the whole batch", async ({ page }) => {
  let decision = "";
  await mockApprovalApp(page, value => { decision = value; });
  await openApproval(page);
  await expect(page.getByText("write", { exact: true })).toBeVisible();
  await expect(page.getByText("bash", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Configure run", exact: true }).click();
  await expect(page.getByRole("button", { name: "Ask", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Ask", exact: true })).toHaveAttribute("aria-pressed", "true");

  await page.reload();
  await page.getByTitle("Select project").first().click();
  await page.getByText(project.name, { exact: true }).click();
  await page.getByText(session.title, { exact: true }).click();
  await expect(page.getByRole("button", { name: "Approve batch" })).toBeVisible();
  await page.getByRole("button", { name: "Approve batch" }).click();
  await expect.poll(() => decision).toBe("approved");
  await expect(page.getByRole("region", { name: "Tool approval required" })).toHaveCount(0);
});

test("a stale approval refresh cannot reopen a resolved panel", async ({ page }) => {
  await mockApprovalApp(page, () => {}, true);
  await openApproval(page);
  await page.waitForTimeout(50);
  await page.getByRole("button", { name: "Reject batch" }).click();
  await expect(page.getByRole("region", { name: "Tool approval required" })).toHaveCount(0);
  await page.waitForTimeout(300);
  await expect(page.getByRole("region", { name: "Tool approval required" })).toHaveCount(0);
});

test.describe("mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("approval panel and composer remain within the viewport", async ({ page }) => {
    await mockApprovalApp(page, () => {});
    await openApproval(page);
    await expect(page.getByRole("button", { name: "Reject batch" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Approve batch" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
  });
});
