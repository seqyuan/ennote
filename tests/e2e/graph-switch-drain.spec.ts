import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillHarnessRoutes } from "./harness";

// Switching Graphs replaces the editor's save session wholesale, so everything an
// unsent queue and an in-flight save were protecting has to be settled before the
// switch, not after it. These tests pin the three ways that can go wrong: an edit
// dropped with the session, a switch that silently loses a save the Worker
// refused, and a publish that lands on the Graph the reader switched to.

const now = "2026-08-07T00:00:00Z";
const provider = { id: "provider-1", name: "Local", type: "openai", baseUrl: "http://127.0.0.1:1", apiKeySet: true, status: "active", createdAt: now, updatedAt: now };
const model = { id: "model-1", providerId: provider.id, modelName: "gpt-test", displayName: "Test model", contextTokens: 8000, maxOutputTokens: 1000, status: "active", createdAt: now, updatedAt: now };

type GraphID = "rna-seq" | "qc";

type Definition = {
  schemaVersion: number;
  id: GraphID;
  name: string;
  budget: { maxTotalTokens: number };
  tasks: Record<string, { hero: string; goal: string }>;
};

function definition(id: GraphID, goal: string): Definition {
  const task = id === "rna-seq" ? "align_1" : "qc_1";
  return {
    schemaVersion: 1,
    id,
    name: id === "rna-seq" ? "RNA-seq" : "QC",
    budget: { maxTotalTokens: 200000 },
    tasks: { [task]: { hero: "global/aligner", goal } },
  };
}

type MockState = {
  definitions: Record<GraphID, Definition>;
  digests: Record<GraphID, string>;
  patchBodies: Record<GraphID, Record<string, unknown>[]>;
  patchStatus: number;
  patchDelayMs: number;
  publishDelayMs: number;
  published: Record<GraphID, number>;
  publishAnswers: number;
};

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

function digestOf(id: GraphID, salt: string) {
  return `sha256:${id === "rna-seq" ? "a" : "e"}`.padEnd(7 + 64, salt);
}

async function mockTwoGraphs(page: Page, options: { patchStatus?: number; patchDelayMs?: number; publishDelayMs?: number } = {}) {
  const state: MockState = {
    definitions: { "rna-seq": definition("rna-seq", "Align reads"), qc: definition("qc", "Check quality") },
    digests: { "rna-seq": digestOf("rna-seq", "a"), qc: digestOf("qc", "e") },
    patchBodies: { "rna-seq": [], qc: [] },
    patchStatus: options.patchStatus ?? 200,
    patchDelayMs: options.patchDelayMs ?? 0,
    publishDelayMs: options.publishDelayMs ?? 0,
    published: { "rna-seq": 1, qc: 1 },
    publishAnswers: 0,
  };
  const graphID = (path: string): GraphID | null => {
    const match = path.match(/^\/v1\/graphs\/([^/]+)/);
    return match && (match[1] === "rna-seq" || match[1] === "qc") ? match[1] as GraphID : null;
  };
  const detail = (id: GraphID) => ({
    id, name: state.definitions[id].name, path: `/home/graphs/${id}/graph.yaml`,
    digest: state.digests[id], latestVersion: state.published[id], definition: state.definitions[id],
  });

  await page.route("**/api/worker/v1/**", async (route) => {
    if (await tryFulfillHarnessRoutes(route)) return;
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/graphs" && method === "GET") {
      return fulfill(route, (["rna-seq", "qc"] as GraphID[]).map((id) => ({
        id, name: state.definitions[id].name, path: `/home/graphs/${id}/graph.yaml`,
        digest: state.digests[id], latestVersion: state.published[id],
      })));
    }
    const id = graphID(path);
    if (id === null) return route.abort();
    if (path === `/v1/graphs/${id}/bindings`) return fulfill(route, []);
    if (path === `/v1/graphs/${id}/builder` && method === "GET") {
      return fulfill(route, { graphId: id, modelProfileId: model.id, messages: [] });
    }
    if (path === `/v1/graphs/${id}/versions`) {
      return fulfill(route, [{ id: `graph-${id}-v${state.published[id]}`, resourceId: id, sourceKind: "graph",
        version: state.published[id], digest: state.digests[id], publishedAt: now }]);
    }
    if (path === `/v1/graphs/${id}/publish`) {
      state.published[id] += 1;
      if (state.publishDelayMs > 0) await new Promise((resolve) => setTimeout(resolve, state.publishDelayMs));
      state.publishAnswers += 1;
      return fulfill(route, { id: `graph-${id}-v${state.published[id]}`, resourceId: id, sourceKind: "graph",
        version: state.published[id], digest: digestOf(id, "d"), publishedAt: now }, 201);
    }
    if (path === `/v1/graphs/${id}` && method === "GET") return fulfill(route, detail(id));
    if (path === `/v1/graphs/${id}` && method === "PATCH") {
      const body = JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>;
      state.patchBodies[id].push(body);
      if (state.patchDelayMs > 0) await new Promise((resolve) => setTimeout(resolve, state.patchDelayMs));
      if (state.patchStatus !== 200) {
        return route.fulfill({ status: state.patchStatus, contentType: "application/json",
          body: JSON.stringify({ error: { code: "internal_error", message: "the Worker could not save the Graph" } }) });
      }
      const task = body.task as { id: string; value: Record<string, unknown> | null } | undefined;
      const tasks = { ...state.definitions[id].tasks } as Record<string, Record<string, unknown>>;
      if (task) {
        if (task.value === null) delete tasks[task.id];
        else tasks[task.id] = task.value;
      }
      state.definitions[id] = { ...state.definitions[id], tasks: tasks as Definition["tasks"],
        name: (body.name as string | undefined) ?? state.definitions[id].name };
      state.digests[id] = digestOf(id, "b");
      return fulfill(route, detail(id));
    }
    return route.abort();
  });

  return state;
}

async function openGraphs(page: Page) {
  const secret = process.env.ENNOTE_E2E_PASSWORD ?? "preview1234";
  await page.goto("/");
  const headers = { Origin: new URL(page.url()).origin };
  const statusResponse = await page.request.get("/api/auth/status");
  if (statusResponse.ok() && statusResponse.headers()["content-type"]?.includes("application/json")) {
    const status = await statusResponse.json() as { requiresPassword: boolean; authenticated: boolean };
    if (!status.requiresPassword) await page.request.post("/api/auth/setup", { data: { password: secret }, headers });
    if (!status.authenticated) {
      const login = await page.request.post("/api/auth/login", { data: { password: secret }, headers });
      expect(login.ok()).toBe(true);
    }
  }
  await page.goto("/graphs");
  await expect(page.locator('[data-task-id="align_1"]')).toBeVisible();
}

const tab = (page: Page, name: string) => page.getByRole("tab", { name });
const saveState = (page: Page) => page.locator(".graph-save-state");

async function goalInput(page: Page, taskID: string) {
  const task = page.locator(`[data-task-id="${taskID}"]`);
  await expect(task).toBeVisible();
  if (await task.locator(".graph-task-form").count() === 0) await task.locator(".graph-task-summary").click();
  return task.getByLabel("Goal");
}

async function editGoal(page: Page, taskID: string, value: string) {
  await (await goalInput(page, taskID)).fill(value);
}

test("an edit made just before a switch reaches the Graph it belongs to", async ({ page }) => {
  const state = await mockTwoGraphs(page);
  await openGraphs(page);

  await editGoal(page, "align_1", "edited then switched");
  // Switch inside the 300ms save debounce: the queue is the only copy of the edit.
  await tab(page, "QC").click();
  await expect(page.locator('[data-task-id="qc_1"]')).toBeVisible();

  expect(state.patchBodies["rna-seq"]).toHaveLength(1);
  const task = state.patchBodies["rna-seq"][0].task as { value: { goal: string } };
  expect(task.value.goal).toBe("edited then switched");

  // And the edit is still there when the reader comes back, because it landed.
  await tab(page, "RNA-seq").click();
  await expect(await goalInput(page, "align_1")).toHaveValue("edited then switched");
  await expect(saveState(page)).toContainText("Draft saved");
});

test("a switch is refused rather than dropping a save the Worker rejected", async ({ page }) => {
  await mockTwoGraphs(page, { patchStatus: 500 });
  await openGraphs(page);

  await editGoal(page, "align_1", "cannot be saved");
  await tab(page, "QC").click();

  // The editor stays on the Graph with the unsent queue and says why: the
  // alternative is switching and letting the session take the edit with it.
  await expect(page.locator('[data-task-id="align_1"]')).toBeVisible();
  await expect(page.locator(".graph-save-error")).toContainText("the Worker could not save the Graph");
  await expect(saveState(page)).toContainText("Unsaved changes");
  await expect(tab(page, "RNA-seq")).toHaveAttribute("aria-selected", "true");
});

test("a publish that lands after a switch does not become the other Graph's revision", async ({ page }) => {
  const state = await mockTwoGraphs(page, { publishDelayMs: 600 });
  await openGraphs(page);

  await page.getByRole("button", { name: /^Publish/ }).click();
  // Switch while the publish is on the wire.
  await tab(page, "QC").click();
  await expect(page.locator('[data-task-id="qc_1"]')).toBeVisible();

  // The point is the answer that arrives *after* the switch, so wait for it to go
  // out and give React a beat to render it before reading the header.
  await expect.poll(() => state.publishAnswers, { timeout: 5000 }).toBe(1);
  await page.waitForTimeout(150);

  // QC's header keeps its own revision: RNA-seq's new version is RNA-seq's.
  const identity = page.locator(".graph-identity");
  await expect(identity).toContainText("v1");
  await expect(identity).not.toContainText("v2");
});
