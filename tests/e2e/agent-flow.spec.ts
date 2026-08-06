import { expect, test, type Page, type Route } from "@playwright/test";

const now = "2026-08-05T00:00:00Z";
const project = { id: "flow-project", name: "RNA screen", description: "", status: "active", createdAt: now, updatedAt: now };
const profile = { id: "flow-profile", name: "Go Review", slug: "go-review", sourceKind: "managed",
  projectScope: null, sourceLocator: "", lifecycleStatus: "active", createdAt: now, updatedAt: now, latestVersion: 1,
  draftRevision: 1 };
const version = { id: "flow-v1", profileId: profile.id, version: 1,
  configDigest: "a".repeat(64),
  definition: { schemaVersion: 1, id: "go-review", budget: { maxTotalTokens: 120000 },
    tasks: { producer: { role: "flow-worker@1", goal: "Implement {inputs.target}" } } },
  publishedAt: now };
const role = { id: "role-1", handle: "flow-worker", name: "Flow Worker", description: "", positioning: "",
  icon: "bot", color: "neutral", scope: "global", projectId: null, status: "active",
  currentVersionId: "rv1", currentVersion: 1, updatedAt: now };
const binding = { id: "flow-binding", projectId: project.id, flowVersionId: version.id, desiredEnabled: false,
  revision: 1, createdAt: now, updatedAt: now };
const flowRun = { runId: "flow-run-1", sessionId: "session-1", projectId: project.id, flowVersionId: version.id,
  manifestDigest: "m".repeat(64), state: "running", totalTokensUsed: 0, inputs: {}, createdAt: now, updatedAt: now };
const runDetail = {
  run: { ...flowRun, state: "completed", totalTokensUsed: 1500 },
  nodes: [
    { runId: flowRun.runId, taskIndex: 0, handle: "producer", roleVersionId: "rv1", skillDigests: [],
      goalDigest: "g".repeat(64), goalText: "Implement src/main.go", terminalState: "completed",
      outputRef: { changedFiles: ["a.go"] }, childRunId: "child-1", createdAt: now },
    { runId: flowRun.runId, taskIndex: 1, handle: "accept", terminalState: "completed", createdAt: now },
  ],
  flowVersion: 1,
};

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function selectProjectAndOpenFlows(page: Page) {
  await page.goto("/");
  if ((page.viewportSize()?.width ?? 1280) <= 640) await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByTitle("Select project").click();
  await page.getByRole("button", { name: project.name }).click();
  await page.getByRole("button", { name: "Open settings" }).click();
  await page.getByRole("tab", { name: /Flows/ }).click();
}

async function mockFlows(page: Page) {
  const bindings: typeof binding[] = [];
  const runs: typeof flowRun[] = [];
  let published = false;
  await page.route("**/api/worker/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace("/api/worker", "");
    if (path === "/v1/projects") return fulfill(route, [project]);
    if (path === "/v1/provider-profiles" || path === "/v1/model-profiles" || path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/roles") return fulfill(route, { items: [role] });
    if (path === `/v1/projects/${project.id}/sessions`) return fulfill(route, []);
    if (path === "/v1/agent-flows") {
      if (route.request().method() === "POST") return fulfill(route, { ...profile, draftRevision: 0 }, 201);
      return fulfill(route, [profile]);
    }
    if (path === `/v1/agent-flows/${profile.id}`) return fulfill(route, profile);
    if (path === `/v1/agent-flows/${profile.id}/draft`) {
      const body = JSON.parse(route.request().postData() ?? "{}");
      profile.draftRevision = (body.expectedRevision ?? 0) + 1;
      return fulfill(route, profile);
    }
    if (path === `/v1/agent-flows/${profile.id}/validate`) return fulfill(route, { valid: true, diagnostics: [] });
    if (path === `/v1/agent-flows/${profile.id}/publish`) {
      published = true;
      return fulfill(route, version, 201);
    }
    if (path === `/v1/agent-flows/${profile.id}/versions`) return fulfill(route, published ? [version] : []);
    if (path === `/v1/projects/${project.id}/agent-flows/candidates`) return fulfill(route, []);
    if (path === `/v1/projects/${project.id}/agent-flows/bindings`) {
      if (route.request().method() === "POST") {
        bindings.push(binding);
        return fulfill(route, binding, 201);
      }
      return fulfill(route, bindings);
    }
    if (path === `/v1/projects/${project.id}/agent-flows/bindings/${binding.id}`) {
      const body = JSON.parse(route.request().postData() ?? "{}");
      if (typeof body.desiredEnabled === "boolean") binding.desiredEnabled = body.desiredEnabled;
      return fulfill(route, binding);
    }
    if (path === `/v1/projects/${project.id}/agent-flows/bindings/${binding.id}/run`) {
      const created = { ...flowRun };
      runs.push(created);
      return fulfill(route, created, 201);
    }
    if (path === `/v1/projects/${project.id}/agent-flows/runs`) return fulfill(route, runs);
    if (path === `/v1/projects/${project.id}/agent-flows/runs/${flowRun.runId}`) return fulfill(route, runDetail);
    if (path === `/v1/projects/${project.id}/agent-flows/runs/${flowRun.runId}/cancel`) return fulfill(route, flowRun);
    if (path === `/v1/projects/${project.id}/agent-flows/check-approvals`) return fulfill(route, []);
    if (path === `/v1/runs/${flowRun.runId}/events`) return fulfill(route, []);
    return route.abort();
  });
}

for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
  test(`Agent Flow editor, publish, bind, and timeline at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    page.on("dialog", (dialog) => {
      if (dialog.message().includes("Run in which session")) {
        void dialog.accept("session-1");
      } else {
        void dialog.accept();
      }
    });
    await mockFlows(page);
    await selectProjectAndOpenFlows(page);

    // New flow -> editor opens with the first (entry) task.
    await page.getByRole("button", { name: "+ New flow" }).click();
    await page.getByPlaceholder("Go Change Review").fill("Go Review");
    await page.getByPlaceholder("go-change-review").fill("go-review");
    await page.getByRole("button", { name: "Create flow" }).click();
    await expect(page.getByText("Edit go-review")).toBeVisible();
    await expect(page.getByText("entry", { exact: true })).toBeVisible();

    // Add a second role task with a depends-selectable previous task and
    // autocomplete chips for inputs / task outputs / flow vars (no prev.*).
    await page.getByRole("button", { name: "+ Add task" }).click();
    await expect(page.getByText("Dependency view")).toBeVisible();

    // Auto budget button computes a suggestion from task budgets.
    await page.getByTitle(/Suggested = 1.25/).scrollIntoViewIfNeeded();
    await page.getByTitle(/Suggested = 1.25/).click();
    const budgetValue = await page.locator('input[placeholder="e.g. 600000"]').inputValue();
    expect(Number(budgetValue)).toBeGreaterThanOrEqual(10000);

    // Save + publish the draft.
    await page.getByRole("button", { name: "Save draft" }).click();
    await page.getByRole("button", { name: "Publish" }).click();

    // Bind the published version -> binding appears Disabled.
    await page.getByRole("button", { name: /Bind v1/ }).click();
    await expect(page.getByRole("button", { name: "Disabled" })).toBeVisible();

    // Enable + run + timeline.
    await page.getByRole("button", { name: "Disabled" }).click();
    await expect(page.getByRole("button", { name: "Enabled" })).toBeVisible();
    await page.getByRole("button", { name: "Run", exact: true }).click();
    await expect(page.getByText("flow-run-1".slice(0, 8))).toBeVisible();
    await page.getByRole("button", { name: "Timeline" }).click();
    await expect(page.getByText("Task checkpoints")).toBeVisible();
    await expect(page.getByText("producer", { exact: true })).toBeVisible();
    await expect(page.getByText(/producer · completed/)).toBeVisible();

    // No horizontal overflow at either viewport.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow).toBe(false);
  });
}
