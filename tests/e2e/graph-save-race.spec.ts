import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillHarnessRoutes } from "./harness";

// The Graph editor saves by draining an op queue, not by diffing a document. These
// tests pin the four things that model promises: an edit made while a save is in
// flight still lands, a failure does not retry itself in a loop, a conflict is put
// to the reader instead of silently overwritten, and a reload of the Graph already
// open does not adopt the disk definition over unsent edits.

const now = "2026-08-07T00:00:00Z";
const provider = { id: "provider-1", name: "Local", type: "openai", baseUrl: "http://127.0.0.1:1", apiKeySet: true, status: "active", createdAt: now, updatedAt: now };
const model = { id: "model-1", providerId: provider.id, modelName: "gpt-test", displayName: "Test model", contextTokens: 8000, maxOutputTokens: 1000, status: "active", createdAt: now, updatedAt: now };

function graphDefinition(goal = "Align reads") {
  return {
    schemaVersion: 1,
    id: "rna-seq",
    name: "RNA-seq",
    budget: { maxTotalTokens: 200000 },
    tasks: { align_1: { hero: "global/aligner", goal } },
  };
}

type MockState = {
  definition: ReturnType<typeof graphDefinition>;
  digest: string;
  patchBodies: Record<string, unknown>[];
  patchCalls: number;
  /** Statuses handed to successive PATCHes; the last one repeats. */
  statuses: number[];
  delayMs: number;
};

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

function fail(route: Route, status: number, code: string, message: string) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ error: { code, message } }) });
}

async function mockGraph(page: Page, options: { delayMs?: number; statuses?: number[] } = {}) {
  const state: MockState = {
    definition: graphDefinition(),
    digest: "sha256:" + "a".repeat(64),
    patchBodies: [],
    patchCalls: 0,
    statuses: options.statuses ?? [],
    delayMs: options.delayMs ?? 0,
  };

  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/graphs" && method === "GET") {
      return fulfill(route, [{ id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 1 }]);
    }
    if (path === "/v1/graphs/rna-seq" && method === "GET") {
      return fulfill(route, { id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 1, definition: state.definition });
    }
    if (path === "/v1/graphs/rna-seq/bindings") return fulfill(route, []);
    if (path === "/v1/graphs/rna-seq/versions") {
      return fulfill(route, [{ id: "graph-v1", resourceId: "rna-seq", sourceKind: "graph", version: 1, digest: "sha256:" + "c".repeat(64), publishedAt: now }]);
    }
    if (path === "/v1/graphs/rna-seq/publish") {
      return fulfill(route, { id: "graph-v2", resourceId: "rna-seq", sourceKind: "graph", version: 2, digest: "sha256:" + "d".repeat(64), publishedAt: now }, 201);
    }
    if (path === "/v1/graphs/rna-seq/builder" && method === "GET") {
      return fulfill(route, { graphId: "rna-seq", modelProfileId: model.id, messages: [] });
    }
    if (path === "/v1/graphs/rna-seq" && method === "PATCH") {
      state.patchCalls += 1;
      const body = JSON.parse(route.request().postData() ?? "{}") as Record<string, unknown>;
      state.patchBodies.push(body);
      const status = state.statuses.length > 1 ? state.statuses.shift()! : state.statuses[0] ?? 200;
      if (state.delayMs > 0) await new Promise((resolve) => setTimeout(resolve, state.delayMs));
      if (status === 409) return fail(route, 409, "source_digest_conflict", "graph.yaml changed since it was loaded; reload before saving");
      if (status !== 200) return fail(route, status, "internal_error", "the Worker could not save the Graph");
      const task = body.task as { id: string; value: Record<string, unknown> | null } | undefined;
      const tasks = { ...state.definition.tasks } as Record<string, Record<string, unknown>>;
      if (task) {
        if (task.value === null) delete tasks[task.id];
        else tasks[task.id] = task.value;
      }
      state.definition = { ...state.definition, tasks, name: (body.name as string | undefined) ?? state.definition.name } as MockState["definition"];
      state.digest = "sha256:" + String(state.patchCalls).padStart(64, "b");
      return fulfill(route, { id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 1, definition: state.definition });
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

async function expandGoal(page: Page) {
  const task = page.locator('[data-task-id="align_1"]');
  if (await task.locator(".graph-task-form").count() === 0) await task.locator(".graph-task-summary").click();
  return task.getByLabel("Goal");
}

const saveState = (page: Page) => page.locator(".graph-save-state");

test("an edit typed while a save is in flight is still saved", async ({ page }) => {
  const state = await mockGraph(page, { delayMs: 400 });
  await openGraphs(page);

  const goal = await expandGoal(page);
  await goal.fill("first goal");
  // Wait past the debounce so the first PATCH is actually in flight…
  await expect.poll(() => state.patchCalls).toBe(1);
  // …then edit again while it is.
  await goal.fill("second goal");

  // The queued edit goes out in a second round, on top of what the first one returned.
  await expect.poll(() => state.patchCalls).toBe(2);
  await expect.poll(() => state.definition.tasks.align_1.goal).toBe("second goal");
  await expect(saveState(page)).toContainText("Draft saved");
  await expect(goal).toHaveValue("second goal");
});

test("a failed save does not retry itself and offers an explicit retry", async ({ page }) => {
  const state = await mockGraph(page, { statuses: [500, 200] });
  await openGraphs(page);

  const goal = await expandGoal(page);
  await goal.fill("will fail once");

  const alert = page.locator(".graph-save-error");
  await expect(alert).toBeVisible();
  // One request, no backoff loop: a failure stops the drain.
  await page.waitForTimeout(800);
  expect(state.patchCalls).toBe(1);

  await alert.getByRole("button", { name: "Retry save" }).click();
  await expect.poll(() => state.patchCalls).toBe(2);
  await expect(alert).toBeHidden();
  await expect(saveState(page)).toContainText("Draft saved");
});

test("a digest conflict is put to the reader, not overwritten", async ({ page }) => {
  const state = await mockGraph(page, { statuses: [409] });
  await openGraphs(page);

  const goal = await expandGoal(page);
  await goal.fill("mine");
  const bar = page.getByRole("status").filter({ hasText: "The file changed on disk." });
  await expect(bar).toBeVisible();
  await expect(page.getByRole("button", { name: "Publish", exact: true })).toBeDisabled();

  // Discarding mine adopts the disk definition; nothing of mine is left.
  await bar.getByRole("button", { name: "Discard mine" }).click();
  await expect(bar).toBeHidden();
  await expect(goal).toHaveValue("Align reads");
  await expect(saveState(page)).toContainText("Draft saved");
  expect(state.definition.tasks.align_1.goal).toBe("Align reads");
});

test("reloading the open Graph while edits are unsent asks instead of discarding", async ({ page }) => {
  await mockGraph(page, { delayMs: 1200 });
  await openGraphs(page);

  const goal = await expandGoal(page);
  await goal.fill("unsent edit");
  await expect(page.locator(".graph-save-state.is-saving")).toBeVisible();

  // Clicking the tab of the Graph already open re-reads it, which is the same
  // path a Builder apply takes (onApplied -> openGraph(sameId)).
  await page.getByRole("tab", { name: "RNA-seq" }).click();

  await expect(page.locator(".graph-conflict-bar")).toContainText("This Graph changed while you had unsaved edits.");
  // Nothing was adopted over the edit that had not been sent yet.
  await expect(goal).toHaveValue("unsent edit");
});
