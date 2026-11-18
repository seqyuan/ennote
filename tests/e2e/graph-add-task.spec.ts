import { expect, test, type Page, type Route } from "@playwright/test";
import { tryFulfillHarnessRoutes } from "./harness";

// Add Task is the Graph editor's front door: without it a reader cannot get from
// an empty Graph to a published one without hand-writing graph.yaml. Two Hero
// sources exist — a shared Hero referenced by handle, and a Graph-local Hero
// created by the same mutation that creates the Task — and these tests pin both,
// plus the editor for the local Hero's own document.

const now = "2026-08-07T00:00:00Z";
const provider = { id: "provider-1", name: "Local", type: "openai", baseUrl: "http://127.0.0.1:1", apiKeySet: true, status: "active", createdAt: now, updatedAt: now };
const model = { id: "model-1", providerId: provider.id, modelName: "gpt-test", displayName: "Test model", contextTokens: 8000, maxOutputTokens: 1000, status: "active", createdAt: now, updatedAt: now };
const sharedHero = { id: "reviewer", name: "Reviewer", path: "/home/agents/heroes/reviewer/hero.md", digest: "sha256:" + "9".repeat(64) };

type GraphPatch = {
  task?: { id: string; value: Record<string, unknown> };
  localHero?: Record<string, unknown> & { handle: string; name: string };
};

type HeroPatch = { expectedDigest?: string; document: Record<string, unknown> };

function emptyGraph() {
  return { schemaVersion: 1, id: "rna-seq", name: "RNA-seq", budget: { maxTotalTokens: 200000 }, tasks: {} as Record<string, unknown> };
}

function fulfill(route: Route, data: unknown, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ data }) });
}

async function mockAddTask(page: Page) {
  const state = {
    definition: emptyGraph(),
    digest: "sha256:" + "a".repeat(64),
    graphPatches: [] as GraphPatch[],
    heroPatches: [] as HeroPatch[],
    hero: null as null | { id: string; name: string; path: string; digest: string; document: Record<string, unknown> },
    /** How many Hero PATCHes to refuse as "the file moved" before accepting one. */
    heroConflicts: 0,
  };

  await page.route("**/api/worker/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname.replace("/api/worker", "");
    if (await tryFulfillHarnessRoutes(route)) return;
    const method = route.request().method();
    if (path === "/v1/projects") return fulfill(route, []);
    if (path === "/v1/provider-profiles") return fulfill(route, [provider]);
    if (path === "/v1/model-profiles") return fulfill(route, [model]);
    if (path === "/v1/policy-profiles") return fulfill(route, []);
    if (path === "/v1/global-heroes") return fulfill(route, [sharedHero]);
    if (path === "/v1/graphs" && method === "GET") {
      return fulfill(route, [{ id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 0 }]);
    }
    if (path === "/v1/graphs/rna-seq" && method === "GET") {
      return fulfill(route, { id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 0, definition: state.definition });
    }
    if (path === "/v1/graphs/rna-seq/bindings") return fulfill(route, []);
    if (path === "/v1/graphs/rna-seq/versions") return fulfill(route, []);
    if (path === "/v1/graphs/rna-seq" && method === "PATCH") {
      const body = JSON.parse(route.request().postData() ?? "{}") as GraphPatch;
      state.graphPatches.push(body);
      const task = body.task as { id: string; value: Record<string, unknown> } | undefined;
      if (task) state.definition = { ...state.definition, tasks: { ...state.definition.tasks, [task.id]: task.value } };
      if (body.localHero) {
        state.hero = {
          id: body.localHero.handle, name: body.localHero.name,
          path: `/home/graphs/rna-seq/heroes/${body.localHero.handle}/hero.md`,
          digest: "sha256:" + "c".repeat(64), document: body.localHero,
        };
      }
      state.digest = "sha256:" + String(state.graphPatches.length).padStart(64, "b");
      return fulfill(route, { id: "rna-seq", name: state.definition.name, path: "/home/graphs/rna-seq/graph.yaml", digest: state.digest, latestVersion: 0, definition: state.definition });
    }
    const heroPath = /^\/v1\/graphs\/rna-seq\/heroes\/(.+)$/.exec(path);
    if (heroPath) {
      if (!state.hero || state.hero.id !== heroPath[1]) {
        return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { code: "hero_not_found", message: "missing" } }) });
      }
      if (method === "PATCH") {
        const body = JSON.parse(route.request().postData() ?? "{}") as HeroPatch;
        if (state.heroConflicts > 0) {
          state.heroConflicts -= 1;
          // Somebody else wrote the file: the digest on disk is no longer ours.
          state.hero = { ...state.hero, digest: "sha256:" + "e".repeat(64) };
          return route.fulfill({ status: 409, contentType: "application/json",
            body: JSON.stringify({ error: { code: "source_digest_conflict", message: "the file changed since it was loaded; reload before saving" } }) });
        }
        state.heroPatches.push(body);
        state.hero = { ...state.hero, document: body.document, digest: "sha256:" + "d".repeat(64) };
      }
      return fulfill(route, state.hero);
    }
    // The Builder panel fetches its thread when the editor mounts.
    if (path === "/v1/graphs/rna-seq/builder") return fulfill(route, { graphId: "rna-seq", modelProfileId: model.id, messages: [] });
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
  await expect(page.getByRole("button", { name: "Add Task" })).toBeVisible();
}

test("Add Task creates a Task and its Graph-local Hero in one save", async ({ page }) => {
  const state = await mockAddTask(page);
  await openGraphs(page);

  await page.getByRole("button", { name: "Add Task" }).click();
  await page.getByPlaceholder("task_id").fill("align");
  await page.getByRole("button", { name: "Add", exact: true }).click();

  // One PATCH carries both halves, so the reference can never dangle.
  await expect.poll(() => state.graphPatches.length).toBe(1);
  expect(state.graphPatches[0].task).toEqual({ id: "align", value: { hero: "local/align", goal: expect.any(String) } });
  expect(state.graphPatches[0].localHero).toMatchObject({ handle: "align", context: { defaultMode: "task_only" } });
  await expect(page.locator('[data-task-id="align"]')).toBeVisible();
});

test("the local Hero panel edits the document the Task runs", async ({ page }) => {
  const state = await mockAddTask(page);
  await openGraphs(page);

  await page.getByRole("button", { name: "Add Task" }).click();
  await page.getByPlaceholder("task_id").fill("review");
  await page.getByRole("button", { name: "Add", exact: true }).click();

  const task = page.locator('[data-task-id="review"]');
  await expect(task.locator(".graph-local-hero")).toBeVisible({ timeout: 5_000 });
  await task.getByLabel("Prompt").fill("Check the QC report.");
  await expect.poll(() => state.heroPatches.length).toBeGreaterThan(0);
  expect(state.heroPatches.at(-1)?.document).toMatchObject({ prompt: "Check the QC report." });
  await expect(task.locator(".graph-local-hero")).toContainText("Saved");
});

test("a Hero save the Worker refused keeps the text and offers the choice", async ({ page }) => {
  const state = await mockAddTask(page);
  state.heroConflicts = 1;
  await openGraphs(page);

  await page.getByRole("button", { name: "Add Task" }).click();
  await page.getByPlaceholder("task_id").fill("review");
  await page.getByRole("button", { name: "Add", exact: true }).click();

  const panel = page.locator('[data-task-id="review"] .graph-local-hero');
  await expect(panel).toBeVisible({ timeout: 5_000 });
  await panel.getByLabel("Prompt").fill("Keep this text");

  // The form stays: the reader's text is the only copy of it.
  const conflict = panel.getByRole("alert");
  await expect(conflict).toContainText("changed on disk");
  await expect(panel.getByLabel("Prompt")).toHaveValue("Keep this text");

  await conflict.getByRole("button", { name: "Save mine over it" }).click();
  await expect.poll(() => state.heroPatches.length).toBe(1);
  expect(state.heroPatches.at(-1)?.document).toMatchObject({ prompt: "Keep this text" });
  await expect(panel).toContainText("Saved");
});

test("a shared Hero needs no local document and refuses an unusable id", async ({ page }) => {
  const state = await mockAddTask(page);
  await openGraphs(page);

  await page.getByRole("button", { name: "Add Task" }).click();
  await page.getByPlaceholder("task_id").fill("2pass");
  // A Hero handle must start with a letter, so this choice cannot be created.
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await expect(page.locator(".graph-add-task [role=\"alert\"]")).toContainText("starts with a letter");
  expect(state.graphPatches).toHaveLength(0);

  await page.getByPlaceholder("task_id").fill("review");
  await page.getByLabel("Shared Hero").check();
  await page.getByRole("group", { name: "Add Task" }).getByRole("combobox").selectOption("reviewer");
  await page.getByRole("button", { name: "Add", exact: true }).click();

  await expect.poll(() => state.graphPatches.length).toBe(1);
  expect(state.graphPatches[0].task).toEqual({ id: "review", value: { hero: "global/reviewer", goal: expect.any(String) } });
  expect(state.graphPatches[0].localHero).toBeUndefined();
  await expect(page.locator('[data-task-id="review"]')).toBeVisible();
});
