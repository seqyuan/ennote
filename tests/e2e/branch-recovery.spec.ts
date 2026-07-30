import { expect, test, type Page, type Route } from "@playwright/test";

const project = { id: "branch-project", name: "Branch project", description: "", status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const policies = ["discuss", "ask", "auto"].map(mode => ({ id: `builtin-tool-${mode}-v1`, name: mode, kind: "tool", version: 1, config: { mode }, status: "active", createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" }));
const main = { id: "main-branch", sessionId: "branch-session", label: "Main", leafMessageId: "main-leaf", messageCount: 3, active: true, createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:00Z" };
const alternate = { id: "alternate-branch", sessionId: "branch-session", parentBranchId: main.id, forkMessageId: "root", label: "Branch 2", leafMessageId: "alternate-leaf", messageCount: 2, active: false, createdAt: "2026-07-28T00:00:01Z", updatedAt: "2026-07-28T00:00:01Z" };

function session(activeBranchId = main.id, activeLeafMessageId = main.leafMessageId) {
  return { id: "branch-session", projectId: project.id, title: "Lineage review", status: "active", activeBranchId,
    activeLeafMessageId, createdAt: "2026-07-28T00:00:00Z", updatedAt: "2026-07-28T00:00:03Z" };
}

function message(id: string, parentMessageId: string | undefined, role: "user" | "assistant", text: string) {
  return { id, sessionId: "branch-session", parentMessageId, role, status: "complete", parts: [{ type: "text", text }], createdAt: "2026-07-28T00:00:00Z" };
}

async function fulfill(route: Route, data: unknown, status = 200) {
  await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function openSession(page: Page) {
  await page.goto("/");
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByTitle("Select project").click();
  await page.getByRole("button", { name: project.name }).click();
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: "Lineage review", exact: true }).click();
}

test("branch switching rejects a late response from the previously active lineage", async ({ page }) => {
  let active = main.id;
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session()]);
    if (path === "/v1/sessions/branch-session") {
      const branch = active === main.id ? main : alternate;
      return fulfill(route, session(branch.id, branch.leafMessageId));
    }
    if (path.endsWith("/active-run")) return fulfill(route, null);
    if (path.endsWith("/recovery")) return fulfill(route, null);
    if (path.endsWith("/compactions")) return fulfill(route, []);
    if (path.endsWith("/branches") && route.request().method() === "GET") {
      return fulfill(route, [
        { ...main, active: active === main.id },
        { ...alternate, active: active === alternate.id },
      ]);
    }
    if (path.endsWith(`/branches/${alternate.id}/activate`)) {
      active = alternate.id;
      return fulfill(route, { session: session(alternate.id, alternate.leafMessageId), branches: [
        { ...main, active: false }, { ...alternate, active: true },
      ] });
    }
    if (path.endsWith(`/branches/${main.id}/activate`)) {
      active = main.id;
      return fulfill(route, { session: session(main.id, main.leafMessageId), branches: [
        { ...main, active: true }, { ...alternate, active: false },
      ] });
    }
    if (path.endsWith("/messages")) {
      const branchAtRequest = active;
      if (branchAtRequest === alternate.id) await new Promise(resolve => setTimeout(resolve, 300));
      const values = branchAtRequest === main.id
        ? [message("root", undefined, "user", "shared root"), message("main-leaf", "root", "assistant", "main branch response")]
        : [message("root", undefined, "user", "shared root"), message("alternate-leaf", "root", "assistant", "stale alternate response")];
      return fulfill(route, { messages: values, hasMore: false, activeLeafMessageId: values.at(-1)?.id });
    }
    return route.abort();
  });

  await openSession(page);
  await expect(page.getByText("main branch response", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Choose conversation branch" }).click();
  await page.getByRole("menuitemradio", { name: /Branch 2/ }).click();
  await expect(page.getByRole("button", { name: "Choose conversation branch" })).toContainText("Branch 2");
  await page.getByRole("button", { name: "Choose conversation branch" }).click();
  await page.getByRole("menuitemradio", { name: /Main/ }).click();
  await expect(page.getByText("main branch response", { exact: true })).toBeVisible();
  await page.waitForTimeout(400);
  await expect(page.getByText("stale alternate response", { exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});

test("a historical message creates and activates a new branch", async ({ page }) => {
  let currentSession = session();
  let branchList = [main];
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [currentSession]);
    if (path === "/v1/sessions/branch-session") return fulfill(route, currentSession);
    if (path.endsWith("/active-run") || path.endsWith("/recovery")) return fulfill(route, null);
    if (path.endsWith("/compactions")) return fulfill(route, []);
    if (path.endsWith("/branches") && route.request().method() === "GET") return fulfill(route, branchList);
    if (path.endsWith("/branches") && route.request().method() === "POST") {
      const body = route.request().postDataJSON() as { fromMessageId: string };
      expect(body.fromMessageId).toBe("root");
      const created = { ...alternate, id: "created-branch", leafMessageId: "root", messageCount: 1, active: true };
      branchList = [{ ...main, active: false }, created];
      currentSession = session(created.id, "root");
      return fulfill(route, { session: currentSession, branches: branchList }, 201);
    }
    if (path.endsWith("/messages")) {
      const values = currentSession.activeBranchId === main.id
        ? [message("root", undefined, "user", "branch from this point"), message("main-leaf", "root", "assistant", "later answer")]
        : [message("root", undefined, "user", "branch from this point")];
      return fulfill(route, { messages: values, hasMore: false, activeLeafMessageId: currentSession.activeLeafMessageId });
    }
    return route.abort();
  });

  await openSession(page);
  await page.getByRole("button", { name: "Branch from this message" }).click();
  await expect(page.getByRole("button", { name: "Choose conversation branch" })).toContainText("Branch 2");
  await expect(page.getByText("later answer", { exact: true })).toHaveCount(0);
});

test("a safe failed run can be retried from the recovery bar", async ({ page }) => {
  const failedRun = { id: "failed-run", turnId: "turn", sessionId: "branch-session", runKind: "agent", baseMessageId: "failed-user",
    attempt: 1, status: "failed", requestedConfig: {}, effectiveConfig: {}, errorCode: "provider_unavailable", createdAt: "2026-07-28T00:00:00Z" };
  const retryRun = { ...failedRun, id: "retry-run", attempt: 2, status: "queued", retryOfRunId: failedRun.id };
  let retries = 0;
  let completed = false;
  await page.route("**/api/worker/v1/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/policy-profiles") return fulfill(route, policies);
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session(main.id, "failed-user")]);
    if (path === "/v1/sessions/branch-session") return fulfill(route, session(main.id, completed ? "recovered" : "failed-user"));
    if (path.endsWith("/branches")) return fulfill(route, [{ ...main, leafMessageId: completed ? "recovered" : "failed-user" }]);
    if (path.endsWith("/active-run")) return fulfill(route, null);
    if (path.endsWith("/compactions")) return fulfill(route, []);
    if (path.endsWith("/recovery")) return fulfill(route, completed ? null : { run: failedRun, retryable: true });
    if (path === `/v1/runs/${failedRun.id}/retry`) {
      retries += 1;
      return fulfill(route, { sourceRunId: failedRun.id, run: retryRun, existing: false }, 202);
    }
    if (path === `/v1/runs/${retryRun.id}/events`) {
      completed = true;
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: `data: ${JSON.stringify({ type: "run_succeeded", payload: {} })}\n\n` });
    }
    if (path.endsWith("/messages")) return fulfill(route, { messages: completed
      ? [message("failed-user", undefined, "user", "recover this"), message("recovered", "failed-user", "assistant", "retry completed")]
      : [message("failed-user", undefined, "user", "recover this")], hasMore: false });
    return route.abort();
  });

  await openSession(page);
  await expect(page.getByTestId("run-recovery")).toContainText("can be retried safely");
  await page.getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByText("retry completed", { exact: true })).toBeVisible();
  expect(retries).toBe(1);
  await expect(page.getByTestId("run-recovery")).toHaveCount(0);
});

test.describe("mobile branch controls", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("header and history controls remain within the viewport", async ({ page }) => {
    await page.route("**/api/worker/v1/**", async route => {
      const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
      if (path === "/v1/projects") return fulfill(route, [project]);
      if (path === "/v1/policy-profiles") return fulfill(route, policies);
      if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, [session()]);
      if (path === "/v1/sessions/branch-session") return fulfill(route, session());
      if (path.endsWith("/branches")) return fulfill(route, [main, alternate]);
      if (path.endsWith("/active-run") || path.endsWith("/recovery")) return fulfill(route, null);
      if (path.endsWith("/compactions")) return fulfill(route, []);
      if (path.endsWith("/messages")) return fulfill(route, { messages: [message("root", undefined, "user", "mobile root"), message("main-leaf", "root", "assistant", "mobile answer")], hasMore: false });
      return route.abort();
    });
    await openSession(page);
    await expect(page.getByRole("button", { name: "Choose conversation branch" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Branch from this message" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
    await page.screenshot({ path: "/tmp/ennote-branch-mobile.png", fullPage: true });
  });
});
